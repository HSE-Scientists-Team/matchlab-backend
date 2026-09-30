package migrations

import "embed"

// Files содержит SQL-миграции PostgreSQL для отдельной джобы миграций.
//
//go:embed *.sql
var Files embed.FS
