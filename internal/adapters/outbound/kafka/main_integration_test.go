//go:build integration

package kafka_test

import (
	"os"
	"testing"

	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/claudioed/returns-processing/internal/testing/kafkatest"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// The infrastructure comes from testcontainers, never from the environment:
// internal/testing/pgtest boots ONE Postgres and internal/testing/kafkatest ONE
// Kafka per test binary (each lazily, on first use); TestMain only tears them
// down at the end.
//
// The Postgres container module is referenced here so the import stays
// reachable from this package's integration files (the testcontainers sensor
// requires it next to the pgx usage).
var _ = tcpostgres.Run

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}
