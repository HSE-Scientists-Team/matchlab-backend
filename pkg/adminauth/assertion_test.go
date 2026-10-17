package adminauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
	"time"
)

func TestAssertionBoundaries(t *testing.T) {
	public, key, _ := ed25519.GenerateKey(rand.Reader)
	encoded := base64.StdEncoding.EncodeToString(public)
	out, e := Sign(context.Background(), key, "00000000-0000-4000-8000-000000000001", "moderator", "user", "staff@hse.ru")
	if e != nil {
		t.Fatal(e)
	}
	md, _ := metadata.FromOutgoingContext(out)
	ctx := metadata.NewIncomingContext(context.Background(), md)
	actor, e := VerifyKey(ctx, "user", encoded, time.Now())
	if e != nil || actor.Email != "staff@hse.ru" {
		t.Fatal(actor, e)
	}
	for _, tc := range []struct {
		name, audience, key string
		ctx                 context.Context
		now                 time.Time
	}{
		{"wrong audience", "project", encoded, ctx, time.Now()},
		{"expired", "user", encoded, ctx, time.Now().Add(time.Minute)},
		{"future", "user", encoded, ctx, time.Now().Add(-time.Minute)},
		{"wrong key", "user", base64.StdEncoding.EncodeToString(make([]byte, 32)), ctx, time.Now()},
		{"ambiguous", "user", encoded, metadata.NewIncomingContext(context.Background(), metadata.Pairs(Header, md.Get(Header)[0], Header, md.Get(Header)[0])), time.Now()},
		{"ordinary user token", "user", encoded, metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user-session")), time.Now()},
		{"modified signature", "user", encoded, metadata.NewIncomingContext(context.Background(), metadata.Pairs(Header, strings.Split(md.Get(Header)[0], ".")[0]+".invalid")), time.Now()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := VerifyKey(tc.ctx, tc.audience, tc.key, tc.now); status.Code(e) != codes.PermissionDenied {
				t.Fatal("accepted invalid assertion", e)
			}
		})
	}
	if e = RequireAdministrator(actor); status.Code(e) != codes.PermissionDenied {
		t.Fatal("moderator escalation", e)
	}
}
func TestInvalidPrivateKey(t *testing.T) {
	for _, v := range []string{"", "secret", base64.StdEncoding.EncodeToString(make([]byte, 31))} {
		if _, e := PrivateKey(v); e == nil {
			t.Fatal("bad key accepted")
		}
	}
}
