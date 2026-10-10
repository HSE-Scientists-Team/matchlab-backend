package s3

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestPresignPartBindsUploadNumberAndSize(t *testing.T) {
	storage, err := New(testConfig("http://seaweedfs:8333"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := storage.PresignPart(context.Background(), "matchlab-media", "users/file", "upload-id", 2, 42, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(request.URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "localhost:8333" || u.Query().Get("uploadId") != "upload-id" || u.Query().Get("partNumber") != "2" || !strings.Contains(u.Query().Get("X-Amz-SignedHeaders"), "content-length") || request.Method != "PUT" || request.Headers["Content-Length"] != "42" {
		t.Fatal("multipart signing parameters incorrect")
	}
}

func TestListPartsPaginates(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("uploadId") != "id" {
			t.Error("missing upload ID")
		}
		w.Header().Set("Content-Type", "application/xml")
		if calls == 1 {
			fmt.Fprint(w, `<ListPartsResult><IsTruncated>true</IsTruncated><NextPartNumberMarker>1</NextPartNumberMarker><Part><PartNumber>1</PartNumber><ETag>first</ETag><Size>5242880</Size></Part></ListPartsResult>`)
		} else {
			if r.URL.Query().Get("part-number-marker") != "1" {
				t.Error("missing continuation")
			}
			fmt.Fprint(w, `<ListPartsResult><IsTruncated>false</IsTruncated><Part><PartNumber>2</PartNumber><ETag>last</ETag><Size>3</Size></Part></ListPartsResult>`)
		}
	}))
	defer server.Close()
	storage, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	parts, err := storage.ListParts(context.Background(), "matchlab-media", "file", "id")
	if err != nil || calls != 2 || len(parts) != 2 || parts[1].SizeBytes != 3 {
		t.Fatalf("parts=%+v calls=%d err=%v", parts, calls, err)
	}
}
