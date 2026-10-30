package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The a2a binary has no in-memory mode: a missing DATABASE_URL is a boot
// error naming it, from buildDeps itself too.
func TestBuildDepsRefusesWithoutADatabase(t *testing.T) {
	_, _, err := buildDeps(context.Background(), quietLogger(), "", "")
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("buildDeps without a database = %v, want a boot error naming DATABASE_URL", err)
	}
}

// An unreachable database is a boot error, not a silent serve.
func TestBuildDepsBadDatabaseFailsBoot(t *testing.T) {
	url := "postgres://u@127.0.0.1:1/db" // nothing listens on port 1
	_, _, err := buildDeps(context.Background(), quietLogger(), url, url)
	if err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// buildDeps threads migrationsDatabaseURL into the migration step and
// databaseURL into the pool, never conflating the two: the application URL
// points at nothing (opening the pool, which happens AFTER the migrations
// succeed, would be a dial error), while the migrations URL is a schemeless
// string the migrator rejects at once with a distinctive parse error. If
// buildDeps ran the migrations against databaseURL instead, this test would
// see a dial/"connection refused" error, not the parse error.
func TestBuildDepsUsesMigrationsDatabaseURLNotDatabaseURLForMigrations(t *testing.T) {
	const (
		bogusMigrationsURL = "not-a-valid-connection-string"
		unreachableAppURL  = "postgres://u@127.0.0.1:1/returns_processing?sslmode=disable&connect_timeout=1"
	)
	_, _, err := buildDeps(context.Background(), quietLogger(), unreachableAppURL, bogusMigrationsURL)
	if err == nil {
		t.Fatal("a malformed MIGRATIONS_DATABASE_URL must fail boot")
	}
	if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "dial") {
		t.Fatalf("err = %v: a dial error means the migrations ran against databaseURL instead of migrationsDatabaseURL", err)
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("RP_A2A_TEST_SET", "v")
	t.Setenv("RP_A2A_TEST_EMPTY", "")
	if got := getenv("RP_A2A_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("RP_A2A_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
