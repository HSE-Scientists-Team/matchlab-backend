// Package management holds request validation shared by internal management APIs.
package management

import (
	"context"
	"database/sql"
	"errors"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/management/v1"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"log/slog"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

func Invalid() error {
	return status.Error(codes.InvalidArgument, "некорректные поля запроса")
}
func ID(value string) bool {
	id, e := uuid.Parse(value)
	return e == nil && id != uuid.Nil && id.String() == value
}
func Text(value string, max int, required bool) bool {
	return utf8.ValidString(value) && strings.IndexByte(value, 0) < 0 && utf8.RuneCountInString(value) <= max && (!required || strings.TrimSpace(value) != "")
}
func Email(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	a, e := mail.ParseAddress(value)
	if e != nil || a.Address != value || len(value) > 320 {
		return "", Invalid()
	}
	return value, nil
}
func Page(p *pb.Page) (int32, int32, error) {
	l, o := p.GetLimit(), p.GetOffset()
	if l == 0 {
		l = 20
	}
	if l < 1 || l > 100 || o < 0 || o > 10000 {
		return 0, 0, Invalid()
	}
	return l, o, nil
}
func Query(q *pb.Query) (int32, int32, error) {
	l, o, e := Page(q.GetPage())
	if e != nil {
		return 0, 0, e
	}
	if !Text(q.Text, 200, false) || !Text(q.Status, 40, false) || !Text(q.Kind, 40, false) || !Text(q.Action, 120, false) || !Text(q.Result, 40, false) {
		return 0, 0, Invalid()
	}
	for _, id := range []string{q.UserId, q.AssignedAdminId, q.ActorAdminId, q.TargetUserId, q.TargetProjectId} {
		if id != "" && !ID(id) {
			return 0, 0, Invalid()
		}
	}
	if q.CreatedFromUnix < 0 || q.CreatedToUnix < 0 || q.CreatedFromUnix > 253402300799 || q.CreatedToUnix > 253402300799 || (q.CreatedToUnix != 0 && q.CreatedToUnix <= q.CreatedFromUnix) {
		return 0, 0, Invalid()
	}
	return l, o, nil
}
func Range(q *pb.StatisticsQuery) (time.Time, time.Time, error) {
	to := time.Now().UTC()
	if q.GetToUnix() > 0 {
		to = time.Unix(q.ToUnix, 0).UTC()
	}
	from := to.AddDate(0, 0, -30)
	if q.GetFromUnix() > 0 {
		from = time.Unix(q.FromUnix, 0).UTC()
	}
	if q.GetFromUnix() < 0 || q.GetToUnix() < 0 || q.GetFromUnix() > 253402300799 || q.GetToUnix() > 253402300799 || !from.Before(to) || to.Sub(from) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, Invalid()
	}
	return from, to, nil
}
func Error(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok {
		return err
	}
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "объект не найден")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "истёк срок запроса")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "запрос отменён")
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return status.Error(codes.AlreadyExists, "объект уже существует")
	}
	if pg != nil {
		slog.Error("administrative storage error", "sqlstate", pg.Code, "table", pg.TableName, "column", pg.ColumnName, "routine", pg.Routine)
	}
	return status.Error(codes.Internal, "ошибка хранилища сервиса")
}
func Conflict() error {
	return status.Error(codes.Aborted, "состояние изменилось; обновите данные")
}
func Changed(result sql.Result, err error) error {
	if err != nil {
		return Error(err)
	}
	n, e := result.RowsAffected()
	if e != nil {
		return Error(e)
	}
	if n != 1 {
		return Conflict()
	}
	return nil
}
func Event(ctx context.Context, tx *sql.Tx, table, actor, action, id, comment, before, after string, revision int32) error {
	// table comes exclusively from hard-coded call sites, never from RPC input.
	_, err := tx.ExecContext(ctx, "INSERT INTO "+table+"(actor_admin_id,action,resource_id,comment,previous_status,status,revision) VALUES($1,$2,$3,$4,$5,$6,$7)", actor, action, id, comment, before, after, revision)
	return err
}

// AllowedQuery rejects filters that the particular endpoint cannot apply.
func AllowedQuery(q *pb.Query, fields ...string) error {
	allowed := map[string]bool{"page": true}
	for _, name := range fields {
		allowed[name] = true
	}
	var invalid bool
	q.ProtoReflect().Range(func(f protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		if !allowed[string(f.Name())] {
			invalid = true
		}
		return !invalid
	})
	if invalid {
		return Invalid()
	}
	return nil
}
