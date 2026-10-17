// Package admin owns staff identity, sessions, permissions and the administrative facade.
package admin

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/admin/v1"
	mpb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	m "github.com/HSE-Scientists-Team/matchlab-backend/pkg/management"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strings"
	"time"
)

var loginSlots = make(chan struct{}, 4)

const SessionTTL = 8 * time.Hour
const bcryptCost = 12

// A valid bcrypt hash makes unknown-account comparisons follow the normal cost path.
const dummyHash = "$2a$12$R9h/cIPz0gi.URNNX3kh2OPST9/PgBkqquzi.Ss7KIUgO2t0jWMUW"

type Service struct {
	pb.UnimplementedAdminServiceServer
	DB         *sql.DB
	SigningKey ed25519.PrivateKey
	Users      upb.UserManagementServiceClient
	Profiles   upb.ProfileServiceClient
	Support    spb.SupportManagementServiceClient
	Projects   ppb.ProjectServiceClient
}
type identity struct {
	account     *pb.StaffAccount
	sessionHash string
	version     int32
}
type identityKey struct{}

func hash(value string) string        { v := sha256.Sum256([]byte(value)); return hex.EncodeToString(v[:]) }
func passwordValid(value string) bool { return len(value) >= 12 && len(value) <= 72 }
func roleValid(v string) bool         { return v == "administrator" || v == "moderator" }
func unauthorized() error {
	return status.Error(codes.Unauthenticated, "недействительный административный сеанс")
}

const accountSelect = `SELECT id::text,email,display_name,role,active,revision,extract(epoch FROM created_at)::bigint,coalesce(extract(epoch FROM last_login_at)::bigint,0) FROM admin.account `

type scanner interface{ Scan(...any) error }

func account(row scanner) (*pb.StaffAccount, error) {
	v := &pb.StaffAccount{}
	e := row.Scan(&v.Id, &v.Email, &v.DisplayName, &v.Role, &v.Active, &v.Revision, &v.CreatedAtUnix, &v.LastLoginAtUnix)
	return v, m.Error(e)
}
func (s *Service) authenticate(ctx context.Context) (identity, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 {
		return identity{}, unauthorized()
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return identity{}, unauthorized()
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil || len(raw) != 32 {
		return identity{}, unauthorized()
	}
	v := &pb.StaffAccount{}
	i := identity{account: v, sessionHash: hash(parts[1])}
	e = s.DB.QueryRowContext(ctx, `SELECT a.id::text,a.email,a.display_name,a.role,a.active,a.revision,extract(epoch FROM a.created_at)::bigint,coalesce(extract(epoch FROM a.last_login_at)::bigint,0),a.session_version FROM admin.session s JOIN admin.account a ON a.id=s.admin_id WHERE s.token_hash=$1 AND s.expires_at>now() AND s.session_version=a.session_version AND a.active`, i.sessionHash).Scan(&v.Id, &v.Email, &v.DisplayName, &v.Role, &v.Active, &v.Revision, &v.CreatedAtUnix, &v.LastLoginAtUnix, &i.version)
	if errors.Is(e, sql.ErrNoRows) {
		return identity{}, unauthorized()
	}
	if e != nil {
		return identity{}, m.Error(e)
	}
	return i, nil
}
func self(ctx context.Context) (identity, error) {
	i, ok := ctx.Value(identityKey{}).(identity)
	if !ok {
		return identity{}, unauthorized()
	}
	return i, nil
}
func administrator(ctx context.Context) (identity, error) {
	i, e := self(ctx)
	if e != nil {
		return i, e
	}
	if i.account.Role != "administrator" {
		return i, status.Error(codes.PermissionDenied, "требуется администратор")
	}
	return i, nil
}
func (s *Service) UnaryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
	if !strings.HasPrefix(info.FullMethod, "/matchlab.admin.v1.AdminService/") || strings.HasSuffix(info.FullMethod, "/Login") {
		return next(ctx, req)
	}
	i, e := s.authenticate(ctx)
	if e != nil {
		return nil, e
	}
	return next(context.WithValue(ctx, identityKey{}, i), req)
}

