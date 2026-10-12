package migrations

import "embed"

// Files содержит SQL-миграции PostgreSQL для запуска всеми сервисами.
//
//go:embed *.sql
var Files embed.FS
