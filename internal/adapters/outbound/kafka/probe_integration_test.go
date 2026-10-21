//go:build integration

package kafka

import (
	"context"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
)

// The probe that found the transport-cache bug: a warm kafka-go client (and
// every client sharing DefaultTransport) answers metadata from cache after
// the broker died. With the throwaway-transport fix the check must detect
// both a paused (frozen, silent) and a terminated (refusing) broker.
func TestProbeBrokerCheckAgainstStoppedContainer(t *testing.T) {
	ctx := context.Background()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.6.1", tckafka.WithClusterID("probe"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = testcontainers.TerminateContainer(c) }()
	brokers, err := c.Brokers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check := NewBrokerCheck(brokers)
	liveCtx, liveCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer liveCancel()
	if err := check.Check(liveCtx); err != nil {
		t.Fatalf("live check = %v", err)
	}
	t.Logf("live check ok against %v", brokers)

	if err := testcontainers.TerminateContainer(c); err != nil {
		t.Fatalf("terminate: %v", err)
	}
	for i := 0; i < 5; i++ {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := check.Check(cctx)
		cancel()
		t.Logf("check %d after terminate: %v", i, err)
		if err == nil {
			t.Errorf("check %d passed against a terminated broker: readiness must dial for real", i)
		}
	}
}
