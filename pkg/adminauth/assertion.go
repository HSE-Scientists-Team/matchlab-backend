// Package adminauth verifies short-lived, audience-bound Admin service assertions.
// Only Admin owns the Ed25519 private key. Data owners receive its public key.
package adminauth

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const Header = "x-matchlab-admin-assertion"

type Actor struct {
	Email    string `json:"email,omitempty"`
	ID       string `json:"id"`
	Role     string `json:"role"`
	Audience string `json:"aud"`
	Expires  int64  `json:"exp"`
	Issued   int64  `json:"iat"`
}

func PrivateKey(value string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, status.Error(codes.FailedPrecondition, "ADMIN_SIGNING_KEY must be a base64 Ed25519 seed (32 bytes)")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
func Sign(ctx context.Context, key ed25519.PrivateKey, id, role, audience string, email ...string) (context.Context, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, status.Error(codes.FailedPrecondition, "admin signing key unavailable")
	}
	now := time.Now().Unix()
	claims := Actor{ID: id, Role: role, Audience: audience, Issued: now, Expires: now + 30}
	if len(email) > 0 {
		claims.Email = email[0]
	}
	raw, _ := json.Marshal(claims)
	token := base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, raw))
	// Drop user credentials and incoming privileged metadata. Never forward arbitrary client headers.
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(Header, token)), nil
}
func Verify(ctx context.Context, audience string) (Actor, error) {
	return VerifyKey(ctx, audience, os.Getenv("ADMIN_PUBLIC_KEY"), time.Now())
}
func VerifyKey(ctx context.Context, audience, public string, now time.Time) (Actor, error) {
	denied := status.Error(codes.PermissionDenied, "требуется доверенный сервис Admin")
	values := metadata.ValueFromIncomingContext(ctx, Header)
	if len(values) == 0 && len(metadata.ValueFromIncomingContext(ctx, "authorization")) == 0 {
		return Actor{}, status.Error(codes.Unauthenticated, "требуется служебная авторизация")
	}
	key, err := base64.StdEncoding.DecodeString(public)
	if err != nil || len(key) != ed25519.PublicKeySize || len(values) != 1 || len(values[0]) > 2048 {
		return Actor{}, denied
	}
	parts := strings.Split(values[0], ".")
	if len(parts) != 2 {
		return Actor{}, denied
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Actor{}, denied
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !ed25519.Verify(key, raw, sig) {
		return Actor{}, denied
	}
	var a Actor
	if json.Unmarshal(raw, &a) != nil || a.Audience != audience || a.Expires <= now.Unix() || a.Issued > now.Unix()+5 || a.Expires-a.Issued > 30 || a.Expires <= a.Issued {
		return Actor{}, denied
	}
	id, e := uuid.Parse(a.ID)
	if e != nil || id == uuid.Nil || id.String() != a.ID || (a.Role != "administrator" && a.Role != "moderator") {
		return Actor{}, denied
	}
	if a.Email != "" {
		parsed, e := mail.ParseAddress(a.Email)
		if e != nil || parsed.Address != a.Email || a.Email != strings.ToLower(a.Email) {
			return Actor{}, denied
		}
	}
	return a, nil
}
func RequireAdministrator(a Actor) error {
	if a.Role != "administrator" {
		return status.Error(codes.PermissionDenied, "требуется администратор")
	}
	return nil
}
