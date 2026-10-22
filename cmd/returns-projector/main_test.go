package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// Every missing mandatory variable is named, all together.
func TestLoadConfigNamesEveryMissingVariable(t *testing.T) {
	_, err := loadConfig(func(string) string { return "" })
	for _, want := range []string{"ANALYTICS_DATABASE_URL", "KAFKA_BROKERS", "ANALYTICS_CONSUMER_GROUP"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want it to name %s", err, want)
		}
	}
	cfg, err := loadConfig(func(k string) string {
		switch k {
		case "ANALYTICS_DATABASE_URL":
			return "postgres://u@h/analytics"
		case "KAFKA_BROKERS":
			return " kafka-1:9092 , kafka-2:9092 "
		case "ANALYTICS_CONSUMER_GROUP":
			return "returns-processing-analytics"
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if len(cfg.brokers) != 2 || cfg.brokers[0] != "kafka-1:9092" {
		t.Fatalf("brokers = %v", cfg.brokers)
	}
	if cfg.adminAddr != ":8091" || cfg.group != "returns-processing-analytics" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

// The admin mux answers liveness always and readiness until flipped.
func TestAdminMuxFlipsReadiness(t *testing.T) {
	var notReady atomic.Bool
	mux := newAdminMux(&notReady)
	if code := muxResult(mux, "/healthz"); code != http.StatusOK {
		t.Fatalf("healthz = %d", code)
	}
	if code := muxResult(mux, "/readyz"); code != http.StatusOK {
		t.Fatalf("readyz before flip = %d", code)
	}
	notReady.Store(true)
	if code := muxResult(mux, "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("readyz after flip = %d", code)
	}
	if code := muxResult(mux, "/healthz"); code != http.StatusOK {
		t.Fatalf("healthz after flip = %d, liveness never flips", code)
	}
}

func muxResult(mux *http.ServeMux, path string) int {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec.Code
}

// A consumer error or an admin failure is an error; a signal is the only
// clean stop.
func TestAwaitStop(t *testing.T) {
	consumerDone := make(chan error, 1)
	adminErr := make(chan error, 1)
	signalled := make(chan struct{})
	consumerDone <- errors.New("reader failed")
	if err := awaitStop(signalled, consumerDone, adminErr); err == nil {
		t.Fatal("a consumer return must be an error")
	}
	boom := errors.New("listen failed")
	adminErr <- boom
	if err := awaitStop(signalled, consumerDone, adminErr); !errors.Is(err, boom) {
		t.Fatalf("admin error = %v", err)
	}
	close(signalled)
	if err := awaitStop(signalled, consumerDone, adminErr); err != nil {
		t.Fatalf("a signal must be a clean stop, got %v", err)
	}
}
