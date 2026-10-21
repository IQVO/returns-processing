package main

import (
	"strings"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
)

func TestParseRelayInterval(t *testing.T) {
	cases := []struct {
		raw  string
		want time.Duration
	}{
		{"", time.Second},
		{"250ms", 250 * time.Millisecond},
		{"2s", 2 * time.Second},
		{"5", 5 * time.Second}, // plain seconds
		{"0", time.Second},
		{"-3s", time.Second},
		{"bogus", time.Second},
	}
	for _, c := range cases {
		if got := parseRelayInterval(c.raw); got != c.want {
			t.Errorf("parseRelayInterval(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

func TestOutboxMaxAttempts(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"", postgres.DefaultOutboxMaxAttempts},
		{"0", 0},
		{"7", 7},
		{" 9 ", 9},
		{"-1", postgres.DefaultOutboxMaxAttempts},
		{"x", postgres.DefaultOutboxMaxAttempts},
	}
	for _, c := range cases {
		if got := outboxMaxAttempts(c.raw); got != c.want {
			t.Errorf("outboxMaxAttempts(%q) = %d, want %d", c.raw, got, c.want)
		}
	}
}

func TestBrokersFromEnv(t *testing.T) {
	t.Setenv("KAFKA_BROKERS", "")
	if got := brokersFromEnv(); got != nil {
		t.Errorf("brokersFromEnv() = %v, want nil", got)
	}
	t.Setenv("KAFKA_BROKERS", "b1:9092, b2:9092,,")
	want := []string{"b1:9092", "b2:9092"}
	got := brokersFromEnv()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("brokersFromEnv() = %v, want %v", got, want)
	}
}

func TestShutdownDrainDelay(t *testing.T) {
	cases := map[string]time.Duration{
		"":    DefaultShutdownDrainDelay,
		"0":   0,
		"2s":  2 * time.Second,
		"-1s": DefaultShutdownDrainDelay,
		"no":  DefaultShutdownDrainDelay,
	}
	for raw, want := range cases {
		t.Setenv("SHUTDOWN_DRAIN_DELAY", raw)
		if got := shutdownDrainDelay(); got != want {
			t.Errorf("shutdownDrainDelay(%q) = %v, want %v", raw, got, want)
		}
	}
}

func setBootEnv(t *testing.T, databaseURL, brokers string, groups bool) {
	t.Helper()
	t.Setenv("DATABASE_URL", databaseURL)
	t.Setenv("KAFKA_BROKERS", brokers)
	for _, name := range consumerGroupEnvs {
		if groups {
			t.Setenv(name, strings.ToLower(strings.TrimSuffix(name, "_CONSUMER_GROUP"))+"-itest")
		} else {
			t.Setenv(name, "")
		}
	}
}

// Boot fails naming EVERY missing mandatory variable, the three consumer
// groups included (they are read now, the consumers land later: fail-closed).
func TestLoadBootConfigNamesEveryMissingVariable(t *testing.T) {
	setBootEnv(t, "", "", false)
	_, err := loadBootConfig()
	if err == nil {
		t.Fatal("want a boot error with everything unset")
	}
	for _, want := range append([]string{"DATABASE_URL", "KAFKA_BROKERS"}, consumerGroupEnvs...) {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

// A complete environment boots; each consumer group env carries through.
func TestLoadBootConfigAcceptsACompleteEnvironment(t *testing.T) {
	setBootEnv(t, "postgres://u:x@h:5432/returns", "b:9092", true)
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "3")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "250ms")
	t.Setenv("HTTP_ADDR", "127.0.0.1:9999")
	cfg, err := loadBootConfig()
	if err != nil {
		t.Fatalf("loadBootConfig: %v", err)
	}
	if cfg.databaseURL != "postgres://u:x@h:5432/returns" || cfg.maxAttempts != 3 ||
		cfg.interval != 250*time.Millisecond || cfg.httpAddr != "127.0.0.1:9999" || len(cfg.brokers) != 1 {
		t.Fatalf("cfg = %+v", cfg)
	}
	for _, name := range consumerGroupEnvs {
		if cfg.groups[name] == "" {
			t.Errorf("cfg.groups[%s] is empty", name)
		}
	}
}

// One missing consumer group alone is a boot error naming it.
func TestLoadBootConfigFailsOnOneMissingGroup(t *testing.T) {
	setBootEnv(t, "postgres://u:x@h:5432/returns", "b:9092", true)
	t.Setenv("MANIFEST_CONSUMER_GROUP", "")
	_, err := loadBootConfig()
	if err == nil || !strings.Contains(err.Error(), "MANIFEST_CONSUMER_GROUP") {
		t.Fatalf("err = %v, want MANIFEST_CONSUMER_GROUP named", err)
	}
}
