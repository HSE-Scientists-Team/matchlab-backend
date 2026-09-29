# Mail service

Mail is an internal gRPC service for sending verification emails. User owns
verification state and tokens; Mail owns SMTP delivery and a durable PostgreSQL
outbox in the `mail` schema. Enqueue returns only after the job is committed.

## Delivery behavior

- Verification tokens are encrypted with AES-256-GCM before they enter the
  outbox. The encryption key is a required 32-byte hex value in
  `MAIL_ENCRYPTION_KEY`; never put it in YAML. Keep this key stable while jobs
  are queued. Before rotating it, drain the outbox or re-encrypt queued jobs.
- Workers claim jobs with row locks and leases, so multiple replicas can
  process the queue. Failed SMTP deliveries retry with exponential backoff,
  up to the configured limit and only while the verification token is valid.
- Delivery is at-least-once. If SMTP accepts a message but the connection fails
  before Mail records success, a retry can send a duplicate.
- Sent and permanently failed jobs have their encrypted token erased. The
  remaining delivery history is removed after the configured retention period.

Production SMTP settings live in `config.yaml`: host, port, sender address,
verification URL, and `require_starttls: true`. Credentials come from
`SMTP_USERNAME` and `SMTP_PASSWORD`; authenticated SMTP requires STARTTLS. Use a
trusted SMTP provider and a real HTTPS verification URL. PostgreSQL password is
`POSTGRES_PASSWORD`.

## Local development

Run the full project with `docker compose up --build`. Compose starts Mailpit as
the SMTP server. Open `http://localhost:8025` to inspect messages. The local
encryption key and SMTP setup are development-only.

The verification URL is intended for the product frontend. For API-only local
testing, copy the `token` query value from the Mailpit message and send it to
`POST /api/v1/auth/email/confirm`.

## API

`SendVerificationEmail(recipient, token, expires_at)` stores an idempotent job
and returns once PostgreSQL has committed it. The SMTP worker builds the
message from the configured sender and verification URL. Mail has no public
HTTP endpoint and does not read User's tables.

The service applies its embedded Goose migration at startup. Run
`go test ./internal/mail/...`; container-backed PostgreSQL and SMTP checks run
with `go test -tags=integration ./internal/mail/migrations ./internal/mail/smtp`.
