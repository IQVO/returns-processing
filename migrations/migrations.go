// Package migrations embeds the golang-migrate SQL migrations so the binary
// needs no migrations directory on disk: the postgres adapter opens the
// embedded FS (internal/adapters/outbound/postgres.Migrator).
package migrations

import "embed"

// FS holds every *.sql migration of this service.
//
//go:embed *.sql
var FS embed.FS
