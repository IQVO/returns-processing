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

func TestLoadRelayConfigRequiresDatabaseAndBrokers(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("KAFKA_BROKERS", "")
	_, err := loadRelayConfig()
	if err == nil {
		t.Fatal("want a boot error without DATABASE_URL and KAFKA_BROKERS")
	}
	for _, want := range []string{"DATABASE_URL", "KAFKA_BROKERS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}

	t.Setenv("DATABASE_URL", "postgres://u:p@h:5432/returns")
	t.Setenv("KAFKA_BROKERS", "b:9092")
	t.Setenv("OUTBOX_MAX_ATTEMPTS", "3")
	t.Setenv("OUTBOX_RELAY_INTERVAL", "250ms")
	cfg, err := loadRelayConfig()
	if err != nil {
		t.Fatalf("loadRelayConfig: %v", err)
	}
	if cfg.databaseURL != "postgres://u:p@h:5432/returns" || cfg.maxAttempts != 3 || cfg.interval != 250*time.Millisecond {
		t.Fatalf("cfg = %+v", cfg)
	}
}
