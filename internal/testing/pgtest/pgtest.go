// Package pgtest boots ONE real Postgres through testcontainers per test
// binary and hands every test its own database, cloned from a template that
// already has the embedded migrations applied. It never reads DATABASE_URL
// and never skips: a missing Docker is a test failure (fleet rule,
// TestPostgresIntegrationTestsUseTestcontainers).
package pgtest

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
)

const (
	user         = "returns_test"
	password     = "returns_test"
	adminDB      = "postgres"
	templateName = "returns_template"
)

var (
	once      sync.Once
	container *tcpostgres.PostgresContainer
	startErr  error
	seq       atomic.Int64
)

func start() {
	ctx := context.Background()
	container, startErr = tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase(adminDB),
		tcpostgres.WithUsername(user),
		tcpostgres.WithPassword(password),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(90*time.Second)),
	)
	if startErr != nil {
		return
	}
	startErr = buildTemplate(ctx)
}

// buildTemplate creates the template database and applies the migrations to
// it (twice: boot runs them on every start, the second run must be a no-op).
func buildTemplate(ctx context.Context) error {
	if err := adminExec(ctx, `CREATE DATABASE `+templateName); err != nil {
		return err
	}
	url, err := databaseURL(ctx, templateName)
	if err != nil {
		return err
	}
	for range 2 {
		if err := postgres.RunMigrations(url); err != nil {
			return fmt.Errorf("migrations: %w", err)
		}
	}
	return nil
}

func databaseURL(ctx context.Context, db string) (string, error) {
	base, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", err
	}
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable", user, password, cfg.Host, cfg.Port, db), nil
}

func adminExec(ctx context.Context, sql string) error {
	url, err := databaseURL(ctx, adminDB)
	if err != nil {
		return err
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	_, err = conn.Exec(ctx, sql)
	return err
}

// URL returns the DSN of a FRESH database (migrations applied) on the shared
// container.
func URL(t *testing.T) string {
	t.Helper()
	return newDatabase(t, templateName)
}

// EmptyURL returns the DSN of a FRESH database with NO migrations applied,
// for the tests that run the migrations themselves.
func EmptyURL(t *testing.T) string {
	t.Helper()
	return newDatabase(t, "template0")
}

func newDatabase(t *testing.T, template string) string {
	t.Helper()
	once.Do(start)
	if startErr != nil {
		t.Fatalf("start postgres container: %v", startErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := fmt.Sprintf("t_%d", seq.Add(1))
	if err := adminExec(ctx, `CREATE DATABASE `+name+` TEMPLATE `+template); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	url, err := databaseURL(ctx, name)
	if err != nil {
		t.Fatalf("database url: %v", err)
	}
	return url
}

// NewPool returns a pool on a fresh database (migrations applied), closed
// when the test ends.
func NewPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	return Open(t, URL(t))
}

// Open returns a pool on the database at databaseURL (one of URL/EmptyURL),
// closed when the test ends.
func Open(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// Count returns the number of rows of table (a trusted literal of the test).
func Count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// Shutdown terminates the shared container; call it from TestMain.
func Shutdown() {
	if container != nil {
		_ = testcontainers.TerminateContainer(container)
	}
}
