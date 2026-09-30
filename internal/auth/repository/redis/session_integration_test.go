//go:build integration

package redis

import (
	"context"
	"testing"
	"time"

	goRedis "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func TestSessionStoreAgainstRedisContainer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "redis:7.4-alpine",
			ExposedPorts: []string{"6379/tcp"},
			WaitingFor:   wait.ForListeningPort("6379/tcp").WithStartupTimeout(60 * time.Second),
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
	port, err := container.MappedPort(ctx, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	client := goRedis.NewClient(&goRedis.Options{Addr: host + ":" + port.Port()})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}

	store := NewSessionStore(NewClientAdapter(client))
	if err := store.Save(ctx, "raw-bearer-token", "user-123", time.Minute); err != nil {
		t.Fatal(err)
	}
	userID, err := store.Find(ctx, "raw-bearer-token")
	if err != nil || userID != "user-123" {
		t.Fatalf("Find вернул %q, %v", userID, err)
	}
	if err := store.Delete(ctx, "raw-bearer-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Find(ctx, "raw-bearer-token"); err == nil {
		t.Fatal("отозванный сеанс всё ещё существует")
	}
}
