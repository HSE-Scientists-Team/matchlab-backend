# User service

User owns account identity, login, password hashes, and email ownership in the
PostgreSQL `users` schema. Registration does not require an email. Login and
registration use a case-insensitive login normalized to lowercase. Passwords
are stored as bcrypt hashes. After login, User asks Auth over gRPC to create a
session.

## Email model

- `user_account.login` is unique without regard to letter case, 1–32 ASCII
  letters, digits, `_`, `-`, or `.`. Email is not used to sign in.
- `email_verification_request` stores a user's one current pending address and
  a SHA-256 hash of its one-time, 30-minute token. Multiple users may have
  pending requests for the same address.
- `user_email` stores only confirmed addresses. A case-insensitive unique index
  allows an address to belong to one user only. A user can have one confirmed
  email; confirming a replacement changes that user's current address.
- A token belongs to the user who requested it. Confirming one user's token
  cannot confirm another user's request, even when both requested the same
  address. If someone else confirmed it first, the later confirmation fails
  with a conflict and leaves the current owner unchanged.

User sends verification requests to the internal Mail gRPC service. Mail
persists an encrypted delivery job and handles SMTP retries. Verification
tokens stay owned by User; Mail cannot confirm an address. See [Mail](../mail/README.md)
for SMTP and delivery behavior.

## PostgreSQL and migrations

Goose migrations are embedded in the service binary and applied before its gRPC
listener opens. Migration 00002 separates login from email and keeps only
previously verified legacy addresses. Previous unverified addresses are
discarded during upgrade because they did not establish ownership. Rolling
back 00002 discards pending requests and restores only verified addresses;
accounts without verified addresses have a NULL legacy email after rollback.

## Run locally

Run all services with `docker compose up --build`. Gateway listens on
`http://localhost:8080`; Mailpit captures verification messages at
`http://localhost:8025`.

For direct Go development, start dependencies and Mail with Compose, then run
Auth, User, and Gateway with their `config.example.yaml` files. Required
secrets are `POSTGRES_PASSWORD`, `REDIS_PASSWORD` (Auth only), and
`MAIL_ENCRYPTION_KEY`, `SMTP_USERNAME`, `SMTP_PASSWORD` (Mail). For local
development, start `docker compose up -d postgres redis mailpit`, then run Auth,
Mail, User, and Gateway locally. Compose binds service ports to localhost only.
The Compose encryption key and Mailpit SMTP settings are development-only.

## Internal gRPC API

- `Register(login, password)` creates an active account without an email.
- `Login(login, password)` verifies credentials and returns a session.
- `RequestEmailVerification(user_id, email)` replaces that user's pending
  request and sends a confirmation message.
- `ConfirmEmail(token)` confirms and assigns the address if no other user owns
  it.
- `GetEmailStatus(user_id)` returns `not_set`, `pending`, or `verified`, along
  with any current and pending addresses.

Run `go test ./internal/user/...`. The Goose integration test uses real
PostgreSQL and checks the duplicate-pending / single-confirmed email rule:
`go test -tags=integration ./internal/user/migrations`.
