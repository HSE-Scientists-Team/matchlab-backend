package migrations

import "embed"

// Files embeds only migrations owned by the mail service.
//
//go:embed *.sql
var Files embed.FS
