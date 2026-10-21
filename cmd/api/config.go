package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
)

// The outbox relay host's configuration, all from the environment: this
// service is built step by step and today's binary is the relay host only
// (no HTTP, no consumers yet); every later piece grows in this same root.

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// newLogger builds the JSON logger; LOG_LEVEL debug|info|warn|warning|error
// (case-insensitive; "warning" is accepted like "warn" fleet-wide).
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

// parseRelayInterval reads OUTBOX_RELAY_INTERVAL (Go duration or plain
// seconds), defaulting when unset, malformed or non-positive.
func parseRelayInterval(raw string) time.Duration {
	if raw == "" {
		return time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		if secs, serr := strconv.ParseFloat(raw, 64); serr == nil {
			d, err = time.Duration(secs*float64(time.Second)), nil
		}
	}
	if err != nil || d <= 0 {
		return time.Second
	}
	return d
}

// outboxMaxAttempts reads OUTBOX_MAX_ATTEMPTS: how many times an outbox row
// may fail before the relay excludes it from claims. Unset = the fleet
// default (20); 0 = unlimited; negative or unparsable values use the
// default, so a typo never silently removes the cap.
func outboxMaxAttempts(raw string) int {
	if raw == "" {
		return postgres.DefaultOutboxMaxAttempts
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 0 {
		return postgres.DefaultOutboxMaxAttempts
	}
	return n
}

// brokersFromEnv reads KAFKA_BROKERS (comma separated); nil when unset.
func brokersFromEnv() []string {
	var brokers []string
	for _, b := range strings.Split(os.Getenv("KAFKA_BROKERS"), ",") {
		if b = strings.TrimSpace(b); b != "" {
			brokers = append(brokers, b)
		}
	}
	return brokers
}

// relayConfig is the mandatory configuration of the relay host: real
// Postgres and real Kafka, no default and no degraded mode behind either.
type relayConfig struct {
	databaseURL string
	brokers     []string
	interval    time.Duration
	maxAttempts int
}

// loadRelayConfig reads and validates the mandatory environment, reporting
// every problem at once so the process fails fast at boot, before it opens a
// connection.
func loadRelayConfig() (relayConfig, error) {
	cfg := relayConfig{
		databaseURL: os.Getenv("DATABASE_URL"),
		brokers:     brokersFromEnv(),
		interval:    parseRelayInterval(getenv("OUTBOX_RELAY_INTERVAL", "")),
		maxAttempts: outboxMaxAttempts(os.Getenv("OUTBOX_MAX_ATTEMPTS")),
	}
	var problems []error
	if cfg.databaseURL == "" {
		problems = append(problems, errors.New("DATABASE_URL is required: this service runs on real Postgres"))
	}
	if len(cfg.brokers) == 0 {
		problems = append(problems, errors.New("KAFKA_BROKERS is required: this service runs on real Kafka"))
	}
	if len(problems) > 0 {
		return cfg, fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return cfg, nil
}
