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

// The composition root's configuration, all from the environment. Every
// mandatory variable is a boot error naming it: this service runs on real
// Postgres and real Kafka, and the consumer groups are read (and required)
// even though the consumers themselves land in a later step — a deployment
// missing one must fail at boot, not at the step that first uses it.

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

// DefaultShutdownDrainDelay is how long shutdown waits, after flipping
// /readyz to not-ready, before closing the listener (SHUTDOWN_DRAIN_DELAY
// overrides it; "0" disables it).
const DefaultShutdownDrainDelay = 5 * time.Second

// shutdownDrainDelay reads SHUTDOWN_DRAIN_DELAY ("0" disables; unset,
// negative or unparsable = the default).
func shutdownDrainDelay() time.Duration {
	raw := os.Getenv("SHUTDOWN_DRAIN_DELAY")
	if raw == "" {
		return DefaultShutdownDrainDelay
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return DefaultShutdownDrainDelay
	}
	return d
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

// consumerGroups are the pinned consumer group env names of
// .claude/rules/integration-events.md (one per consumed topic; the consumers
// themselves are a later step — the ids are read now so a deployment missing
// one fails at boot).
var consumerGroupEnvs = []string{
	"PRODUCT_CLASSIFICATION_CONSUMER_GROUP",
	"ORDER_LINES_CONSUMER_GROUP",
	"MANIFEST_CONSUMER_GROUP",
}

// bootConfig is the mandatory configuration of the service: real Postgres,
// real Kafka, the three pinned consumer groups. There is no default and no
// degraded mode behind any of it.
type bootConfig struct {
	databaseURL string
	brokers     []string
	interval    time.Duration
	maxAttempts int
	httpAddr    string
	// groups carries the env NAME -> group id of every pinned consumer.
	groups map[string]string
}

// loadBootConfig reads and validates the mandatory environment, reporting
// every problem at once, each naming its variable, so the process fails fast
// at boot, before it opens a connection or binds the listener.
func loadBootConfig() (bootConfig, error) {
	cfg := bootConfig{
		databaseURL: os.Getenv("DATABASE_URL"),
		brokers:     brokersFromEnv(),
		interval:    parseRelayInterval(getenv("OUTBOX_RELAY_INTERVAL", "")),
		maxAttempts: outboxMaxAttempts(os.Getenv("OUTBOX_MAX_ATTEMPTS")),
		httpAddr:    getenv("HTTP_ADDR", ":8080"),
		groups:      map[string]string{},
	}
	var problems []error
	if cfg.databaseURL == "" {
		problems = append(problems, errors.New("DATABASE_URL is required: this service runs on real Postgres"))
	}
	if len(cfg.brokers) == 0 {
		problems = append(problems, errors.New("KAFKA_BROKERS is required: this service runs on real Kafka"))
	}
	for _, name := range consumerGroupEnvs {
		id := strings.TrimSpace(os.Getenv(name))
		if id == "" {
			problems = append(problems, fmt.Errorf("%s is required: the consumers run on the shared broker and their group ids are pinned in .claude/rules/integration-events.md", name))
			continue
		}
		cfg.groups[name] = id
	}
	if len(problems) > 0 {
		return bootConfig{}, fmt.Errorf("invalid configuration: %w", errors.Join(problems...))
	}
	return cfg, nil
}
