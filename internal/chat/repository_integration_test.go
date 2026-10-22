//go:build integration

package chat

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/migrations"
	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	mobynetwork "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"net"
	"sync"
	"testing"
	"time"
)

func chatDB(t *testing.T) (context.Context, *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	dsn := func(host string, port mobynetwork.Port) string {
		return fmt.Sprintf("postgres://postgres:test-only@%s/matchlab?sslmode=disable", net.JoinHostPort(host, port.Port()))
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{Image: "postgres:17-alpine", Env: map[string]string{"POSTGRES_PASSWORD": "test-only", "POSTGRES_DB": "matchlab"}, ExposedPorts: []string{"5432/tcp"}, WaitingFor: wait.ForSQL("5432/tcp", "pgx", dsn)}, Started: true})
	if container != nil {
		t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	}
	if err != nil {
		t.Fatal(err)
	}
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", dsn(host, port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	return ctx, db
}
func TestConcurrentConversationsMessagesAndRead(t *testing.T) {
	ctx, db := chatDB(t)
	r := &Repository{DB: db}
	a, b := uuid.NewString(), uuid.NewString()
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			x, y := a, b
			if i%2 == 1 {
				x, y = y, x
			}
			c, _, err := r.Open(ctx, x, y)
			ids <- c.ID
			errs <- err
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errs)
	var id string
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for value := range ids {
		if id != "" && value != id {
			t.Fatal("duplicate pair")
		}
		id = value
	}
	allow := func(context.Context) error { return nil }
	client := uuid.NewString()
	messages := make(chan Message, 12)
	errs = make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m, _, err := r.Send(ctx, id, a, client, "hello", allow)
			messages <- m
			errs <- err
		}()
	}
	wg.Wait()
	close(messages)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first string
	for m := range messages {
		if m.Seq != 1 || first != "" && m.ID != first {
			t.Fatal("retry duplicated")
		}
		first = m.ID
	}
	if _, _, err := r.Send(ctx, id, a, client, "different", allow); !errors.Is(err, ErrConflict) {
		t.Fatal("conflict", err)
	}
	denied := errors.New("blocked")
	if _, _, err := r.Send(ctx, id, a, uuid.NewString(), "denied", func(context.Context) error { return denied }); !errors.Is(err, denied) {
		t.Fatal(err)
	}
	errs = make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := r.Send(ctx, id, b, uuid.NewString(), "reply", allow)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	c, err := r.Conversation(ctx, id, a)
	if err != nil || c.LastSeq != 13 || c.Unread != 12 || c.LastMessage == nil || c.LastMessage.Seq != 13 {
		t.Fatal("sequence or incoming count", c, err)
	}
	// A failed insert rolls back both message and counter.
	if _, _, err := r.Send(ctx, id, a, uuid.NewString(), " ", allow); err == nil {
		t.Fatal("blank text accepted")
	}
	c, _ = r.Conversation(ctx, id, a)
	if c.LastSeq != 13 {
		t.Fatal("failed insert consumed seq")
	}
	for _, seq := range []int64{12, 8, 13, 3} {
		wg.Add(1)
		go func(seq int64) {
			defer wg.Done()
			if err := r.MarkRead(ctx, id, a, seq); err != nil {
				t.Error(err)
			}
		}(seq)
	}
	wg.Wait()
	c, err = r.Conversation(ctx, id, a)
	read, _ := c.Read(a)
	if err != nil || read != 13 || c.Unread != 0 {
		t.Fatal("read regressed", c, err)
	}
	if err := r.MarkRead(ctx, id, a, 14); !errors.Is(err, ErrInvalid) {
		t.Fatal("future read", err)
	}
	outsider := uuid.NewString()
	if _, err := r.Conversation(ctx, id, outsider); !errors.Is(err, ErrMissing) {
		t.Fatal("foreign read", err)
	}
	if _, _, err := r.Send(ctx, id, outsider, uuid.NewString(), "attack", allow); !errors.Is(err, ErrMissing) {
		t.Fatal("foreign send", err)
	}
	if err := r.MarkRead(ctx, id, outsider, 1); !errors.Is(err, ErrMissing) {
		t.Fatal("foreign mark", err)
	}
	before := int64(10)
	old, err := r.Messages(ctx, id, 3, &before, nil)
	if err != nil || len(old) != 3 || old[0].Seq != 9 || old[2].Seq != 7 {
		t.Fatal("before", old, err)
	}
	after := int64(10)
	newer, err := r.Messages(ctx, id, 3, nil, &after)
	if err != nil || len(newer) != 3 || newer[0].Seq != 11 || newer[2].Seq != 13 {
		t.Fatal("after", newer, err)
	}
	other, _, err := r.Open(ctx, a, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	list, err := r.List(ctx, a, 1, nil)
	if err != nil || len(list) != 1 || list[0].ID != other.ID {
		t.Fatal("empty conversation ordering", list, err)
	}
	cursor := &Cursor{At: list[0].Activity(), ID: list[0].ID}
	list, err = r.List(ctx, a, 1, cursor)
	if err != nil || len(list) != 1 || list[0].ID != id {
		t.Fatal("keyset pagination", list, err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO chat.conversation(user_low_id,user_high_id) VALUES($1,$1)`, a); err == nil {
		t.Fatal("self chat accepted")
	}
}
