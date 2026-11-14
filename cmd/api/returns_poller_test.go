package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/usecases"
)

func TestLoadNetworkReturnsConfigFlagDefaultsOff(t *testing.T) {
	for _, raw := range []string{"", "false", "0", "off", "no", "typo"} {
		env := map[string]string{"NETWORK_RETURNS_POLLER_ENABLED": raw}
		cfg, err := loadNetworkReturnsConfig(func(k string) string { return env[k] })
		if err != nil || cfg.enabled {
			t.Fatalf("flag %q = enabled=%t err=%v (fail-closed)", raw, cfg.enabled, err)
		}
	}
	// Disabled, a missing URL and vendor are fine: nothing can call out.
	cfg, err := loadNetworkReturnsConfig(func(string) string { return "" })
	if err != nil || cfg.interval != DefaultReturnsPollInterval {
		t.Fatalf("disabled cfg = %+v err=%v", cfg, err)
	}
}

func TestLoadNetworkReturnsConfigEnabledRequiresWiring(t *testing.T) {
	env := map[string]string{"NETWORK_RETURNS_POLLER_ENABLED": "true"}
	getenv := func(k string) string { return env[k] }
	if _, err := loadNetworkReturnsConfig(getenv); err == nil ||
		!strings.Contains(err.Error(), "NETWORK_RETURNS_BASE_URL") {
		t.Fatalf("missing base url = %v", err)
	}
	env["NETWORK_RETURNS_BASE_URL"] = "http://network"
	if _, err := loadNetworkReturnsConfig(getenv); err == nil ||
		!strings.Contains(err.Error(), "NETWORK_RETURNS_VENDOR_ID") {
		t.Fatalf("missing vendor = %v", err)
	}
	env["NETWORK_RETURNS_VENDOR_ID"] = "V-WS"
	env["NETWORK_RETURNS_POLL_INTERVAL"] = "5s"
	cfg, err := loadNetworkReturnsConfig(getenv)
	if err != nil || !cfg.enabled || cfg.baseURL != "http://network" || cfg.vendorID != "V-WS" || cfg.interval != 5*time.Second {
		t.Fatalf("cfg = %+v err=%v", cfg, err)
	}
}

func TestParsePollIntervalFallsBackToDefault(t *testing.T) {
	for _, raw := range []string{"", "nonsense", "-1s", "0"} {
		if got := parsePollInterval(raw); got != DefaultReturnsPollInterval {
			t.Fatalf("parsePollInterval(%q) = %v", raw, got)
		}
	}
	if got := parsePollInterval("2m"); got != 2*time.Minute {
		t.Fatalf("duration = %v", got)
	}
	if got := parsePollInterval("90"); got != 90*time.Second {
		t.Fatalf("plain seconds = %v", got)
	}
}

// countingHalf counts the ticks it sees and can fail (the poll half).
type countingHalf struct {
	ticks atomic.Int64
	err   error
}

func (c *countingHalf) ExecutePoll(context.Context) (usecases.AdvicePollResult, error) {
	c.ticks.Add(1)
	return usecases.AdvicePollResult{Seen: 1, Applied: 1}, c.err
}

func (c *countingHalf) ExecuteDrain(context.Context) (usecases.OutcomeDrainResult, error) {
	c.ticks.Add(1)
	return usecases.OutcomeDrainResult{Submitted: 1}, nil
}

func newTestPoller(pollErr error) (*returnsPoller, *countingHalf, *countingHalf) {
	poll, drain := &countingHalf{err: pollErr}, &countingHalf{}
	return &returnsPoller{
		poll:     pollHalf{poll},
		drain:    drainHalf{drain},
		interval: time.Millisecond,
		logger:   slog.Default(),
	}, poll, drain
}

func runPollerTicks(t *testing.T, p *returnsPoller) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("the poller did not stop with its context")
	}
}

func TestReturnsPollerTicksUntilCancelled(t *testing.T) {
	p, poll, drain := newTestPoller(nil)
	runPollerTicks(t, p)
	if poll.ticks.Load() == 0 || drain.ticks.Load() == 0 {
		t.Fatalf("ticks: polls=%d drains=%d", poll.ticks.Load(), drain.ticks.Load())
	}
}

func TestReturnsPollerTickSurvivesFailures(t *testing.T) {
	p, poll, drain := newTestPoller(errors.New("network down"))
	runPollerTicks(t, p)
	// A failing poll never stopped the drain: both halves kept ticking.
	if poll.ticks.Load() < 2 || drain.ticks.Load() < 2 {
		t.Fatalf("ticks: polls=%d drains=%d (a failure must not stop the loop)", poll.ticks.Load(), drain.ticks.Load())
	}
}

// pollHalf and drainHalf adapt the counting double to the worker's two
// interfaces.
type pollHalf struct{ c *countingHalf }

func (h pollHalf) Execute(ctx context.Context) (usecases.AdvicePollResult, error) {
	return h.c.ExecutePoll(ctx)
}

type drainHalf struct{ c *countingHalf }

func (h drainHalf) Execute(ctx context.Context) (usecases.OutcomeDrainResult, error) {
	return h.c.ExecuteDrain(ctx)
}
