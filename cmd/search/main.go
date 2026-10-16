package main

import (
	"context"
	"database/sql"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/search/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	p "github.com/HSE-Scientists-Team/matchlab-backend/internal/search"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/grpcapp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"log/slog"
	"os"
)

func main() {
	if err := grpcapp.Run("search", "matchlab.search.v1.SearchService", []string{"user", "project"}, setup); err != nil {
		slog.Error("search stopped", "error", err)
		os.Exit(1)
	}
}
func setup(ctx context.Context, db *sql.DB, c grpcapp.Config, server *grpc.Server, logger *slog.Logger) (func(), error) {
	user, err := grpc.NewClient(c.Peers["user"], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	project, err := grpc.NewClient(c.Peers["project"], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		user.Close()
		return nil, err
	}
	pb.RegisterSearchServiceServer(server, &p.Service{User: upb.NewProfileServiceClient(user), Project: ppb.NewProjectServiceClient(project)})
	return func() { user.Close(); project.Close() }, nil
}
