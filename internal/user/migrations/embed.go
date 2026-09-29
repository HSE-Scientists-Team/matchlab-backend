package migrations

import "embed"

// Files contains the migrations owned by the user service.
//
//go:embed *.sql
var Files embed.FS
