# Auth service

Auth owns short-lived login sessions in Redis. It has no PostgreSQL connection
or SQL migrations. User accounts and password hashes belong to the user service.
Auth exposes an internal gRPC API; the gateway validates/revokes sessions and
the user service creates a session after a successful login.

## Configuration and local run

The service loads YAML from `/etc/app/config.yaml` by default. Override the path
with `-config`. Redis host, port, and database are nonsecret YAML settings; the
password must come from `REDIS_PASSWORD`.

Run the entire project with `docker compose up --build` from the repository
root. To run Auth directly while developing, start Redis and then Auth:

```sh
docker compose up -d redis
export REDIS_PASSWORD=matchlab_redis_local
go run ./cmd/auth -config cmd/auth/config.example.yaml
```

The local Compose password is for development only. Redis has no persistent
volume: restarting or recreating it clears sessions and requires users to log
in again. Production persistence is an infrastructure decision.

## gRPC API

- `CreateSession(user_id)` creates a cryptographically random bearer token.
- `ValidateSession(token)` returns the associated user ID or `Unauthenticated`.
- `RevokeSession(token)` removes a session; revoking an absent valid token is
  idempotent.

Only a SHA-256 hash of a token is used as the Redis key. Tokens and the Redis
password are never logged. Sessions expire after 24 hours. Run
`go test ./internal/auth/...` for the service packages. The integration test
uses Testcontainers and a real Redis image: `go test -tags=integration
./internal/auth/repository/redis`. Build the image from the repository root
with `docker build -f cmd/auth/Dockerfile -t auth:local .`.
