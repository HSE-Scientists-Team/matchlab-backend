package rpcmeta

import (
	"context"
	"google.golang.org/grpc/metadata"
	"testing"
)

func TestServiceCredentialsCannotBeForwardedByCaller(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer user", "x-matchlab-project-token", "injected-service-key"))
	md, _ := metadata.FromOutgoingContext(Forward(ctx))
	if len(md.Get("authorization")) != 1 || len(md.Get("x-matchlab-project-token")) != 0 {
		t.Fatal("credential boundary violated")
	}
}
