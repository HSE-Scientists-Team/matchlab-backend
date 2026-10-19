package profile

import (
	"context"
	"crypto/subtle"
	"errors"
	authpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/auth/v1"
	media "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/adminauth"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

type Auth interface {
	ValidateSession(context.Context, *authpb.ValidateSessionRequest, ...grpc.CallOption) (*authpb.ValidateSessionResponse, error)
}
type Media interface {
	GetFile(context.Context, *media.GetFileRequest, ...grpc.CallOption) (*media.GetFileResponse, error)
	CreateDownloadURL(context.Context, *media.CreateDownloadURLRequest, ...grpc.CallOption) (*media.CreateDownloadURLResponse, error)
}
type Service struct {
	pb.UnimplementedProfileServiceServer
	Repo         *Repository
	Auth         Auth
	Media        Media
	ProjectToken string
}

func validID(id string) bool {
	v, err := uuid.Parse(id)
	return err == nil && v != uuid.Nil && id == v.String()
}
func invalid() error {
	return status.Error(codes.InvalidArgument, "некорректные поля запроса")
}
func rpcError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, "объект не найден")
	case errors.Is(err, ErrConflict):
		return status.Error(codes.Aborted, "состояние изменилось или операция уже выполнена")
	case errors.Is(err, ErrForbidden):
		return status.Error(codes.PermissionDenied, "операция запрещена")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "запрос отменён")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "истёк срок выполнения")
	}
	return status.Error(codes.Internal, "ошибка сервиса User")
}
func (s *Service) actor(ctx context.Context, required, moderator bool) (string, error) {
	if moderator {
		a, e := adminauth.Verify(ctx, "user")
		return a.ID, e
	}
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) == 0 && !required {
		return "", nil
	}
	if len(values) != 1 {
		return "", status.Error(codes.Unauthenticated, "требуется сеанс")
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", status.Error(codes.Unauthenticated, "требуется сеанс")
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	session, err := s.Auth.ValidateSession(call, &authpb.ValidateSessionRequest{Token: parts[1]})
	if err != nil {
		if status.Code(err) == codes.Unauthenticated {
			return "", status.Error(codes.Unauthenticated, "недействительный сеанс")
		}
		return "", status.Error(codes.Unavailable, "Auth недоступен")
	}
	if !validID(session.GetUserId()) {
		return "", status.Error(codes.Unauthenticated, "недействительный сеанс")
	}
	role, _, err := s.Repo.Identity(ctx, session.UserId)
	if errors.Is(err, ErrNotFound) {
		return "", status.Error(codes.PermissionDenied, "требуется активный аккаунт с подтверждённой почтой")
	}
	if err != nil {
		return "", rpcError(err)
	}
	if moderator && role != "moderator" && role != "administrator" {
		return "", status.Error(codes.PermissionDenied, "требуются права модератора")
	}
	return session.UserId, nil
}
func page(p *pb.Page) (int32, int32, error) {
	limit, offset := p.GetLimit(), p.GetOffset()
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 10000 {
		return 0, 0, invalid()
	}
	return limit, offset, nil
}
func text(v string, max int, required bool) bool {
	return utf8.ValidString(v) && utf8.RuneCountInString(v) <= max && (!required || strings.TrimSpace(v) != "")
}
func tags(values []string, allowed map[string]bool) bool {
	if len(values) > 30 {
		return false
	}
	seen := map[string]bool{}
	for i, v := range values {
		v = strings.TrimSpace(v)
		key := strings.ToLower(v)
		if !text(v, 80, true) || seen[key] || (allowed != nil && !allowed[v]) {
			return false
		}
		seen[key] = true
		values[i] = v
	}
	return true
}
func validLink(value string) bool {
	if value == "" {
		return true
	}
	u, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}
