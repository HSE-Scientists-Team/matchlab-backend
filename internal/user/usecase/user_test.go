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
	account                                  repository.Account
	email, passwordHash, tokenHash, markedID string
	expiresAt, confirmedAt                   time.Time
	saveErr, findErr, confirmErr             error
	findCalls                                int
}

func (f *fakeUsers) SaveRegistration(_ context.Context, email, passwordHash, tokenHash string, expiresAt time.Time) error {
	f.email, f.passwordHash, f.tokenHash, f.expiresAt = email, passwordHash, tokenHash, expiresAt
	return f.saveErr
}
func (f *fakeUsers) FindByEmail(_ context.Context, email string) (repository.Account, error) {
	f.findCalls++
	f.email = email
	return f.account, f.findErr
}
func (f *fakeUsers) MarkLogin(_ context.Context, id string) error { f.markedID = id; return nil }
func (f *fakeUsers) ConfirmEmail(_ context.Context, tokenHash string, now time.Time) error {
	f.tokenHash, f.confirmedAt = tokenHash, now
	return f.confirmErr
}
func (f *fakeUsers) GetEmailStatus(context.Context, string) (repository.EmailStatus, error) {
	return repository.EmailStatus{}, nil
}

type fakeSessions struct {
	userID, token string
	err           error
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

func TestRegisterSavesHashedCredentialsAndSendsConfirmation(t *testing.T) {
	users, sender, sessions := &fakeUsers{}, &fakeEmailSender{}, &fakeSessions{}
	service := NewService(users, sessions, sender)
	service.now = func() time.Time { return time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC) }
	if err := service.Register(context.Background(), " Student@HSE.RU ", "strong-password"); err != nil {
		t.Fatal(err)
	}
	if users.email != "student@hse.ru" || sender.address != users.email || sender.token == "" {
		t.Fatal("email was not normalized or sent")
	}
	if bcrypt.CompareHashAndPassword([]byte(users.passwordHash), []byte("strong-password")) != nil {
		t.Fatal("wrong password hash")
	}
	hash := sha256.Sum256([]byte(sender.token))
	if users.tokenHash != hex.EncodeToString(hash[:]) {
		t.Fatal("token must be stored only as SHA-256 hash")
	}
	if users.expiresAt != service.now().Add(EmailVerificationTTL) {
		t.Fatal("wrong token expiry")
	}
	if sessions.userID != "" || users.markedID != "" || users.findCalls != 0 {
		t.Fatal("registration must not log in or require existing credentials")
	}
	if err := service.ConfirmEmail(context.Background(), sender.token); err != nil {
		t.Fatal(err)
	}
	if users.tokenHash != hex.EncodeToString(hash[:]) || users.confirmedAt != service.now() {
		t.Fatal("confirmation used wrong token or time")
	}
}

func TestReregisterUsesNewPasswordAndToken(t *testing.T) {
	users, sender := &fakeUsers{}, &fakeEmailSender{}
	service := NewService(users, &fakeSessions{}, sender)
	if err := service.Register(context.Background(), "student@hse.ru", "stranger-password"); err != nil {
		t.Fatal(err)
	}
	oldToken := sender.token
	if err := service.Register(context.Background(), " STUDENT@HSE.RU ", "owner-password"); err != nil {
		t.Fatal(err)
	}
	if sender.token == oldToken || users.findCalls != 0 {
		t.Fatal("registration required the old password or reused token")
	}
	if bcrypt.CompareHashAndPassword([]byte(users.passwordHash), []byte("owner-password")) != nil || bcrypt.CompareHashAndPassword([]byte(users.passwordHash), []byte("stranger-password")) == nil {
		t.Fatal("new request must contain only owner's password hash")
	}
}

func TestRegisterRejectsInvalidInputs(t *testing.T) {
	users, sender := &fakeUsers{}, &fakeEmailSender{}
	service := NewService(users, &fakeSessions{}, sender)
	for _, email := range []string{"", "плохой адрес", "a@", "Name <student@hse.ru>"} {
		if err := service.Register(context.Background(), email, "strong-password"); !errors.Is(err, ErrInvalidEmail) {
			t.Errorf("%q: %v", email, err)
		}
	}
	for _, password := range []string{"short", string(make([]byte, 73))} {
		if err := service.Register(context.Background(), "student@hse.ru", password); !errors.Is(err, ErrInvalidPassword) {
			t.Errorf("invalid password: %v", err)
		}
	}
	if users.tokenHash != "" || sender.token != "" {
		t.Fatal("invalid registration was persisted or sent")
	}
}

func TestRegisterFailuresDoNotSendConfirmation(t *testing.T) {
	for _, failure := range []error{repository.ErrEmailTaken, repository.ErrOrganizationUnavailable, errors.New("db unavailable")} {
		users, sender := &fakeUsers{saveErr: failure}, &fakeEmailSender{}
		if err := NewService(users, &fakeSessions{}, sender).Register(context.Background(), "student@hse.ru", "strong-password"); !errors.Is(err, failure) || sender.token != "" {
			t.Fatalf("error %v, email sent %t", err, sender.token != "")
		}
	}
}

func TestSMTPFailureAllowsNewRegistration(t *testing.T) {
	users, sender := &fakeUsers{}, &fakeEmailSender{err: errors.New("smtp unavailable")}
	service := NewService(users, &fakeSessions{}, sender)
	if err := service.Register(context.Background(), "student@hse.ru", "first-password"); !errors.Is(err, ErrEmailDelivery) {
		t.Fatal(err)
	}
	oldHash := users.tokenHash
	sender.err = nil
	if err := service.Register(context.Background(), "student@hse.ru", "owner-password"); err != nil {
		t.Fatal(err)
	}
	if users.tokenHash == oldHash || sender.token == "" {
		t.Fatal("retry did not replace the failed request")
	}
}

func TestLoginRequiresAnActiveConfirmedAccount(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("strong-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, password, status string
		verified               bool
		findErr, want          error
	}{
		{"pending request", "strong-password", "", false, repository.ErrNotFound, ErrInvalidCredentials},
		{"wrong password", "wrong-password", "active", true, nil, ErrInvalidCredentials},
		{"blocked", "strong-password", "blocked", true, nil, ErrAccountInactive},
		{"unverified account", "strong-password", "active", false, nil, ErrEmailUnverified},
		{"confirmed", "strong-password", "active", true, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := &fakeUsers{account: repository.Account{ID: "user-id", PasswordHash: string(hash), Status: tc.status, Verified: tc.verified}, findErr: tc.findErr}
			sessions := &fakeSessions{token: "session-token"}
			id, token, err := NewService(users, sessions, nil).Login(context.Background(), " Student@HSE.RU ", tc.password)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error %v, want %v", err, tc.want)
			}
			if tc.want != nil {
				if id != "" || token != "" || sessions.userID != "" || users.markedID != "" {
					t.Fatal("failed login created a session")
				}
			} else if id != "user-id" || token != sessions.token || users.markedID != id || sessions.userID != id || users.email != "student@hse.ru" {
				t.Fatal("confirmed login failed")
			}
		})
	}
}

func TestConfirmEmailRejectsMalformedTokens(t *testing.T) {
	users := &fakeUsers{}
	service := NewService(users, &fakeSessions{}, nil)
	for _, token := range []string{"", "bad-token", "!invalid!"} {
		if err := service.ConfirmEmail(context.Background(), token); !errors.Is(err, ErrInvalidVerification) {
			t.Fatal(err)
		}
	}
	if users.tokenHash != "" {
		t.Fatal("malformed token reached repository")
	}
}
