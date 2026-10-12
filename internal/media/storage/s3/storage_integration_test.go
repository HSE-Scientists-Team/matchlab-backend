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
	t.Run("download ranges using the same signed URL", func(t *testing.T) {
		var assembled strings.Builder
		for _, test := range []struct {
			name         string
			rangeHeader  string
			contentRange string
			body         string
			assemble     bool
		}{
			{"first chunk", "bytes=0-1", "bytes 0-1/5", "he", true},
			{"middle chunk", "bytes=2-3", "bytes 2-3/5", "ll", true},
			{"last chunk clipped to file size", "bytes=4-5", "bytes 4-4/5", "o", true},
			{"resume from offset", "bytes=2-", "bytes 2-4/5", "llo", false},
			{"suffix", "bytes=-2", "bytes 3-4/5", "lo", false},
		} {
			t.Run(test.name, func(t *testing.T) {
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Range", test.rangeHeader)
				resp, err := httpClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusPartialContent {
					t.Fatalf("status=%d, want %d", resp.StatusCode, http.StatusPartialContent)
				}
				if got := resp.Header.Get("Content-Range"); got != test.contentRange {
					t.Errorf("Content-Range=%q, want %q", got, test.contentRange)
				}
				if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
					t.Errorf("Accept-Ranges=%q, want bytes", got)
				}
				if resp.ContentLength != int64(len(test.body)) || string(body) != test.body {
					t.Fatalf("Content-Length=%d body=%q, want length=%d body=%q", resp.ContentLength, body, len(test.body), test.body)
				}
				if got := resp.Header.Get("ETag"); got != *object.ETag {
					t.Errorf("ETag=%q, want %q", got, *object.ETag)
				}
				if test.assemble {
					assembled.Write(body)
				}
			})
		}
		if got := assembled.String(); got != "hello" {
			t.Errorf("assembled file=%q, want hello", got)
		}
		t.Run("range beyond file size", func(t *testing.T) {
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Range", "bytes=5-")
			resp, err := httpClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("status=%d, want %d", resp.StatusCode, http.StatusRequestedRangeNotSatisfiable)
			}
		})
	})
	request, _ = http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/%s/files/one", endpoint, cfg.Bucket), nil)
	resp, err = httpClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anonymous access: %d", resp.StatusCode)
	}
	t.Run("multipart upload resume complete and abort", func(t *testing.T) {
		key := "files/multipart"
		id, err := storage.StartMultipart(ctx, cfg.Bucket, key, "application/pdf")
		if err != nil {
			t.Fatal(err)
		}
		defer storage.AbortMultipart(context.Background(), cfg.Bucket, key, id)
		putPart := func(number int32, body string) domain.Part {
			t.Helper()
			signed, err := storage.PresignPart(ctx, cfg.Bucket, key, id, number, int64(len(body)), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, signed.URL, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			for name, value := range signed.Headers {
				req.Header.Set(name, value)
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != http.StatusOK || resp.Header.Get("ETag") == "" {
				t.Fatalf("part=%d status=%d", number, resp.StatusCode)
			}
			return domain.Part{Number: number, ETag: resp.Header.Get("ETag"), SizeBytes: int64(len(body))}
		}
		// Out-of-order upload and replacing a part model resume/retry.
		putPart(2, "old")
		firstBody := strings.Repeat("a", int(domain.MinPartSize))
		first := putPart(1, firstBody)
		last := putPart(2, "end")
		parts, err := storage.ListParts(ctx, cfg.Bucket, key, id)
		foundFirst, foundLast := false, false
		for _, part := range parts {
			if part == first {
				foundFirst = true
			}
			if part == last {
				foundLast = true
			}
		}
		if err != nil || !foundFirst || !foundLast {
			t.Fatalf("parts=%+v err=%v", parts, err)
		}
		if err := storage.FinishMultipart(ctx, cfg.Bucket, key, id, []domain.Part{first, last}); err != nil {
			t.Fatal(err)
		}
		object, err := storage.HeadObject(ctx, cfg.Bucket, key)
		if err != nil || object.SizeBytes != domain.MinPartSize+3 || object.ContentType != "application/pdf" {
			t.Fatalf("object=%+v err=%v", object, err)
		}
		download, err := storage.PresignDownload(ctx, domain.DownloadInput{Bucket: cfg.Bucket, Key: key, OriginalName: "multipart.pdf", TTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, download.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != http.StatusOK || string(body) != firstBody+"end" {
			t.Fatalf("multipart download: status=%d size=%d err=%v", response.StatusCode, len(body), err)
		}
		id, err = storage.StartMultipart(ctx, cfg.Bucket, "files/aborted", "application/pdf")
		if err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := storage.AbortMultipart(ctx, cfg.Bucket, "files/aborted", id); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := storage.ListParts(ctx, cfg.Bucket, "files/aborted", id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("aborted session: %v", err)
		}
	})
}
