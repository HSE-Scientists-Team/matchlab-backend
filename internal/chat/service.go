package chat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/chat/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/rpcmeta"
	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Users interface {
	GetIdentity(context.Context, *upb.Empty, ...grpc.CallOption) (*upb.Identity, error)
	GetProfile(context.Context, *upb.ProfileQuery, ...grpc.CallOption) (*upb.Profile, error)
	CheckInteraction(context.Context, *upb.InteractionQuery, ...grpc.CallOption) (*upb.InteractionResult, error)
}
type Store interface {
	Open(context.Context, string, string) (Conversation, bool, error)
	Conversation(context.Context, string, string) (Conversation, error)
	List(context.Context, string, int32, *Cursor) ([]Conversation, error)
	Messages(context.Context, string, int32, *int64, *int64) ([]Message, error)
	Send(context.Context, string, string, string, string, func(context.Context) error) (Message, bool, error)
	MarkRead(context.Context, string, string, int64) error
}
type Service struct {
	pb.UnimplementedChatServiceServer
	Repo  Store
	Users Users
}

func validID(s string) bool {
	id, err := uuid.Parse(s)
	return err == nil && id != uuid.Nil && id.String() == s
}
func invalid() error {
	return status.Error(codes.InvalidArgument, "некорректные поля запроса")
}
func failure(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrMissing):
		return status.Error(codes.NotFound, "диалог не найден")
	case errors.Is(err, ErrConflict):
		return status.Error(codes.AlreadyExists, "client_message_id уже использован для другого текста")
	case errors.Is(err, ErrInvalid):
		return invalid()
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "запрос отменён")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "истёк срок запроса")
	default:
		return status.Error(codes.Internal, "ошибка сервиса Chat")
	}
}
func userFailure(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unauthenticated, codes.PermissionDenied, codes.NotFound:
		return err
	default:
		return status.Error(codes.Unavailable, "User недоступен")
	}
}
func (s *Service) actor(ctx context.Context) (string, error) {
	values := metadata.ValueFromIncomingContext(ctx, "authorization")
	if len(values) != 1 {
		return "", status.Error(codes.Unauthenticated, "требуется сеанс")
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", status.Error(codes.Unauthenticated, "требуется сеанс")
	}
	call, cancel := context.WithTimeout(rpcmeta.Forward(ctx), 3*time.Second)
	defer cancel()
	identity, err := s.Users.GetIdentity(call, &upb.Empty{})
	if err != nil {
		return "", userFailure(err)
	}
	if !validID(identity.GetUserId()) {
		return "", status.Error(codes.Unauthenticated, "недействительный сеанс")
	}
	return identity.UserId, nil
}
func (s *Service) canSend(ctx context.Context, actor, peer string) error {
	call, cancel := context.WithTimeout(rpcmeta.Forward(ctx), 3*time.Second)
	profile, err := s.Users.GetProfile(call, &upb.ProfileQuery{UserId: peer})
	cancel()
	if err != nil {
		return userFailure(err)
	}
	if profile.GetUserId() != peer {
		return status.Error(codes.Unavailable, "некорректный ответ User")
	}
	call, cancel = context.WithTimeout(rpcmeta.Forward(ctx), 3*time.Second)
	defer cancel()
	result, err := s.Users.CheckInteraction(call, &upb.InteractionQuery{FromUserId: actor, ToUserId: peer})
	if err != nil {
		return userFailure(err)
	}
	if result == nil {
		return status.Error(codes.Unavailable, "некорректный ответ User")
	}
	if result.GetBlocked() {
		return status.Error(codes.PermissionDenied, "переписка запрещена блокировкой")
	}
	return nil
}
func message(m Message) *pb.Message {
	return &pb.Message{Id: m.ID, ConversationId: m.ConversationID, Seq: m.Seq, SenderUserId: m.SenderID, ClientMessageId: m.ClientID, Body: m.Body, CreatedAtUnix: m.CreatedAt.Unix()}
}
func conversation(c Conversation, actor string) *pb.Conversation {
	read, peerRead := c.Read(actor)
	result := &pb.Conversation{Id: c.ID, PeerUserId: c.Peer(actor), LastMessageSeq: c.LastSeq, LastReadSeq: read, PeerLastReadSeq: peerRead, UnreadCount: c.Unread, CreatedAtUnix: c.CreatedAt.Unix()}
	if c.LastAt != nil {
		at := c.LastAt.Unix()
		result.LastMessageAtUnix = &at
	}
	if c.LastMessage != nil {
		result.LastMessage = message(*c.LastMessage)
	}
	return result
}
func (s *Service) OpenConversation(ctx context.Context, q *pb.OpenConversationRequest) (*pb.OpenConversationResponse, error) {
	actor, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	if q == nil || !validID(q.UserId) || actor == q.UserId {
		return nil, invalid()
	}
	if err = s.canSend(ctx, actor, q.UserId); err != nil {
		return nil, err
	}
	c, created, err := s.Repo.Open(ctx, actor, q.UserId)
	if err != nil {
		return nil, failure(err)
	}
	return &pb.OpenConversationResponse{Conversation: conversation(c, actor), Created: created}, nil
}
func limit(value, def int32) (int32, error) {
	if value == 0 {
		value = def
	}
	if value < 1 || value > 100 {
		return 0, invalid()
	}
	return value, nil
}
func decodeCursor(value, actor string) (*Cursor, error) {
	if value == "" {
		return nil, nil
	}
	if len(value) > 1024 {
		return nil, invalid()
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, invalid()
	}
	var c Cursor
	if json.Unmarshal(raw, &c) != nil || !validID(c.ID) || c.UserID != actor || c.At.IsZero() || c.At.Year() < 1 || c.At.Year() > 9999 {
		return nil, invalid()
	}
	return &c, nil
}
func (s *Service) ListConversations(ctx context.Context, q *pb.ConversationQuery) (*pb.ConversationList, error) {
	actor, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	n, err := limit(q.GetLimit(), 20)
	if err != nil {
		return nil, err
	}
	cursor, err := decodeCursor(q.GetCursor(), actor)
	if err != nil {
		return nil, err
	}
	items, err := s.Repo.List(ctx, actor, n+1, cursor)
	if err != nil {
		return nil, failure(err)
	}
	result := &pb.ConversationList{Items: []*pb.Conversation{}}
	if len(items) > int(n) {
		items = items[:n]
		last := items[len(items)-1]
		raw, _ := json.Marshal(Cursor{At: last.Activity(), ID: last.ID, UserID: actor})
		result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	for _, c := range items {
		result.Items = append(result.Items, conversation(c, actor))
	}
	return result, nil
}
func (s *Service) ListMessages(ctx context.Context, q *pb.MessageQuery) (*pb.MessageList, error) {
	actor, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	if q == nil || !validID(q.ConversationId) || q.BeforeSeq != nil && q.AfterSeq != nil || q.BeforeSeq != nil && *q.BeforeSeq <= 0 || q.AfterSeq != nil && *q.AfterSeq < 0 {
		return nil, invalid()
	}
	n, err := limit(q.Limit, 50)
	if err != nil {
		return nil, err
	}
	if _, err = s.Repo.Conversation(ctx, q.ConversationId, actor); err != nil {
		return nil, failure(err)
	}
	items, err := s.Repo.Messages(ctx, q.ConversationId, n+1, q.BeforeSeq, q.AfterSeq)
	if err != nil {
		return nil, failure(err)
	}
	result := &pb.MessageList{Items: []*pb.Message{}, HasMore: len(items) > int(n)}
	if result.HasMore {
		items = items[:n]
	}
	if q.AfterSeq == nil {
		for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
			items[i], items[j] = items[j], items[i]
		}
	}
	for _, m := range items {
		result.Items = append(result.Items, message(m))
	}
	return result, nil
}
func (s *Service) SendMessage(ctx context.Context, q *pb.SendMessageRequest) (*pb.SendMessageResponse, error) {
	actor, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	if q == nil || !validID(q.ConversationId) || !validID(q.ClientMessageId) || !utf8.ValidString(q.Body) || strings.ContainsRune(q.Body, 0) || strings.TrimSpace(q.Body) == "" || utf8.RuneCountInString(q.Body) > 10000 {
		return nil, invalid()
	}
	c, err := s.Repo.Conversation(ctx, q.ConversationId, actor)
	if err != nil {
		return nil, failure(err)
	}
	var policyErr error
	m, created, err := s.Repo.Send(ctx, c.ID, actor, q.ClientMessageId, q.Body, func(call context.Context) error {
		policyErr = s.canSend(call, actor, c.Peer(actor))
		return policyErr
	})
	if policyErr != nil {
		return nil, policyErr
	}
	if err != nil {
		return nil, failure(err)
	}
	return &pb.SendMessageResponse{Message: message(m), Created: created}, nil
}
func (s *Service) MarkRead(ctx context.Context, q *pb.MarkReadRequest) (*pb.Empty, error) {
	actor, err := s.actor(ctx)
	if err != nil {
		return nil, err
	}
	if q == nil || !validID(q.ConversationId) || q.LastReadSeq < 0 {
		return nil, invalid()
	}
	if err = s.Repo.MarkRead(ctx, q.ConversationId, actor, q.LastReadSeq); err != nil {
		return nil, failure(err)
	}
	return &pb.Empty{}, nil
}
