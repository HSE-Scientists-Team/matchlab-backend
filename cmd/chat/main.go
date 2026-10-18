package main

import (
	"context"
	"database/sql"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/chat"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/chat/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/grpcapp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"log/slog"
	"os"
)

func main() {
	if err := grpcapp.Run("chat", "matchlab.chat.v1.ChatService", []string{"user"}, setup); err != nil {
		slog.Error("chat stopped", "error", err)
		os.Exit(1)
	}
}
func setup(_ context.Context, db *sql.DB, c grpcapp.Config, server *grpc.Server, _ *slog.Logger) (func(), error) {
	conn, err := grpc.NewClient(c.Peers["user"], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	pb.RegisterChatServiceServer(server, &chat.Service{Repo: &chat.Repository{DB: db}, Users: upb.NewProfileServiceClient(conn)})
	return func() { _ = conn.Close() }, nil
}
