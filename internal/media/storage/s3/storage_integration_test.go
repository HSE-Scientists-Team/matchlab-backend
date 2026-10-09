//go:build integration

package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/config"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestSeaweedFSUploadDownload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	configPath, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "compose", "seaweedfs", "s3.json"))
	if err != nil {
		t.Fatal(err)
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "chrislusf/seaweedfs:4.47",
			Cmd:          []string{"mini", "-dir=/data", "-s3.config=/etc/seaweedfs/s3.json"},
			Env:          map[string]string{"S3_ACCESS_KEY": "test-admin", "S3_SECRET_KEY": "test-admin-secret", "S3_MEDIA_ACCESS_KEY": "test-media", "S3_MEDIA_SECRET_KEY": "test-media-secret"},
			Files:        []testcontainers.ContainerFile{{HostFilePath: configPath, ContainerFilePath: "/etc/seaweedfs/s3.json", FileMode: 0644}},
			ExposedPorts: []string{"8333/tcp", "9333/tcp"},
			WaitingFor:   wait.ForHTTP("/cluster/status").WithPort("9333/tcp").WithStartupTimeout(90 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = container.Terminate(context.Background()) })
	host, err := container.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := container.MappedPort(ctx, "8333/tcp")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + net.JoinHostPort(host, port.Port())
	cfg := config.S3{Endpoint: endpoint, PublicEndpoint: endpoint, Region: "us-east-1", Bucket: "matchlab-media", UsePathStyle: true, RequestTimeout: 10 * time.Second, AccessKey: "test-admin", SecretKey: "test-admin-secret"}
	admin, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Готовность master не означает готовность S3 gateway.
	var createErr error
	for attempt := 0; attempt < 30; attempt++ {
		_, createErr = admin.client.CreateBucket(ctx, &awss3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)})
		if createErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	if createErr != nil {
		t.Fatal(createErr)
	}
	cfg.AccessKey, cfg.SecretKey = "test-media", "test-media-secret"
	storage, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.HeadObject(ctx, cfg.Bucket, "missing"); !errors.Is(err, domain.ErrObjectNotFound) {
		t.Fatalf("missing object: %v", err)
	}
	signed, err := storage.PresignUpload(ctx, domain.UploadInput{Bucket: cfg.Bucket, Key: "files/one", ContentType: "application/pdf", SizeBytes: 5, TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Timeout: 10 * time.Second}
	upload := func(body, contentType string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, signed.Method, signed.URL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range signed.Headers {
			req.Header.Set(name, value)
		}
		req.Header.Set("Content-Type", contentType)
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if code := upload("too-long", "application/pdf"); code != http.StatusForbidden {
		t.Fatalf("wrong length accepted: %d", code)
	}
	if code := upload("hello", "text/plain"); code != http.StatusForbidden {
		t.Fatalf("wrong Content-Type accepted: %d", code)
	}
	if code := upload("hello", "application/pdf"); code != http.StatusOK {
		t.Fatalf("upload status: %d", code)
	}
	if code := upload("other", "application/pdf"); code != http.StatusPreconditionFailed {
		t.Fatalf("overwrite accepted: %d", code)
	}
	object, err := storage.HeadObject(ctx, cfg.Bucket, "files/one")
	if err != nil || object.SizeBytes != 5 || object.ContentType != "application/pdf" || object.ETag == nil {
		t.Fatalf("HEAD: %+v, %v", object, err)
	}
	download, err := storage.PresignDownload(ctx, domain.DownloadInput{Bucket: cfg.Bucket, Key: "files/one", OriginalName: "report.pdf", TTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", download.URL, nil)
	resp, err := httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "hello" || !strings.Contains(resp.Header.Get("Content-Disposition"), "report.pdf") {
		t.Fatalf("download status=%d body=%q error=%v", resp.StatusCode, body, err)
	}
	request, _ = http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/%s/files/one", endpoint, cfg.Bucket), nil)
	resp, err = httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous access: %d", resp.StatusCode)
	}
}
