package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// returnsPoller is the background worker of ADR 0012: every tick it polls
// the network's return advices into the advice view, then drains the stored
// outcomes one step each. A failure is logged and the next tick retries —
// a network outage degrades the feature, never the pod (ADR 0012 Decision
// 6: unlike a dead Kafka consumer, a delayed poll freezes nothing the REST
// surface serves).
type returnsPoller struct {
	poll     advicePoller
	drain    outcomeDrainer
	interval time.Duration
	logger   *slog.Logger
}

// advicePoller and outcomeDrainer are the worker's two halves, as small
// interfaces so the tick loop is testable without a network (the concrete
// use cases satisfy them).
type advicePoller interface {
	Execute(context.Context) (usecases.AdvicePollResult, error)
}

type outcomeDrainer interface {
	Execute(context.Context) (usecases.OutcomeDrainResult, error)
}

// Run ticks until ctx is cancelled; it returns nil on cancellation.
func (p *returnsPoller) Run(ctx context.Context) error {
	t := time.NewTicker(p.interval)
	defer t.Stop()
	p.logger.Info("network return-advices poller running", "interval", p.interval.String())
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			p.tick(ctx)
		}
	}
}

// tick is one poll plus one drain; each half's failure is a warning, never
// a stop.
func (p *returnsPoller) tick(ctx context.Context) {
	if res, err := p.poll.Execute(ctx); err != nil {
		p.logger.Warn("network return-advices poll failed; the next tick retries", "error", err)
	} else if res.Seen > 0 {
		p.logger.Info("network return advices polled", "seen", res.Seen, "applied", res.Applied)
	}
	if res, err := p.drain.Execute(ctx); err != nil {
		p.logger.Warn("network return-outcome drain failed; the next tick retries", "error", err)
	} else if res.Submitted+res.Settled+res.Failed > 0 {
		p.logger.Info("network return outcomes drained",
			"submitted", res.Submitted, "settled", res.Settled, "failed", res.Failed)
	}
}

// networkReturnsConfig is the poller's environment (ADR 0012): a flag that
// defaults OFF (fail-closed: a typo must not turn a live integration on),
// and — once enabled — a mandatory base URL and vendor id.
type networkReturnsConfig struct {
	enabled  bool
	baseURL  string
	vendorID string
	interval time.Duration
}

// DefaultReturnsPollInterval is the tick of the poller when
// NETWORK_RETURNS_POLL_INTERVAL is unset, malformed or non-positive.
const DefaultReturnsPollInterval = 30 * time.Second

// loadNetworkReturnsConfig reads NETWORK_RETURNS_POLLER_ENABLED (true, 1,
// yes or on — case-insensitive), NETWORK_RETURNS_BASE_URL,
// NETWORK_RETURNS_VENDOR_ID and NETWORK_RETURNS_POLL_INTERVAL (Go duration
// or plain seconds).
func loadNetworkReturnsConfig(getenv func(string) string) (networkReturnsConfig, error) {
	cfg := networkReturnsConfig{
		enabled:  isTrue(getenv("NETWORK_RETURNS_POLLER_ENABLED")),
		baseURL:  getenv("NETWORK_RETURNS_BASE_URL"),
		vendorID: getenv("NETWORK_RETURNS_VENDOR_ID"),
		interval: parsePollInterval(getenv("NETWORK_RETURNS_POLL_INTERVAL")),
	}
	if !cfg.enabled {
		return cfg, nil
	}
	if cfg.baseURL == "" {
		return cfg, errRequiredEnv("NETWORK_RETURNS_BASE_URL", "the return-advices poller is enabled (NETWORK_RETURNS_POLLER_ENABLED) and needs the network's Vendor API base URL")
	}
	if cfg.vendorID == "" {
		return cfg, errRequiredEnv("NETWORK_RETURNS_VENDOR_ID", "the return-advices poller is enabled (NETWORK_RETURNS_POLLER_ENABLED) and needs the vendor this service operates as")
	}
	return cfg, nil
}

// parsePollInterval mirrors the relay-interval rules: unset, malformed or
// non-positive values use the default, so a typo never spins the loop.
func parsePollInterval(raw string) time.Duration {
	if raw == "" {
		return DefaultReturnsPollInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := parseSeconds(raw); serr == nil {
			d, err = secs, nil
		}
	}
	if err != nil || d <= 0 {
		return DefaultReturnsPollInterval
	}
	return d
}

// parseSeconds reads a plain seconds number as a duration.
func parseSeconds(raw string) (time.Duration, error) {
	secs, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(secs * float64(time.Second)), nil
}

// isTrue reads a boolean env flag: true, 1, yes or on (case-insensitive);
// anything else — including the empty string — is off (fail-closed).
func isTrue(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// errRequiredEnv is the boot error of a missing mandatory variable, named
// for the operator.
func errRequiredEnv(name, why string) error {
	return fmt.Errorf("%s is required: %s", name, why)
}
