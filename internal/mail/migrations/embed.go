package migrations

import "embed"

// Files содержит только миграции, принадлежащие сервису Mail.
//
//go:embed *.sql
var Files embed.FS
