package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
)

// The shutdown contract of serveUntilSignal, proven without Postgres or
// Kafka: a cancellation of the CALLER's ctx (how the in-process tests stop
// run, and how any embedding caller would) is a clean shutdown in the fleet
// order with the relay stopped LAST, never "outbox relay stopped"; a relay
// that returns on its own while the service is serving is still a death that
// ends run with an error.

// fakeRelay stands in for the outbox relay. Like the real one it returns nil
// once its context is cancelled; stopOnItsOwn makes it return at once while
// its context is still live (a relay that died), with err (nil = a bare
// return).
type fakeRelay struct {
	stopOnItsOwn bool
	err          error

	started  chan struct{}
	ctxDone  atomic.Bool
	returned atomic.Bool
}

func newFakeRelay() *fakeRelay { return &fakeRelay{started: make(chan struct{})} }

func (f *fakeRelay) Run(ctx context.Context) error {
	close(f.started)
	defer f.returned.Store(true)
	if f.stopOnItsOwn {
		return f.err
	}
	<-ctx.Done()
	f.ctxDone.Store(true)
	return nil
}

// idleConsumers is a consumer set with no worker: nothing dies, shutdown
// returns at once.
func idleConsumers() *consumerSet {
	return &consumerSet{stop: func() {}, deaths: make(chan consumerDeath, 1)}
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// serve runs serveUntilSignal in the background on a loopback ephemeral port.
func serve(ctx context.Context, readiness *inboundhttp.Readiness, relay relayRunner) <-chan error {
	done := make(chan error, 1)
	srv := &http.Server{Addr: "127.0.0.1:0", Handler: http.NotFoundHandler(), ReadHeaderTimeout: time.Second}
	go func() { done <- serveUntilSignal(ctx, quietLogger(), srv, readiness, relay, idleConsumers()) }()
	return done
}

func waitResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(15 * time.Second):
		t.Fatal("serveUntilSignal did not return")
		return nil
	}
}

// A caller-ctx cancellation must not reach the relay before the shutdown
// sequence does: the relay keeps running through the readiness flip and the
// drain delay, is stopped last, and the shutdown returns nil. Deriving the
// relay's context from the caller's ctx cancels it at the same instant as
// sigCtx — the relay then returns on relayDone, races sigCtx.Done() in the
// serving select, and run reports "outbox relay stopped" for a clean stop
// (the red `integration` job of PR #67).
func TestServeUntilSignal_ACallerCancelIsACleanShutdownThatStopsTheRelayLast(t *testing.T) {
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "300ms")
	readiness := inboundhttp.NewReadiness(0, 0)
	relay := newFakeRelay()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := serve(ctx, readiness, relay)
	<-relay.started

	cancel()
	deadline := time.Now().Add(5 * time.Second)
	for readiness.Ready() {
		select {
		case err := <-done:
			t.Fatalf("serveUntilSignal returned %v without the drain (readiness never flipped): the caller's "+
				"cancellation was taken for a relay that stopped on its own", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness never flipped to not-ready after the caller's cancellation")
		}
		time.Sleep(time.Millisecond)
	}
	// Inside the 300 ms drain window: the relay must still be relaying.
	if relay.ctxDone.Load() || relay.returned.Load() {
		t.Error("the caller's cancellation stopped the relay before the drain: its context must outlive the " +
			"caller's ctx and end only at the shutdown's relay step (stopped LAST)")
	}

	if err := waitResult(t, done); err != nil {
		t.Fatalf("a caller-ctx cancellation must be a clean shutdown, serveUntilSignal returned %v", err)
	}
	if !relay.returned.Load() {
		t.Error("the shutdown returned without stopping and awaiting the relay")
	}
}

// The exact symptom, repeated: with no drain delay the window between the
// caller's cancellation and the serving select is as small as it gets, and
// not a single stop may come back as an error.
func TestServeUntilSignal_ACallerCancelNeverReportsTheRelayStopped(t *testing.T) {
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0s")
	const rounds = 200
	failures := 0
	var first error
	for range rounds {
		relay := newFakeRelay()
		ctx, cancel := context.WithCancel(context.Background())
		done := serve(ctx, inboundhttp.NewReadiness(0, 0), relay)
		<-relay.started
		cancel()
		if err := waitResult(t, done); err != nil {
			failures++
			if first == nil {
				first = err
			}
		}
	}
	if failures > 0 {
		t.Fatalf("%d of %d caller-ctx cancellations returned an error (first: %v), want 0", failures, rounds, first)
	}
}

// A relay that returns on its own while the service is serving is a death:
// nothing can leave the service any more, so serveUntilSignal stops serving
// and returns an error (the pod restarts) — a bare return reports "outbox
// relay stopped", a failing one its own error.
func TestServeUntilSignal_ARelayThatStopsOnItsOwnIsAnError(t *testing.T) {
	t.Setenv("SHUTDOWN_DRAIN_DELAY", "0s")
	boom := errors.New("relay exploded")
	for _, tc := range []struct {
		name    string
		err     error
		wantErr string
	}{
		{name: "a bare return", err: nil, wantErr: "outbox relay stopped"},
		{name: "a failure", err: boom, wantErr: boom.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			relay := newFakeRelay()
			relay.stopOnItsOwn, relay.err = true, tc.err
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			err := waitResult(t, serve(ctx, inboundhttp.NewReadiness(0, 0), relay))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("a relay that stopped on its own must end serving with %q, got %v", tc.wantErr, err)
			}
			if tc.err != nil && !errors.Is(err, boom) {
				t.Fatalf("the relay's own error must be returned, got %v", err)
			}
		})
	}
}
