//go:build integration

package analyticsstore

import (
	"context"
	"os"
	"testing"

	"github.com/claudioed/returns-processing/internal/analytics/report"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// The infrastructure comes from testcontainers, never from the environment.
func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// newPostgresStore migrates an EMPTY analytical database with the projector's
// own embedded migrations and returns the writer and the reader over it —
// the exact pair cmd/returns-projector and cmd/returns-reports wire.
func newPostgresStore(t *testing.T) store {
	t.Helper()
	url := pgtest.EmptyURL(t) // no OLTP migrations: the analytical set applies here
	if err := RunMigrations(url); err != nil {
		t.Fatalf("run analytics migrations: %v", err)
	}
	pool, err := NewPool(context.Background(), url)
	if err != nil {
		t.Fatalf("open analytics pool: %v", err)
	}
	t.Cleanup(pool.Close)
	ro, err := NewReadOnlyPool(context.Background(), url)
	if err != nil {
		t.Fatalf("open read-only analytics pool: %v", err)
	}
	t.Cleanup(ro.Close)
	return storePair{Projection: NewProjection(pool), Reader: NewReader(ro)}
}

// storePair pairs a Projection and a Reader into one store.
type storePair struct {
	report.Projection
	report.Reader
}

// The SAME contract as the in-memory store, on real Postgres: the two stores
// cannot drift.
func TestPostgresContract(t *testing.T) {
	runContract(t, newPostgresStore)
}
