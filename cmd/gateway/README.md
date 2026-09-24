# Gateway example

This service demonstrates session lookup and revocation. It does not implement
user login: a trusted authentication flow must verify a user before calling
`usecase.SessionService.Create`. Do not add a public session creation endpoint
that accepts an arbitrary user ID.

## Configuration and local run

Copy `config.example.yaml` to `config.local.yaml` and edit the HTTP and Redis
host/port values. The default config path in the process is
`/etc/app/config.yaml`; override it with `-config` for local development.
The Redis password must be supplied as the `REDIS_PASSWORD` environment
variable. The service fails at startup if it is missing or Redis cannot be
reached. Do not put the password in YAML.

```sh
cp cmd/gateway/config.example.yaml cmd/gateway/config.local.yaml
export REDIS_PASSWORD='your-local-password'
go run ./cmd/gateway -config cmd/gateway/config.local.yaml
```

## HTTP API

- `GET /health`: process liveness, `200 OK`.
- `GET /sessions/current`: requires `Authorization: Bearer <token>`, returns
  `{"user_id":"..."}` or `401` for an invalid or expired session.
- `DELETE /sessions/current`: requires the same bearer token and revokes it;
  returns `204`. Revoking a missing but well formed token is idempotent.

All responses include `X-Request-ID`. Requests and recovered panics are logged
with that ID using `slog`. Authentication tokens and Redis passwords are not
logged.

Sessions expire after 24 hours. Redis holds only the SHA-256 hash of each
random token as a key and the user ID as its value. Sessions therefore depend
on Redis persistence: if Redis data is lost, users must sign in again. Configure
Redis persistence in the infrastructure repository if sessions must survive a
Redis restart. The service uses Redis database 0 by default and prefixes its
keys with `gateway:session:`.

Run tests with `go test ./...`.
Build the image from the repository root with
`docker build -f cmd/gateway/Dockerfile -t gateway:local .`.
