// Package migrations embeds the SQL migration files so the binary can
// migrate the database regardless of the current working directory.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
