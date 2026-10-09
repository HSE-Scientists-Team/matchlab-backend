//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mediav1 "github.com/HSE-Scientists-Team/matchlab-backend/internal/gen/media/v1"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/migrator/migrations"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/docker/go-connections/nat"
	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

// Проверяет Dockerfile, main.go и весь путь gRPC → usecase → PostgreSQL/S3.
func TestMediaContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	testNetwork, err := network.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = testNetwork.Remove(context.Background()) })
	start := func(request testcontainers.ContainerRequest) testcontainers.Container {
		t.Helper()
		container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: request, Started: true})
		if container != nil {
			t.Cleanup(func() { _ = container.Terminate(context.Background()) })
		}
		if err != nil {
			t.Fatal(err)
		}
		return container
	}
	endpoint := func(container testcontainers.Container, port string) string {
		t.Helper()
		host, err := container.Host(ctx)
		if err != nil {
			t.Fatal(err)
		}
		mapped, err := container.MappedPort(ctx, nat.Port(port))
		if err != nil {
			t.Fatal(err)
		}
		return net.JoinHostPort(host, mapped.Port())
	}
	postgres := start(testcontainers.ContainerRequest{
		Image:        "postgres:17-alpine",
		Env:          map[string]string{"POSTGRES_DB": "matchlab", "POSTGRES_USER": "matchlab", "POSTGRES_PASSWORD": "integration-only"},
		ExposedPorts: []string{"5432/tcp"}, Networks: []string{testNetwork.Name},
		NetworkAliases: map[string][]string{testNetwork.Name: {"postgres"}},
		WaitingFor: wait.ForSQL("5432/tcp", "pgx", func(host string, port nat.Port) string {
			return fmt.Sprintf("postgres://matchlab:integration-only@%s/matchlab?sslmode=disable", net.JoinHostPort(host, port.Port()))
		}).WithStartupTimeout(90 * time.Second),
	})
	db, err := sql.Open("pgx", fmt.Sprintf("postgres://matchlab:integration-only@%s/matchlab?sslmode=disable", endpoint(postgres, "5432/tcp")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	// Тест готовит схему до запуска Media, сам сервис её не изменяет.
	if _, err := migrations.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	seaweed := start(testcontainers.ContainerRequest{
		Image:        "chrislusf/seaweedfs:4.47",
		Cmd:          []string{"mini", "-dir=/data", "-s3.config=/etc/seaweedfs/s3.json"},
		Env:          map[string]string{"S3_ACCESS_KEY": "test-admin", "S3_SECRET_KEY": "test-admin-secret", "S3_MEDIA_ACCESS_KEY": "test-media", "S3_MEDIA_SECRET_KEY": "test-media-secret"},
		Files:        []testcontainers.ContainerFile{{HostFilePath: filepath.Join(root, "compose", "seaweedfs", "s3.json"), ContainerFilePath: "/etc/seaweedfs/s3.json", FileMode: 0644}},
		ExposedPorts: []string{"8333/tcp", "9333/tcp"}, Networks: []string{testNetwork.Name},
		NetworkAliases: map[string][]string{testNetwork.Name: {"seaweedfs"}},
		WaitingFor:     wait.ForHTTP("/cluster/status").WithPort("9333/tcp").WithStartupTimeout(90 * time.Second),
	})
	s3Endpoint := "http://" + endpoint(seaweed, "8333/tcp")
	admin := awss3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test-admin", "test-admin-secret", ""), HTTPClient: &http.Client{Timeout: 5 * time.Second}}, func(o *awss3.Options) {
		o.BaseEndpoint = aws.String(s3Endpoint)
		o.UsePathStyle = true
	})
	var bucketErr error
	for attempt := 0; attempt < 30; attempt++ {
		_, bucketErr = admin.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String("matchlab-media")})
		if bucketErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	if bucketErr != nil {
		t.Fatal(bucketErr)
	}
	mediaConfig := fmt.Sprintf(`grpc:
  host: 0.0.0.0
  port: 8085
postgres:
  host: postgres
  port: 5432
  database: matchlab
  user: matchlab
  ssl_mode: disable
s3:
  endpoint: http://seaweedfs:8333
  public_endpoint: %s
  region: us-east-1
  bucket: matchlab-media
  use_path_style: true
  request_timeout: 5s
upload:
  url_ttl: 10m
  max_size_bytes: 10485760
  allowed_content_types: [application/pdf]
download:
  url_ttl: 5m
`, s3Endpoint)
	var buildLog bytes.Buffer
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Docker build log:\n%s", buildLog.String())
		}
	})
	media := start(testcontainers.ContainerRequest{
		FromDockerfile: testcontainers.FromDockerfile{Context: root, Dockerfile: "cmd/media/Dockerfile", BuildLogWriter: &buildLog},
		Cmd:            []string{"-config", "/etc/app/config.yaml"},
		Env:            map[string]string{"POSTGRES_PASSWORD": "integration-only", "S3_MEDIA_ACCESS_KEY": "test-media", "S3_MEDIA_SECRET_KEY": "test-media-secret"},
		Files:          []testcontainers.ContainerFile{{Reader: strings.NewReader(mediaConfig), ContainerFilePath: "/etc/app/config.yaml", FileMode: 0644}},
		ExposedPorts:   []string{"8085/tcp"}, Networks: []string{testNetwork.Name},
		WaitingFor: wait.ForListeningPort("8085/tcp").SkipInternalCheck().WithStartupTimeout(60 * time.Second),
	})
	conn, err := grpc.NewClient(endpoint(media, "8085/tcp"), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := mediav1.NewMediaServiceClient(conn)
	health, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{Service: mediav1.MediaService_ServiceDesc.ServiceName})
	if err != nil || health.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Fatalf("health: %v, %v", health, err)
	}
	for _, public := range []bool{false, true} {
		name := "private file"
		if public {
			name = "public file"
		}
		t.Run(name, func(t *testing.T) {
			owner := uuid.NewString()
			stranger := uuid.NewString()
			created, err := client.CreateUpload(ctx, &mediav1.CreateUploadRequest{UserId: owner, OriginalName: "report.pdf", ContentType: "application/pdf", SizeBytes: 5, IsPublic: public})
			if err != nil {
				t.Fatal(err)
			}
			id := created.GetFile().GetId()
			if id == "" || created.GetFile().GetIsPublic() != public || created.GetFile().GetStatus() != mediav1.FileStatus_FILE_STATUS_PENDING || created.GetFile().SizeBytes != nil {
				t.Fatal("incorrect pending response")
			}
			metadata, err := client.GetFile(ctx, &mediav1.GetFileRequest{UserId: owner, FileId: id})
			if err != nil || metadata.GetFile().GetOriginalName() != "report.pdf" {
				t.Fatalf("pending metadata: %v", err)
			}
			if _, err := client.CompleteUpload(ctx, &mediav1.CompleteUploadRequest{UserId: owner, FileId: id}); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("confirm missing object: %v", err)
			}
			if _, err := client.CreateDownloadURL(ctx, &mediav1.CreateDownloadURLRequest{UserId: owner, FileId: id}); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("pending download: %v", err)
			}
			for _, reader := range []string{stranger, ""} {
				metadata, err := client.GetFile(ctx, &mediav1.GetFileRequest{UserId: reader, FileId: id})
				if public {
					if err != nil || !metadata.GetFile().GetIsPublic() {
						t.Fatalf("public metadata as %q: %v", reader, err)
					}
				} else if status.Code(err) != codes.NotFound {
					t.Fatalf("private metadata as %q: %v", reader, err)
				}
				want := codes.NotFound
				if reader == "" {
					want = codes.InvalidArgument
				}
				if _, err := client.CompleteUpload(ctx, &mediav1.CompleteUploadRequest{UserId: reader, FileId: id}); status.Code(err) != want {
					t.Fatalf("non-owner confirmation as %q: %v", reader, err)
				}
			}
			request, err := http.NewRequestWithContext(ctx, created.GetMethod(), created.GetUploadUrl(), strings.NewReader("hello"))
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range created.GetHeaders() {
				request.Header.Set(name, value)
			}
			httpClient := &http.Client{Timeout: 10 * time.Second}
			resp, err := httpClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("PUT: %d", resp.StatusCode)
			}
			completed, err := client.CompleteUpload(ctx, &mediav1.CompleteUploadRequest{UserId: owner, FileId: id})
			if err != nil || completed.GetFile().GetIsPublic() != public || completed.GetFile().GetStatus() != mediav1.FileStatus_FILE_STATUS_READY || completed.GetFile().GetSizeBytes() != 5 || completed.GetFile().UploadedAtUnix == nil {
				t.Fatalf("complete: %v, %v", completed, err)
			}
			repeated, err := client.CompleteUpload(ctx, &mediav1.CompleteUploadRequest{UserId: owner, FileId: id})
			if err != nil || repeated.GetFile().GetUploadedAtUnix() != completed.GetFile().GetUploadedAtUnix() {
				t.Fatalf("repeat: %v", err)
			}
			for _, reader := range []string{stranger, ""} {
				url, err := client.CreateDownloadURL(ctx, &mediav1.CreateDownloadURLRequest{UserId: reader, FileId: id})
				if public {
					if err != nil || url.GetDownloadUrl() == "" {
						t.Fatalf("public download as %q: %v", reader, err)
					}
				} else if status.Code(err) != codes.NotFound {
					t.Fatalf("private download as %q: %v", reader, err)
				}
			}
			reader := owner
			if public {
				reader = ""
			}
			download, err := client.CreateDownloadURL(ctx, &mediav1.CreateDownloadURLRequest{UserId: reader, FileId: id})
			if err != nil {
				t.Fatal(err)
			}
			request, err = http.NewRequestWithContext(ctx, "GET", download.GetDownloadUrl(), nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err = httpClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || resp.StatusCode != http.StatusOK || string(body) != "hello" {
				t.Fatalf("GET: status=%d body=%q error=%v", resp.StatusCode, body, err)
			}
		})
	}
}
