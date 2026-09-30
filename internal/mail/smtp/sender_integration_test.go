//go:build integration

package smtp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestSenderDeliversToMailpit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "axllent/mailpit:v1.27",
			ExposedPorts: []string{"1025/tcp", "8025/tcp"},
			WaitingFor:   wait.ForListeningPort("1025/tcp").WithStartupTimeout(60 * time.Second),
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
	smtpPort, err := container.MappedPort(ctx, "1025/tcp")
	if err != nil {
		t.Fatal(err)
	}
	apiPort, err := container.MappedPort(ctx, "8025/tcp")
	if err != nil {
		t.Fatal(err)
	}
	sender := NewSender(Config{
		Host: host, Port: smtpPort.Int(), From: "no-reply@example.org",
		VerificationURL: "https://example.org/verify", RequireStartTLS: false,
	})
	if err := sender.SendVerification(ctx, "person@example.org", "test-verification-token", time.Now().Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}

	apiURL := "http://" + host + ":" + apiPort.Port() + "/api/v1/messages"
	client := &http.Client{Timeout: 3 * time.Second}
	for attempt := 0; attempt < 20; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Do(request)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if response.StatusCode == http.StatusOK {
				var result struct {
					Total int `json:"total"`
				}
				if err := json.Unmarshal(body, &result); err != nil {
					t.Fatal(err)
				}
				if result.Total > 0 {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Mailpit не получил письмо: %v", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	t.Fatalf("API Mailpit не показывает доставленных писем по адресу %s", apiURL)
}
