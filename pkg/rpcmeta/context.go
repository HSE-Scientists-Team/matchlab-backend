// Package rpcmeta forwards caller authorization, never internal service tokens.
package rpcmeta

import (
	"context"
	"google.golang.org/grpc/metadata"
)

func Forward(ctx context.Context) context.Context {
	return metadata.NewOutgoingContext(ctx, metadata.MD{"authorization": metadata.ValueFromIncomingContext(ctx, "authorization")})
}
