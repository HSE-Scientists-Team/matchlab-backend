# Gateway

Gateway is the public HTTP entry point. It owns HTTP routes, request IDs,
middleware, and HTTP-to-gRPC error mapping. It does not access PostgreSQL or
Redis directly. Auth stores and checks sessions; User stores accounts and
handles registration and password login. Gateway calls both over gRPC.

## Configuration and local run

The default config path is `/etc/app/config.yaml`; override it with `-config`.
`config.example.yaml` contains HTTP and internal gRPC host/port settings.

Start the complete local project from the repository root (PostgreSQL, Redis,
Auth, User, and Gateway):

```sh
docker compose up --build
```

Gateway is available at `http://localhost:8080`. Stop the services with
`Ctrl+C`; run `docker compose down` to stop and remove containers. PostgreSQL
data remains in its named volume. Compose uses `config.compose.yaml` files with
Docker service DNS names, separate from the localhost configs used with `go run`.

To run Go services directly for development, start only the dependencies:

```sh
docker compose up -d postgres redis
```

Then run each service in its own terminal:

```sh
export POSTGRES_PASSWORD=matchlab_local_only
export REDIS_PASSWORD=matchlab_redis_local
go run ./cmd/auth -config cmd/auth/config.example.yaml
```

```sh
export POSTGRES_PASSWORD=matchlab_local_only
export REDIS_PASSWORD=matchlab_redis_local
go run ./cmd/user -config cmd/user/config.example.yaml
```

```sh
go run ./cmd/gateway -config cmd/gateway/config.example.yaml
```

Compose credentials are for local development only. PostgreSQL data persists in
a named volume. Redis has no volume, so restarting it clears sessions.

## HTTP API

- `GET /health`: process liveness.
- `POST /api/v1/auth/register`: JSON `{ "login": "...", "password": "..." }`;
  returns `201` with the new `user_id`.
- `POST /api/v1/auth/login`: same JSON shape; returns `user_id` and
  `access_token`.
- `GET /api/v1/users/me/email`: requires a bearer token; returns the current
  email `status` (`not_set`, `pending`, or `verified`) and pending/current
  addresses.
- `POST /api/v1/users/me/email`: requires a bearer token and JSON
  `{ "email": "..." }`; requests an email confirmation message and returns
  `202 Accepted`.
- `POST /api/v1/auth/email/confirm`: JSON `{ "token": "..." }`; confirms the
  address tied to that one-time token and returns `204`.
- `GET /api/v1/sessions/current`: requires `Authorization: Bearer <token>` and
  returns the session's `user_id`.
- `DELETE /api/v1/sessions/current`: revokes the bearer token and returns `204`.

Registration and login are routed to User; session validation and revocation
are routed to Auth. Routes live under `/api/v1` so future HTTP APIs can be added
in separate route groups without making Gateway depend on service storage.
All responses include `X-Request-ID`; logs never include passwords or tokens.
Compose runs Mailpit locally: open `http://localhost:8025` to view test emails.

Run tests with `go test ./...`. Build with
`docker build -f cmd/gateway/Dockerfile -t gateway:local .`.