// Login attempts for the same normalized address serialize, including across replicas.
func (s *Service) Login(ctx context.Context, q *pb.LoginInput) (*pb.LoginResult, error) {
	select {
	case loginSlots <- struct{}{}:
		defer func() { <-loginSlots }()
	default:
		return nil, status.Error(codes.Unavailable, "вход временно перегружен; повторите позже")
	}
	email, e := m.Email(q.Email)
	if e != nil || len(q.Password) > 72 || q.Password == "" {
		return nil, status.Error(codes.Unauthenticated, "неверный email или пароль")
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	key := hash(email)
	if _, e = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('admin-login:'||$1,0))`, key); e != nil {
		return nil, m.Error(e)
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO admin.login_throttle(key_hash) VALUES($1) ON CONFLICT(key_hash) DO UPDATE SET failures=CASE WHEN admin.login_throttle.window_started_at<now()-interval '15 minutes' THEN 0 ELSE admin.login_throttle.failures END,window_started_at=CASE WHEN admin.login_throttle.window_started_at<now()-interval '15 minutes' THEN now() ELSE admin.login_throttle.window_started_at END`, key); e != nil {
		return nil, m.Error(e)
	}
	var attempts int
	if e = tx.QueryRowContext(ctx, `SELECT failures FROM admin.login_throttle WHERE key_hash=$1`, key).Scan(&attempts); e != nil {
		return nil, m.Error(e)
	}
	if attempts >= 5 {
		return nil, status.Error(codes.ResourceExhausted, "слишком много попыток входа; повторите через 15 минут")
	}
	var id, pwd string
	var active bool
	var version int32
	e = tx.QueryRowContext(ctx, `SELECT id::text,password_hash,active,session_version FROM admin.account WHERE email=$1 FOR UPDATE`, email).Scan(&id, &pwd, &active, &version)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, m.Error(e)
	}
	if e != nil {
		pwd = dummyHash
	}
	valid := bcrypt.CompareHashAndPassword([]byte(pwd), []byte(q.Password)) == nil
	if !valid || !active || id == "" {
		if _, e = tx.ExecContext(ctx, `UPDATE admin.login_throttle SET failures=failures+1 WHERE key_hash=$1`, key); e != nil {
			return nil, m.Error(e)
		}
		if e = tx.Commit(); e != nil {
			return nil, m.Error(e)
		}
		return nil, status.Error(codes.Unauthenticated, "неверный email или пароль")
	}
	var random [32]byte
	if _, e = rand.Read(random[:]); e != nil {
		return nil, m.Error(e)
	}
	token := base64.RawURLEncoding.EncodeToString(random[:])
	expires := time.Now().UTC().Add(SessionTTL)
	if _, e = tx.ExecContext(ctx, `INSERT INTO admin.session(token_hash,admin_id,session_version,expires_at) VALUES($1,$2,$3,$4)`, hash(token), id, version, expires); e != nil {
		return nil, m.Error(e)
	}
	if _, e = tx.ExecContext(ctx, `UPDATE admin.account SET last_login_at=now(),updated_at=now() WHERE id=$1`, id); e != nil {
		return nil, m.Error(e)
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM admin.login_throttle WHERE key_hash=$1`, key); e != nil {
		return nil, m.Error(e)
	}
	a, e := account(tx.QueryRowContext(ctx, accountSelect+`WHERE id=$1`, id))
	if e != nil {
		return nil, e
	}
	if _, e = audit(ctx, tx, id, "session.login", id, "", "succeeded"); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return &pb.LoginResult{SessionToken: token, ExpiresAtUnix: expires.Unix(), Account: a}, nil
}
func (s *Service) GetMe(ctx context.Context, _ *mpb.Empty) (*pb.StaffAccount, error) {
	i, e := self(ctx)
	return i.account, e
}
func (s *Service) Logout(ctx context.Context, _ *mpb.Empty) (*mpb.Empty, error) {
	i, e := self(ctx)
	if e != nil {
		return nil, e
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `DELETE FROM admin.session WHERE token_hash=$1`, i.sessionHash); e != nil {
		return nil, m.Error(e)
	}
	if _, e = audit(ctx, tx, i.account.Id, "session.logout", i.account.Id, "", "succeeded"); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return &mpb.Empty{}, nil
}
func (s *Service) ListStaff(ctx context.Context, q *mpb.Query) (*pb.StaffList, error) {
	if _, e := administrator(ctx); e != nil {
		return nil, e
	}
	if e := m.AllowedQuery(q, "text", "status"); e != nil {
		return nil, e
	}
	l, o, e := m.Query(q)
	if e != nil {
		return nil, e
	}
	if q.Status != "" && q.Status != "active" && q.Status != "inactive" {
		return nil, m.Invalid()
	}
	where := `WHERE ($1='' OR position(lower($1) in lower(email||' '||display_name))>0) AND ($2='' OR active=($2='active'))`
	tx, e := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	out := &pb.StaffList{Items: []*pb.StaffAccount{}}
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM admin.account `+where, q.Text, q.Status).Scan(&out.Total); e != nil {
		return nil, m.Error(e)
	}
	rows, e := tx.QueryContext(ctx, accountSelect+where+` ORDER BY created_at,id LIMIT $3 OFFSET $4`, q.Text, q.Status, l, o)
	if e != nil {
		return nil, m.Error(e)
	}
	defer rows.Close()
	for rows.Next() {
		v, err := account(rows)
		if err != nil {
			return nil, err
		}
		out.Items = append(out.Items, v)
	}
	if e = rows.Err(); e != nil {
		return nil, m.Error(e)
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return out, nil
}
func (s *Service) GetStaff(ctx context.Context, q *mpb.ID) (*pb.StaffAccount, error) {
	if _, e := administrator(ctx); e != nil {
		return nil, e
	}
	if !m.ID(q.Id) {
		return nil, m.Invalid()
	}
	return account(s.DB.QueryRowContext(ctx, accountSelect+`WHERE id=$1`, q.Id))
}
func (s *Service) CreateStaff(ctx context.Context, q *pb.StaffInput) (*pb.StaffAccount, error) {
	i, e := administrator(ctx)
	if e != nil {
		return nil, e
	}
	email, e := m.Email(q.Email)
	if e != nil || !roleValid(q.Role) || !m.Text(q.DisplayName, 120, true) || !passwordValid(q.Password) {
		return nil, m.Invalid()
	}
	pwd, e := bcrypt.GenerateFromPassword([]byte(q.Password), bcryptCost)
	if e != nil {
		return nil, m.Error(e)
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	var id string
	e = tx.QueryRowContext(ctx, `INSERT INTO admin.account(email,display_name,password_hash,role,created_by_admin_id) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, email, strings.TrimSpace(q.DisplayName), string(pwd), q.Role, i.account.Id).Scan(&id)
	if e != nil {
		return nil, m.Error(e)
	}
	if _, e = audit(ctx, tx, i.account.Id, "staff.create", id, "", "succeeded"); e != nil {
		return nil, e
	}
	v, e := account(tx.QueryRowContext(ctx, accountSelect+`WHERE id=$1`, id))
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return v, nil
}
func lockStaff(ctx context.Context, tx *sql.Tx) error {
	_, e := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(350035)`)
	return m.Error(e)
}
func (s *Service) UpdateStaff(ctx context.Context, q *pb.StaffUpdate) (*pb.StaffAccount, error) {
	i, e := administrator(ctx)
	if e != nil {
		return nil, e
	}
	if !m.ID(q.Id) || !roleValid(q.Role) || !m.Text(q.DisplayName, 120, true) || !m.Text(q.Reason, 2000, true) || q.ExpectedRevision < 1 {
		return nil, m.Invalid()
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	if e = lockStaff(ctx, tx); e != nil {
		return nil, e
	}
	old, e := account(tx.QueryRowContext(ctx, accountSelect+`WHERE id=$1 FOR UPDATE`, q.Id))
	if e != nil {
		return nil, e
	}
	if old.Revision != q.ExpectedRevision {
		return nil, m.Conflict()
	}
	if old.Active && old.Role == "administrator" && (!q.Active || q.Role != "administrator") {
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM admin.account WHERE active AND role='administrator'`).Scan(&count); e != nil {
			return nil, m.Error(e)
		}
		if count <= 1 {
			return nil, status.Error(codes.FailedPrecondition, "нельзя отключить последнего активного администратора")
		}
	}
	_, e = tx.ExecContext(ctx, `UPDATE admin.account SET display_name=$2,role=$3,active=$4,revision=revision+1,session_version=session_version+1,updated_at=now() WHERE id=$1`, q.Id, strings.TrimSpace(q.DisplayName), q.Role, q.Active)
	if e != nil {
		return nil, m.Error(e)
	}
	if _, e = audit(ctx, tx, i.account.Id, "staff.update", q.Id, q.Reason, "succeeded"); e != nil {
		return nil, e
	}
	v, e := account(tx.QueryRowContext(ctx, accountSelect+`WHERE id=$1`, q.Id))
	if e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return v, nil
}
func (s *Service) ChangePassword(ctx context.Context, q *pb.PasswordChange) (*mpb.Empty, error) {
	i, e := self(ctx)
	if e != nil {
		return nil, e
	}
	if !passwordValid(q.NewPassword) || len(q.CurrentPassword) > 72 {
		return nil, m.Invalid()
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	var old string
	var version int32
	if e = tx.QueryRowContext(ctx, `SELECT password_hash,session_version FROM admin.account WHERE id=$1 AND active FOR UPDATE`, i.account.Id).Scan(&old, &version); e != nil {
		return nil, m.Error(e)
	}
	if version != i.version {
		return nil, unauthorized()
	}
	if bcrypt.CompareHashAndPassword([]byte(old), []byte(q.CurrentPassword)) != nil {
		return nil, status.Error(codes.Unauthenticated, "неверный текущий пароль")
	}
	pwd, e := bcrypt.GenerateFromPassword([]byte(q.NewPassword), bcryptCost)
	if e != nil {
		return nil, m.Error(e)
	}
	if _, e = tx.ExecContext(ctx, `UPDATE admin.account SET password_hash=$2,session_version=session_version+1,revision=revision+1,updated_at=now() WHERE id=$1`, i.account.Id, string(pwd)); e != nil {
		return nil, m.Error(e)
	}
	if _, e = audit(ctx, tx, i.account.Id, "staff.password", i.account.Id, "", "succeeded"); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return &mpb.Empty{}, nil
}
func (s *Service) ResetStaffPassword(ctx context.Context, q *pb.PasswordReset) (*mpb.Empty, error) {
	i, e := administrator(ctx)
	if e != nil {
		return nil, e
	}
	if !m.ID(q.Id) || !passwordValid(q.Password) || !m.Text(q.Reason, 2000, true) || q.ExpectedRevision < 1 {
		return nil, m.Invalid()
	}
	if q.Id == i.account.Id {
		return nil, status.Error(codes.FailedPrecondition, "для своего пароля используйте ChangePassword")
	}
	pwd, e := bcrypt.GenerateFromPassword([]byte(q.Password), bcryptCost)
	if e != nil {
		return nil, m.Error(e)
	}
	return s.reset(ctx, i, q.Id, string(pwd), q.Reason, "staff.reset_password", q.ExpectedRevision)
}
func (s *Service) RevokeStaffSessions(ctx context.Context, q *mpb.ID) (*mpb.Empty, error) {
	i, e := administrator(ctx)
	if e != nil {
		return nil, e
	}
	if !m.ID(q.Id) {
		return nil, m.Invalid()
	}
	return s.reset(ctx, i, q.Id, "", "", "staff.revoke_sessions", 0)
}
func (s *Service) reset(ctx context.Context, i identity, id, pwd, reason, action string, expectedRevision int32) (*mpb.Empty, error) {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, m.Error(e)
	}
	defer tx.Rollback()
	result, e := tx.ExecContext(ctx, `UPDATE admin.account SET password_hash=CASE WHEN $2='' THEN password_hash ELSE $2 END,session_version=session_version+1,revision=revision+1,updated_at=now() WHERE id=$1 AND ($3::integer=0 OR revision=$3)`, id, pwd, expectedRevision)
	if e = m.Changed(result, e); e != nil {
		return nil, e
	}
	if _, e = audit(ctx, tx, i.account.Id, action, id, reason, "succeeded"); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, m.Error(e)
	}
	return &mpb.Empty{}, nil
}

// Bootstrap is an operator-only action. It creates exactly the first administrator.
func Bootstrap(ctx context.Context, db *sql.DB, email, password string) (string, error) {
	email, e := m.Email(email)
	if e != nil || !passwordValid(password) {
		return "", m.Invalid()
	}
	pwd, e := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if e != nil {
		return "", e
	}
	tx, e := db.BeginTx(ctx, nil)
	if e != nil {
		return "", e
	}
	defer tx.Rollback()
	if e = lockStaff(ctx, tx); e != nil {
		return "", e
	}
	var count int
	if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM admin.account`).Scan(&count); e != nil {
		return "", e
	}
	if count != 0 {
		return "", status.Error(codes.FailedPrecondition, "служебные аккаунты уже существуют")
	}
	var id string
	if e = tx.QueryRowContext(ctx, `INSERT INTO admin.account(email,display_name,password_hash,role) VALUES($1,'Administrator',$2,'administrator') RETURNING id::text`, email, string(pwd)).Scan(&id); e != nil {
		return "", m.Error(e)
	}
	if _, e = audit(ctx, tx, id, "staff.bootstrap", id, "operator bootstrap", "succeeded"); e != nil {
		return "", e
	}
	if e = tx.Commit(); e != nil {
		return "", e
	}
	return id, nil
}
