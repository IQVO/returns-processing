package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// The consumer supervision wiring, proven without a broker: a dying consumer
// reports on the set's deaths channel (the composition root exits non-zero on
// it), a clean shutdown reports nothing, and every pinned env resolves a
// consumer under its own group.

// dyingConsumer fails on Run after a beat.
type dyingConsumer struct {
	boomAfter time.Duration
	err       error
}

func (c *dyingConsumer) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(c.boomAfter):
		return c.err
	}
}

func (c *dyingConsumer) Close() error { return nil }

// parkedConsumer runs until cancelled.
type parkedConsumer struct{}

func (parkedConsumer) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (parkedConsumer) Close() error { return nil }

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
}

func TestConsumerSet_ADeathIsReportedWithItsEnv(t *testing.T) {
	boom := errors.New("reader failed")
	set := &consumerSet{
		stop:   func() {},
		deaths: make(chan consumerDeath, 3),
	}
	w := &consumerWorker{name: "manifests consumer", env: "MANIFEST_CONSUMER_GROUP", done: make(chan struct{})}
	go set.run(context.Background(), &dyingConsumer{boomAfter: time.Millisecond, err: boom}, w, "g", nil, testLogger())

	select {
	case d := <-set.deaths:
		if d.name != "manifests consumer" || d.env != "MANIFEST_CONSUMER_GROUP" || !errors.Is(d.err, boom) {
			t.Fatalf("death = %+v", d)
		}
		if got := d.describe(); !strings.Contains(got.Error(), "MANIFEST_CONSUMER_GROUP") || !strings.Contains(got.Error(), "reader failed") {
			t.Fatalf("describe() = %v, want the env and the cause", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the death was never reported")
	}
}

func TestConsumerSet_ACleanStopReportsNothing(t *testing.T) {
	set := &consumerSet{
		stop:   func() {},
		deaths: make(chan consumerDeath, 3),
	}
	w := &consumerWorker{name: "order lines consumer", env: "ORDER_LINES_CONSUMER_GROUP", done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	go set.run(ctx, parkedConsumer{}, w, "g", nil, testLogger())
	cancel()

	select {
	case <-w.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the consumer did not stop on cancel")
	}
	select {
	case d := <-set.deaths:
		t.Fatalf("a clean stop was reported as a death: %+v", d)
	default:
	}
}

func TestConsumerBuilders_EveryPinnedEnvBuilds(t *testing.T) {
	builders := consumerBuilders(adapters{})
	want := map[string]bool{}
	for _, env := range consumerGroupEnvs {
		want[env] = true
	}
	if len(builders) != len(want) {
		t.Fatalf("builders = %d keys, want %d", len(builders), len(want))
	}
	for env := range want {
		if builders[env] == nil {
			t.Fatalf("no builder for %s", env)
		}
	}
}
