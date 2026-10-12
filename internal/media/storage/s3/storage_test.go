package s3

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/config"
	"github.com/HSE-Scientists-Team/matchlab-backend/internal/media/domain"
)

func testConfig(endpoint string) config.S3 {
	return config.S3{Endpoint: endpoint, PublicEndpoint: "http://localhost:8333", Region: "us-east-1", Bucket: "matchlab-media", UsePathStyle: true, RequestTimeout: time.Second, AccessKey: "media-test", SecretKey: "test-secret"}
}

func TestPresignUsesPublicEndpointAndRequiredHeaders(t *testing.T) {
	storage, err := New(testConfig("http://seaweedfs:8333"))
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range []int64{42} {
		request, err := storage.PresignUpload(context.Background(), domain.UploadInput{Bucket: "matchlab-media", Key: "users/a/b", ContentType: "application/pdf", SizeBytes: size, TTL: 10 * time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(request.URL)
		if err != nil {
			t.Fatal(err)
		}
		if u.Host != "localhost:8333" || u.Path != "/matchlab-media/users/a/b" || request.Method != "PUT" || u.Query().Get("X-Amz-Expires") != "600" {
			t.Fatalf("unexpected signed request: host=%s path=%s method=%s", u.Host, u.Path, request.Method)
		}
		signed := u.Query().Get("X-Amz-SignedHeaders")
		for _, name := range []string{"content-length", "content-type", "if-none-match"} {
			if !strings.Contains(signed, name) {
				t.Errorf("%s is not signed: %s", name, signed)
			}
		}
		if request.Headers["If-None-Match"] != "*" || request.Headers["Content-Type"] != "application/pdf" {
			t.Fatalf("missing required headers: %v", request.Headers)
		}
	}
	download, err := storage.PresignDownload(context.Background(), domain.DownloadInput{Bucket: "matchlab-media", Key: "users/a/b", OriginalName: "отчёт.pdf", TTL: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(download.URL)
	if download.Method != "GET" || u.Host != "localhost:8333" || !strings.HasPrefix(u.Query().Get("response-content-disposition"), "attachment;") {
		t.Fatal("incorrect download parameters")
	}
}

func TestHeadObjectUsesInternalEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" || r.URL.Path != "/matchlab-media/file" || !strings.Contains(r.Header.Get("Authorization"), "Credential=media-test/") {
			t.Errorf("unexpected HEAD request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Length", "42")
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("ETag", `"etag"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	storage, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	object, err := storage.HeadObject(context.Background(), "matchlab-media", "file")
	if err != nil || object.SizeBytes != 42 || object.ContentType != "application/pdf" || object.ETag == nil || object.ChecksumSHA256 != nil {
		t.Fatalf("object: %+v, error: %v", object, err)
	}
}

func TestHeadObjectErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
	}{{404, domain.ErrObjectNotFound}, {403, domain.ErrStorageUnavailable}} {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
			defer server.Close()
			storage, err := New(testConfig(server.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = storage.HeadObject(context.Background(), "matchlab-media", "file")
			if !errors.Is(err, tc.want) {
				t.Fatalf("error=%v, want %v", err, tc.want)
			}
		})
	}
	storage, err := New(testConfig("http://localhost:1"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := storage.HeadObject(ctx, "matchlab-media", "file"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
}

func TestDeleteObject(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{
		{204, "", nil},
		{404, `<Error><Code>NoSuchKey</Code></Error>`, nil},
		{403, `<Error><Code>AccessDenied</Code></Error>`, domain.ErrStorageUnavailable},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != "/matchlab-media/files/key" || r.Header.Get("Authorization") == "" {
				t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(tc.status)
			w.Write([]byte(tc.body))
		}))
		storage, err := New(testConfig(server.URL))
		if err != nil {
			t.Fatal(err)
		}
		err = storage.DeleteObject(context.Background(), "matchlab-media", "files/key")
		server.Close()
		if !errors.Is(err, tc.want) {
			t.Fatalf("status %d: %v", tc.status, err)
		}
	}
}
