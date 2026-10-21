package postgres

import (
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // the postgres:// driver
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/claudioed/returns-processing/migrations"
)

// Environment variables naming the database. The PgBouncer rule: DDL needs a
// direct (session) connection, so the binary READS MIGRATIONS_DATABASE_URL for
// the migrations and falls back to DATABASE_URL, which may go through a pooler.
const (
	EnvDatabaseURL           = "DATABASE_URL"
	EnvMigrationsDatabaseURL = "MIGRATIONS_DATABASE_URL"
)

// MigrationsURL returns the URL the migrations run against: the value of
// MIGRATIONS_DATABASE_URL when it is set and non-empty, else DATABASE_URL.
// getenv is os.Getenv in production; it is a parameter so the choice is
// testable without touching the process environment.
func MigrationsURL(getenv func(string) string) string {
	if url := getenv(EnvMigrationsDatabaseURL); url != "" {
		return url
	}
	return getenv(EnvDatabaseURL)
}

// Migrator opens a golang-migrate runner over the embedded migrations. The
// caller closes it.
func Migrator(databaseURL string) (*migrate.Migrate, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("open embedded migrations: %w", err)
	}
	return migrate.NewWithSourceInstance("iofs", src, databaseURL)
}

// RunMigrations applies every pending embedded migration against
// databaseURL. Running it again is a no-op.
func RunMigrations(databaseURL string) error {
	m, err := Migrator(databaseURL)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}
