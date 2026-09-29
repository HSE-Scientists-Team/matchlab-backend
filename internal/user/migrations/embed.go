package migrations

import "embed"

// Files содержит миграции, принадлежащие сервису User.
//
//go:embed *.sql
var Files embed.FS
