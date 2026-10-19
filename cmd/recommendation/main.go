package main

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"time"

	ppb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/project/v1"
	pb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/recommendation/v1"
	spb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/search/v1"
	upb "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/user/v1"
	p "github.com/HSE-Scientists-Team/matchlab-backend/internal/recommendation"
	"github.com/HSE-Scientists-Team/matchlab-backend/pkg/grpcapp"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	if err := grpcapp.Run("recommendation", "matchlab.recommendation.v1.RecommendationService", []string{"user", "search", "project"}, setup); err != nil {
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
	project, err := grpc.NewClient(c.Peers["project"], grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		user.Close()
		search.Close()
		return nil, err
	}
	profile := upb.NewProfileServiceClient(user)
	service := &p.Service{User: profile, Profiles: profile, ProjectClient: ppb.NewProjectServiceClient(project), Search: spb.NewSearchServiceClient(search)}
	var cacheClient *redis.Client
	if address := c.Peers["recommendation_redis"]; address != "" {
		host, port, addressError := net.SplitHostPort(address)
		portNumber, portError := strconv.Atoi(port)
		if addressError != nil || host == "" || portError != nil || portNumber < 1 || portNumber > 65535 || os.Getenv("RECOMMENDATION_REDIS_PASSWORD") == "" {
			user.Close()
			search.Close()
			project.Close()
			return nil, fmt.Errorf("valid recommendation_redis address and RECOMMENDATION_REDIS_PASSWORD required")
		}
		cacheClient = redis.NewClient(&redis.Options{Addr: address, Password: os.Getenv("RECOMMENDATION_REDIS_PASSWORD"),
			DialTimeout: 300 * time.Millisecond, ReadTimeout: 300 * time.Millisecond, WriteTimeout: 300 * time.Millisecond,
			MaxRetries: -1, ContextTimeoutEnabled: true})
		service.Cache = &p.RedisCache{Client: cacheClient}
	}
	pb.RegisterRecommendationServiceServer(server, service)
	return func() {
		user.Close()
		search.Close()
		project.Close()
		if cacheClient != nil {
			cacheClient.Close()
		}
	}, nil
}
