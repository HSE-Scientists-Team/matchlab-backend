package main

import (
	"context"
	"database/sql"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/admin"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/admin/v1"
	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/support/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/adminauth"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/grpcapp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"log/slog"
	"os"
)

func main() {
	key, err := adminauth.PrivateKey(os.Getenv("ADMIN_SIGNING_KEY"))
	if err != nil {
		slog.Error("invalid admin signing key")
		os.Exit(1)
	}
	var api *admin.Service
	setup := func(ctx context.Context, db *sql.DB, c grpcapp.Config, server *grpc.Server, logger *slog.Logger) (func(), error) {
		users, e := grpc.NewClient(c.Peers["user"], grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e != nil {
			return nil, e
		}
		support, e := grpc.NewClient(c.Peers["support"], grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e != nil {
			users.Close()
			return nil, e
		}
		projects, e := grpc.NewClient(c.Peers["project"], grpc.WithTransportCredentials(insecure.NewCredentials()))
		if e != nil {
			users.Close()
			support.Close()
			return nil, e
		}
		api = &admin.Service{DB: db, SigningKey: key, Users: upb.NewUserManagementServiceClient(users), Profiles: upb.NewProfileServiceClient(users), Support: spb.NewSupportManagementServiceClient(support), Projects: ppb.NewProjectServiceClient(projects)}
		pb.RegisterAdminServiceServer(server, api)
		worker, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); api.Cleanup(worker) }()
		return func() { cancel(); <-done; users.Close(); support.Close(); projects.Close() }, nil
	}
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		return api.UnaryInterceptor(ctx, req, info, next)
	}
	if err = grpcapp.RunWithInterceptors("admin", "matchlab.admin.v1.AdminService", []string{"user", "support", "project"}, setup, interceptor); err != nil {
		slog.Error("admin stopped", "error", err)
		os.Exit(1)
	}
}
