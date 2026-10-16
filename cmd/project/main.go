package main

import (
	"context"
	"database/sql"
	"fmt"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	p "github.com/HSE-Scientists-Team/matchlab-backend/internal/project"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/grpcapp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"log/slog"
	"os"
)

func main() {
	if len(os.Getenv("PROJECT_SYNC_TOKEN")) < 32 {
		slog.Error("PROJECT_SYNC_TOKEN must contain at least 32 bytes")
		os.Exit(1)
	}
	if err := grpcapp.Run("project", "matchlab.project.v1.ProjectService", []string{"user"}, setup); err != nil {
		slog.Error("project stopped", "error", err)
		os.Exit(1)
	}
}
func setup(ctx context.Context, db *sql.DB, c grpcapp.Config, server *grpc.Server, logger *slog.Logger) (func(), error) {
	token := os.Getenv("PROJECT_SYNC_TOKEN")
	if len(token) < 32 {
		return nil, fmt.Errorf("PROJECT_SYNC_TOKEN must contain at least 32 bytes")
	}
	user, err := grpc.NewClient(c.Peers["user"], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	service := &p.Service{Repo: &p.Repository{DB: db}, Users: upb.NewProfileServiceClient(user), SyncToken: token}
	pb.RegisterProjectServiceServer(server, service)
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); service.RunOutbox(workerCtx, logger) }()
	return func() { cancel(); <-done; user.Close() }, nil
}
