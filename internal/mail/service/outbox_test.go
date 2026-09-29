package service

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/mail/repository"
)

type fakeOutbox struct {
	key, recipient    string
	ciphertext, nonce []byte
	expires           time.Time
	job               *repository.Job
	sent              bool
	failed            bool
	markErr           error
}

func (f *fakeOutbox) Enqueue(_ context.Context, key, recipient string, ciphertext, nonce []byte, expires time.Time) error {
	f.key, f.recipient, f.ciphertext, f.nonce, f.expires = key, recipient, ciphertext, nonce, expires
	return nil
}
func (f *fakeOutbox) Claim(context.Context, time.Duration) (*repository.Job, error) {
	return f.job, nil
}
func (f *fakeOutbox) MarkSent(context.Context, int64) error { f.sent = true; return f.markErr }
func (f *fakeOutbox) MarkFailed(context.Context, int64, int, time.Duration, string) error {
	f.failed = true
	return f.markErr
}
func (f *fakeOutbox) Cleanup(context.Context, time.Duration) error { return nil }

type fakeSender struct {
	recipient, token string
	expires          time.Time
	err              error
}

func (f *fakeSender) SendVerification(_ context.Context, recipient, token string, expires time.Time) error {
	f.recipient, f.token, f.expires = recipient, token, expires
	return f.err
}

func TestEnqueueEncryptsTokenAndWorkerDeliversIt(t *testing.T) {
	store := &fakeOutbox{}
	key := []byte("0123456789abcdef0123456789abcdef")
	queue, err := NewQueue(store, key)
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	if err := queue.EnqueueVerification(context.Background(), " Person@Example.org ", token, expiresAt); err != nil {
		t.Fatal(err)
	}
	if store.recipient != "person@example.org" || len(store.ciphertext) == 0 || strings.Contains(string(store.ciphertext), token) {
		t.Fatal("outbox did not normalize recipient or encrypt token")
	}
	job := &repository.Job{ID: 17, IdempotencyKey: store.key, Recipient: store.recipient, Ciphertext: store.ciphertext, Nonce: store.nonce, ExpiresAt: expiresAt, Attempts: 1}
	store.job = job
	sender := &fakeSender{}
	worker := NewWorker(store, queue, sender, WorkerConfig{Lease: time.Minute, MaxAttempts: 8, PollInterval: time.Second, Retention: time.Hour}, slog.Default())
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed || !store.sent || sender.token != token || sender.recipient != store.recipient {
		t.Fatalf("worker delivery: processed %v sent %v recipient %q error %v", processed, store.sent, sender.recipient, err)
	}
}

func TestQueueRejectsInvalidTokenAndExpiredRequest(t *testing.T) {
	queue, err := NewQueue(&fakeOutbox{}, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.EnqueueVerification(context.Background(), "a@example.org", "bad-token", time.Now().Add(time.Minute)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("invalid token error = %v", err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	if err := queue.EnqueueVerification(context.Background(), "a@example.org", token, time.Now().Add(-time.Minute)); !errors.Is(err, ErrInvalidExpiry) {
		t.Fatalf("expired request error = %v", err)
	}
}

func TestWorkerRetriesSMTPFailure(t *testing.T) {
	store := &fakeOutbox{}
	queue, err := NewQueue(store, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	store.key = func() string {
		key := messageKey("a@example.org", token)
		return fmt.Sprintf("%x", key[:])
	}()
	store.job = &repository.Job{ID: 18, IdempotencyKey: store.key, Recipient: "a@example.org", Ciphertext: encryptForTest(t, queue, store.key, token), Nonce: testNonce(t, queue), ExpiresAt: time.Now().Add(time.Hour), Attempts: 1}
	worker := NewWorker(store, queue, &fakeSender{err: errors.New("SMTP offline")}, WorkerConfig{MaxAttempts: 8}, slog.Default())
	processed, err := worker.RunOnce(context.Background())
	if err != nil || !processed || !store.failed || store.sent {
		t.Fatalf("worker retry: processed %v failed %v sent %v error %v", processed, store.failed, store.sent, err)
	}
}

func testNonce(t *testing.T, queue *Queue) []byte {
	t.Helper()
	return make([]byte, queue.aead.NonceSize())
}

func encryptForTest(t *testing.T, queue *Queue, key, token string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce := testNonce(t, queue)
	return queue.aead.Seal(nil, nonce, []byte(token), decoded)
}
