package main

import (
	"context"
	"database/sql"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/recommendation/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/search/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	p "github.com/HSE-Scientists-Team/matchlab-backend/internal/recommendation"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/grpcapp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"log/slog"
	"os"
)

func main() {
	if err := grpcapp.Run("recommendation", "matchlab.recommendation.v1.RecommendationService", []string{"user", "search"}, setup); err != nil {
		slog.Error("recommendation stopped", "error", err)
		os.Exit(1)
	}
}
func setup(ctx context.Context, db *sql.DB, c grpcapp.Config, server *grpc.Server, logger *slog.Logger) (func(), error) {
	user, err := grpc.NewClient(c.Peers["user"], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	search, err := grpc.NewClient(c.Peers["search"], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		user.Close()
		return nil, err
	}
	pb.RegisterRecommendationServiceServer(server, &p.Service{User: upb.NewProfileServiceClient(user), Search: spb.NewSearchServiceClient(search)})
	return func() { user.Close(); search.Close() }, nil
}