func validateProfile(p *pb.ProfileInput) error {
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	if !text(p.DisplayName, 255, true) || !text(p.University, 255, false) || !text(p.Specialization, 255, false) || !text(p.StudyLevel, 80, false) || !text(p.About, 10000, false) || p.StudyYear < 0 || p.StudyYear > 12 || !tags(p.Interests, nil) || !tags(p.Skills, nil) || !tags(p.ProjectTypes, map[string]bool{"scientific": true, "practical": true}) {
		return invalid()
	}
	if p.AvatarFileId != "" && !validID(p.AvatarFileId) {
		return invalid()
	}
	if p.ContactEmail != "" {
		a, err := mail.ParseAddress(p.ContactEmail)
		if err != nil || a.Address != p.ContactEmail || len(p.ContactEmail) > 320 {
			return invalid()
		}
	}
	if !text(p.Telegram, 100, false) {
		return invalid()
	}
	return nil
}
func validateAchievement(p *pb.AchievementInput) error {
	p.Title = strings.TrimSpace(p.Title)
	k := p.Kind
	if k != "education" && k != "certificate" && k != "scientific" && k != "project" {
		return invalid()
	}
	if !text(p.Title, 255, true) || !text(p.Description, 10000, k == "scientific" || k == "project") || !text(p.Institution, 255, k == "education" || k == "certificate") || p.GraduationYear < 0 || (p.GraduationYear != 0 && (p.GraduationYear < 1900 || p.GraduationYear > int32(time.Now().Year()+20))) || !validLink(p.Url) {
		return invalid()
	}
	if p.EvidenceFileId != "" && !validID(p.EvidenceFileId) {
		return invalid()
	}
	if p.Id != "" && (!validID(p.Id) || p.ExpectedRevision < 1) {
		return invalid()
	}
	return nil
}
func (s *Service) file(ctx context.Context, owner, id string, avatar bool) error {
	if id == "" {
		return nil
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	f, err := s.Media.GetFile(call, &media.GetFileRequest{UserId: owner, FileId: id})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return status.Error(codes.NotFound, "файл не найден")
		}
		return status.Error(codes.Unavailable, "Media недоступен")
	}
	if f.GetFile().GetOwnerUserId() != owner {
		return status.Error(codes.NotFound, "файл не найден")
	}
	if f.GetFile().GetStatus() != media.FileStatus_FILE_STATUS_READY {
		return status.Error(codes.FailedPrecondition, "файл не загружен")
	}
	if avatar && (!f.File.IsPublic || (f.File.ContentType != "image/jpeg" && f.File.ContentType != "image/png")) {
		return status.Error(codes.FailedPrecondition, "аватар должен быть публичным PNG/JPEG")
	}
	if !avatar && f.File.IsPublic {
		return status.Error(codes.FailedPrecondition, "подтверждающий документ должен быть приватным")
	}
	return nil
}
func (s *Service) GetProfile(ctx context.Context, q *pb.ProfileQuery) (*pb.Profile, error) {
	viewer, err := s.actor(ctx, q.UserId == "", false)
	if err != nil {
		return nil, err
	}
	id := q.UserId
	if id == "" {
		id = viewer
	}
	if !validID(id) {
		return nil, invalid()
	}
	// Public profile rehydration, including recommendations, requires the same
	// active confirmed account as discovery. Do not revive legacy unconfirmed accounts.
	if _, _, err := s.Repo.Identity(ctx, id); err != nil {
		return nil, rpcError(err)
	}
	p, privacy, err := s.Repo.Profile(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	visible, err := s.Repo.CanSeeContacts(ctx, id, viewer, privacy.ShowContacts)
	if err != nil {
		return nil, rpcError(err)
	}
	if !visible {
		p.Details.ContactEmail = ""
		p.Details.Telegram = ""
	}
	return p, nil
}
func (s *Service) UpdateProfile(ctx context.Context, p *pb.ProfileInput) (*pb.Profile, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if err = validateProfile(p); err != nil {
		return nil, err
	}
	if err = s.file(ctx, id, p.AvatarFileId, true); err != nil {
		return nil, err
	}
	if err = s.Repo.SaveProfile(ctx, id, p); err != nil {
		return nil, rpcError(err)
	}
	return s.GetProfile(ctx, &pb.ProfileQuery{})
}
func (s *Service) GetSettings(ctx context.Context, _ *pb.Empty) (*pb.PrivacySettings, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	_, p, err := s.Repo.Profile(ctx, id)
	return p, rpcError(err)
}
func (s *Service) UpdateSettings(ctx context.Context, p *pb.PrivacySettings) (*pb.PrivacySettings, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	return p, rpcError(s.Repo.Settings(ctx, id, p))
}
func redact(a *pb.Achievement, viewer string) {
	if a.UserId != viewer {
		a.Details.EvidenceFileId = ""
		a.ModerationComment = ""
	}
}
func (s *Service) ListAchievements(ctx context.Context, q *pb.AchievementQuery) (*pb.AchievementList, error) {
	viewer, err := s.actor(ctx, q.UserId == "", false)
	if err != nil {
		return nil, err
	}
	id := q.UserId
	if id == "" {
		id = viewer
	}
	if !validID(id) {
		return nil, invalid()
	}
	if _, _, err = s.Repo.Identity(ctx, id); err != nil {
		return nil, rpcError(err)
	}
	limit, offset, err := page(q.Page)
	if err != nil {
		return nil, err
	}
	if q.Kind != "" && q.Kind != "education" && q.Kind != "certificate" && q.Kind != "scientific" && q.Kind != "project" {
		return nil, invalid()
	}
	res, err := s.Repo.Achievements(ctx, id, q.Kind, false, limit, offset)
	if err != nil {
		return nil, rpcError(err)
	}
	for _, a := range res.Items {
		redact(a, viewer)
	}
	return res, nil
}
func (s *Service) owned(ctx context.Context, id, owner string) (*pb.Achievement, error) {
	if !validID(id) {
		return nil, invalid()
	}
	a, err := s.Repo.Achievement(ctx, id)
	if err != nil {
		return nil, rpcError(err)
	}
	if a.UserId != owner {
		return nil, status.Error(codes.NotFound, "объект не найден")
	}
	return a, nil
}
func (s *Service) SaveAchievement(ctx context.Context, p *pb.AchievementInput) (*pb.Achievement, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if err = validateAchievement(p); err != nil {
		return nil, err
	}
	if p.Id != "" {
		if _, err = s.owned(ctx, p.Id, id); err != nil {
			return nil, err
		}
	}
	if err = s.file(ctx, id, p.EvidenceFileId, false); err != nil {
		return nil, err
	}
	a, err := s.Repo.SaveAchievement(ctx, id, p)
	return a, rpcError(err)
}
func (s *Service) DeleteAchievement(ctx context.Context, p *pb.AchievementID) (*pb.Empty, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(p.Id) {
		return nil, invalid()
	}
	return &pb.Empty{}, rpcError(s.Repo.DeleteAchievement(ctx, p.Id, id))
}
func (s *Service) SubmitAchievement(ctx context.Context, p *pb.AchievementID) (*pb.Achievement, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	a, err := s.owned(ctx, p.Id, id)
	if err != nil {
		return nil, err
	}
	if a.Details.EvidenceFileId == "" && a.Details.Url == "" {
		return nil, status.Error(codes.FailedPrecondition, "требуется ссылка или подтверждающий документ")
	}
	if err = s.file(ctx, id, a.Details.EvidenceFileId, false); err != nil {
		return nil, err
	}
	a, err = s.Repo.Submit(ctx, p.Id, id, a.Revision)
	return a, rpcError(err)
}
func (s *Service) ListBlocks(ctx context.Context, p *pb.Page) (*pb.BlockList, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	limit, offset, err := page(p)
	if err != nil {
		return nil, err
	}
	res, err := s.Repo.Blocks(ctx, id, limit, offset)
	return res, rpcError(err)
}
func (s *Service) changeBlock(ctx context.Context, p *pb.UserID, remove bool) (*pb.Empty, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(p.UserId) || id == p.UserId {
		return nil, invalid()
	}
	if !remove {
		if _, _, err = s.Repo.Identity(ctx, p.UserId); err != nil {
			return nil, rpcError(err)
		}
	}
	return &pb.Empty{}, rpcError(s.Repo.Block(ctx, id, p.UserId, remove))
}
func (s *Service) BlockUser(ctx context.Context, p *pb.UserID) (*pb.Empty, error) {
	return s.changeBlock(ctx, p, false)
}
func (s *Service) UnblockUser(ctx context.Context, p *pb.UserID) (*pb.Empty, error) {
	return s.changeBlock(ctx, p, true)
}
func (s *Service) CheckInteraction(ctx context.Context, p *pb.InteractionQuery) (*pb.InteractionResult, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(p.FromUserId) || !validID(p.ToUserId) {
		return nil, invalid()
	}
	if id != p.FromUserId && id != p.ToUserId {
		return nil, status.Error(codes.PermissionDenied, "чужая пара пользователей")
	}
	b, err := s.Repo.Blocked(ctx, p.FromUserId, p.ToUserId)
	return &pb.InteractionResult{Blocked: b}, rpcError(err)
}
func (s *Service) ListVerificationQueue(ctx context.Context, p *pb.Page) (*pb.AchievementList, error) {
	if _, err := s.actor(ctx, true, true); err != nil {
		return nil, err
	}
	limit, offset, err := page(p)
	if err != nil {
		return nil, err
	}
	res, err := s.Repo.Achievements(ctx, "", "", true, limit, offset)
	return res, rpcError(err)
}
func (s *Service) ReviewAchievement(ctx context.Context, p *pb.VerificationDecision) (*pb.Achievement, error) {
	id, err := s.actor(ctx, true, true)
	if err != nil {
		return nil, err
	}
	if !validID(p.Id) || p.ExpectedRevision < 1 || !text(p.Comment, 2000, !p.Approve) {
		return nil, invalid()
	}
	a, err := s.Repo.Achievement(ctx, p.Id)
	if err != nil {
		return nil, rpcError(err)
	}

	claims, claimsErr := adminauth.Verify(ctx, "user")
	if claimsErr != nil || claims.Email == "" {
		return nil, status.Error(codes.PermissionDenied, "неполная служебная идентификация")
	}
	var ownerEmail string
	if err = s.Repo.DB.QueryRowContext(ctx, `SELECT email FROM users.user_account WHERE id=$1`, a.UserId).Scan(&ownerEmail); err != nil {
		return nil, rpcError(err)
	}
	if strings.EqualFold(ownerEmail, claims.Email) {
		return nil, status.Error(codes.PermissionDenied, "нельзя проверять собственные достижения")
	}
	if p.Approve {
		if err = s.file(ctx, a.UserId, a.Details.EvidenceFileId, false); err != nil {
			return nil, err
		}
	}
	res, err := s.Repo.DecideAdmin(ctx, id, p)
	return res, rpcError(err)
}
func (s *Service) GetAchievementEvidence(ctx context.Context, p *pb.AchievementID) (*pb.Evidence, error) {
	if _, err := s.actor(ctx, true, true); err != nil {
		return nil, err
	}
	if !validID(p.Id) {
		return nil, invalid()
	}
	a, err := s.Repo.Achievement(ctx, p.Id)
	if err != nil {
		return nil, rpcError(err)
	}
	if a.Details.EvidenceFileId == "" {
		return nil, status.Error(codes.NotFound, "доказательство отсутствует")
	}
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	res, err := s.Media.CreateDownloadURL(call, &media.CreateDownloadURLRequest{UserId: a.UserId, FileId: a.Details.EvidenceFileId})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "Media недоступен")
	}
	return &pb.Evidence{DownloadUrl: res.GetDownloadUrl(), ExpiresAtUnix: res.GetExpiresAtUnix()}, nil
}
func (s *Service) RecordProjectContext(ctx context.Context, p *pb.ProjectContext) (*pb.Empty, error) {
	tokens := metadata.ValueFromIncomingContext(ctx, "x-matchlab-project-token")
	if s.ProjectToken == "" || len(tokens) != 1 || subtle.ConstantTimeCompare([]byte(tokens[0]), []byte(s.ProjectToken)) != 1 {
		return nil, status.Error(codes.PermissionDenied, "требуется сервис Projects")
	}
	if p.SourceRevision < 1 || (p.State != "active" && p.State != "completed" && p.State != "cancelled") || (p.State == "completed") != (p.CompletedAtUnix > 0) {
		return nil, invalid()
	}

	if !validID(p.ProjectId) || !validID(p.InitiatorUserId) || !text(p.Title, 255, true) || p.CompletedAtUnix < 0 || p.CompletedAtUnix > time.Now().Unix() || len(p.ParticipantUserIds) < 1 || len(p.ParticipantUserIds) > 3001 {
		return nil, invalid()
	}
	seen := map[string]bool{}
	for _, id := range p.ParticipantUserIds {
		if !validID(id) || seen[id] {
			return nil, invalid()
		}
		seen[id] = true
	}
	if !seen[p.InitiatorUserId] {
		return nil, invalid()
	}
	return &pb.Empty{}, rpcError(s.Repo.RecordProject(ctx, p))
}
func (s *Service) ListReviews(ctx context.Context, p *pb.ReviewQuery) (*pb.ReviewList, error) {
	id, err := s.actor(ctx, p.UserId == "", false)
	if err != nil {
		return nil, err
	}
	if p.UserId != "" {
		id = p.UserId
	}
	if !validID(id) {
		return nil, invalid()
	}
	if _, _, err = s.Repo.Identity(ctx, id); err != nil {
		return nil, rpcError(err)
	}
	limit, offset, err := page(p.Page)
	if err != nil {
		return nil, err
	}
	res, err := s.Repo.Reviews(ctx, id, limit, offset)
	return res, rpcError(err)
}
func (s *Service) CreateReview(ctx context.Context, p *pb.ReviewInput) (*pb.Review, error) {
	id, err := s.actor(ctx, true, false)
	if err != nil {
		return nil, err
	}
	if !validID(p.ProjectId) || !validID(p.TargetUserId) || id == p.TargetUserId || !text(p.Title, 255, true) || !text(p.Text, 10000, true) || p.Rating < 1 || p.Rating > 5 {
		return nil, invalid()
	}
	res, err := s.Repo.CreateReview(ctx, id, p)
	return res, rpcError(err)
}

func (s *Service) GetAchievement(ctx context.Context, p *pb.AchievementID) (*pb.Achievement, error) {
	viewer, err := s.actor(ctx, false, false)
	if err != nil {
		return nil, err
	}
	if !validID(p.Id) {
		return nil, invalid()
	}
	a, err := s.Repo.Achievement(ctx, p.Id)
	if err != nil {
		return nil, rpcError(err)
	}
	if _, _, err = s.Repo.Identity(ctx, a.UserId); err != nil {
		return nil, rpcError(err)
	}
	redact(a, viewer)
	return a, nil
}
