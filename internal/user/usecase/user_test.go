package usecase

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/user/repository"
	"golang.org/x/crypto/bcrypt"
)

type fakeUsers struct {
	account                      repository.Account
	login, hash, markedID        string
	email, tokenHash             string
	expiresAt, confirmedAt       time.Time
	createErr, findErr, emailErr error
	status                       repository.EmailStatus
}

func (f *fakeUsers) Create(_ context.Context, login, hash string) (string, error) {
	f.login, f.hash = login, hash
	if f.createErr != nil {
		return "", f.createErr
	}
	return "4f9a4c95-6144-4ec8-89e8-3866207d7561", nil
}
func (f *fakeUsers) FindByLogin(_ context.Context, login string) (repository.Account, error) {
	f.login = login
	if f.findErr != nil {
		return repository.Account{}, f.findErr
	}
	return f.account, nil
}
func (f *fakeUsers) MarkLogin(_ context.Context, id string) error { f.markedID = id; return nil }
func (f *fakeUsers) SaveEmailVerification(_ context.Context, userID, email, tokenHash string, expiresAt time.Time) error {
	f.email, f.tokenHash, f.expiresAt = email, tokenHash, expiresAt
	return f.emailErr
}
func (f *fakeUsers) ConfirmEmail(_ context.Context, tokenHash string, now time.Time) error {
	f.tokenHash, f.confirmedAt = tokenHash, now
	return f.emailErr
}
func (f *fakeUsers) GetEmailStatus(context.Context, string) (repository.EmailStatus, error) {
	return f.status, nil
}

type fakeSessions struct {
	userID string
	token  string
	err    error
}

func (f *fakeSessions) Create(_ context.Context, id string) (string, error) {
	f.userID = id
	return f.token, f.err
}

type fakeEmailSender struct {
	address, token string
	err            error
}

func (f *fakeEmailSender) SendVerification(_ context.Context, address, token string, _ time.Time) error {
	f.address, f.token = address, token
	return f.err
}

func TestRegisterNormalizesLoginAndStoresPasswordHash(t *testing.T) {
	users := &fakeUsers{}
	service := NewService(users, &fakeSessions{}, nil)
	id, err := service.Register(context.Background(), "  Student_Name-01.X ", "strong-password")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || users.login != "student_name-01.x" {
		t.Fatalf("unexpected account: id %q login %q", id, users.login)
	}
	if users.hash == "strong-password" || bcrypt.CompareHashAndPassword([]byte(users.hash), []byte("strong-password")) != nil {
		t.Fatal("password was not stored as a valid hash")
	}
}

func TestRegisterRejectsInvalidInputs(t *testing.T) {
	service := NewService(&fakeUsers{}, &fakeSessions{}, nil)
	for _, login := range []string{"", "bad login", "жук", "a@b", "123456789012345678901234567890123"} {
		if _, err := service.Register(context.Background(), login, "strong-password"); !errors.Is(err, ErrInvalidLogin) {
			t.Errorf("login %q error = %v", login, err)
		}
	}
	if _, err := service.Register(context.Background(), "okay.login", "short"); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("password error = %v", err)
	}
}

func TestLoginCreatesSessionForCanonicalLogin(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("strong-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	id := "4f9a4c95-6144-4ec8-89e8-3866207d7561"
	users := &fakeUsers{account: repository.Account{ID: id, PasswordHash: string(hash), Status: "active"}}
	sessions := &fakeSessions{token: "new-session-token"}
	service := NewService(users, sessions, nil)
	gotID, token, err := service.Login(context.Background(), "Student.Name", "strong-password")
	if err != nil || gotID != id || token != sessions.token || sessions.userID != id || users.markedID != id || users.login != "student.name" {
		t.Fatalf("login: id %q token %q error %v", gotID, token, err)
	}
	if _, _, err := service.Login(context.Background(), "student.name", "wrong-password"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("wrong password error = %v", err)
	}
	users.account.Status = "blocked"
	if _, _, err := service.Login(context.Background(), "student.name", "strong-password"); !errors.Is(err, ErrAccountInactive) {
		t.Fatalf("blocked user error = %v", err)
	}
}

func TestRequestAndConfirmEmailStoreOnlyTokenHash(t *testing.T) {
	users, sender := &fakeUsers{}, &fakeEmailSender{}
	service := NewService(users, &fakeSessions{}, sender)
	if err := service.RequestEmailVerification(context.Background(), "user-id", " Student@Example.ORG "); err != nil {
		t.Fatal(err)
	}
	if users.email != "student@example.org" || sender.address != users.email || sender.token == "" {
		t.Fatalf("email request was not normalized/sent: repository=%q sender=%q token=%q", users.email, sender.address, sender.token)
	}
	hash := sha256.Sum256([]byte(sender.token))
	if users.tokenHash != hex.EncodeToString(hash[:]) || users.tokenHash == sender.token {
		t.Fatal("verification token was not stored as a hash")
	}
	if err := service.ConfirmEmail(context.Background(), sender.token); err != nil {
		t.Fatal(err)
	}
	if users.tokenHash != hex.EncodeToString(hash[:]) {
		t.Fatal("confirmation looked up a different token hash")
	}
}
