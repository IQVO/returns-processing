//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	kafkaconsumer "github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/testing/kafkatest"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// The consumer integration proofs run on REAL infrastructure, both started
// with testcontainers (never skip-gated, never a hardcoded broker address):
// internal/testing/kafkatest boots one Kafka and internal/testing/pgtest one
// Postgres per test binary; TestMain tears them down at the end. The modules
// are referenced here so their imports stay reachable from these
// Kafka-and-Postgres-touching files (the fleet's sensors read exactly that).
var (
	_ = tckafka.Run
	_ = tcpostgres.Run
)

func TestMain(m *testing.M) {
	code := m.Run()
	kafkatest.Shutdown()
	pgtest.Shutdown()
	os.Exit(code)
}

// itctx bounds one interaction with Postgres or Kafka (fleet rule: <= 30s).
func itctx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// testWriter sends the consumer's log lines to the test output, so a failed
// run shows what the consumer did.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}

func itLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(testWriter{t}, nil))
}

// newTopic creates the consumed topic and its dead-letter topic explicitly
// and waits for both leaders (the test broker has auto-creation OFF; the
// cluster broker auto-creates on first write, the fleet convention).
func newTopic(t *testing.T, base string) (topic, dlq string) {
	t.Helper()
	topic = kafkatest.Topic(t, base)
	dlq = topic + kafkaconsumer.DLQSuffix
	kafkatest.CreateTopic(t, dlq)
	return topic, dlq
}

// groupFor is the consumer group id of an integration test: unique per
// topic, like the environment variable a deployment sets.
func groupFor(name, topic string) string { return "itest-" + name + "-" + topic }

// runConsumer starts c and stops it (and waits for it) when the test ends.
func runConsumer(t *testing.T, c *kafkaconsumer.Consumer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = c.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(60 * time.Second):
			t.Log("consumer did not stop within 60s of cancellation")
		}
		_ = c.Close()
	})
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// assertCommittedPast waits until the group committed `want` offsets of
// topic: every message — the skipped ones (a legacy flat envelope, an
// unknown type) and the dead-lettered one included — was committed past.
func assertCommittedPast(t *testing.T, group, topic string, want int64) {
	t.Helper()
	eventually(t, fmt.Sprintf("%d committed offsets of %s", want, topic),
		func() bool { return kafkatest.CommittedOffset(t, group, topic) == want })
}
