package requestlog_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/requestlog"
)

func serve(t *testing.T, method, path string, status int) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestlog.RequestLogger(slog.New(slog.NewJSONHandler(&buf, nil))))
	handler := func(w http.ResponseWriter, _ *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
		}
		_, _ = w.Write([]byte("body"))
	}
	r.HandleFunc("/returns/{rmaId}", handler)
	r.HandleFunc("/healthz", handler)
	r.HandleFunc("/readyz", handler)

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, path, nil))

	if buf.Len() == 0 {
		return nil
	}
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log line is not JSON: %q (%v)", buf.String(), err)
	}
	return []map[string]any{rec}
}

func TestRequestLoggerLevelFollowsTheStatus(t *testing.T) {
	tests := []struct {
		name   string
		status int
		want   string
	}{
		{"an implicit 200 is INFO", 0, "INFO"},
		{"a 201 is INFO", http.StatusCreated, "INFO"},
		{"a 404 is WARN", http.StatusNotFound, "WARN"},
		{"a 409 is WARN", http.StatusConflict, "WARN"},
		{"a 500 is ERROR", http.StatusInternalServerError, "ERROR"},
		{"a 503 is ERROR", http.StatusServiceUnavailable, "ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := serve(t, http.MethodGet, "/returns/rma-1", tt.status)
			if len(lines) != 1 || lines[0]["level"] != tt.want {
				t.Fatalf("lines = %v, want one at %s", lines, tt.want)
			}
		})
	}
}

func TestRequestLoggerAttributes(t *testing.T) {
	lines := serve(t, http.MethodGet, "/returns/rma-1", http.StatusCreated)
	if len(lines) != 1 {
		t.Fatalf("lines = %v", lines)
	}
	line := lines[0]
	want := map[string]any{"msg": "http request", "method": "GET", "route": "/returns/{rmaId}", "status": float64(201), "bytes": float64(4)}
	for k, v := range want {
		if line[k] != v {
			t.Errorf("%s = %v, want %v", k, line[k], v)
		}
	}
	if line["request_id"] == "" || line["request_id"] == nil {
		t.Error("request_id missing")
	}
	if _, ok := line["duration_ms"]; !ok {
		t.Error("duration_ms missing")
	}
	if _, ok := line["path"]; ok {
		t.Error("the raw path must not be logged: it carries ids")
	}
}

func TestRequestLoggerNamesAnUnmatchedRoute(t *testing.T) {
	lines := serve(t, http.MethodGet, "/nothing/here/rma-123", 0)
	if len(lines) != 1 || lines[0]["route"] != "unmatched" || lines[0]["status"] != float64(404) {
		t.Fatalf("lines = %v, want route=unmatched status=404", lines)
	}
}

func TestRequestLoggerSkipsProbes(t *testing.T) {
	for _, path := range []string{"/healthz", "/readyz"} {
		if lines := serve(t, http.MethodGet, path, 0); len(lines) != 0 {
			t.Errorf("%s was logged: %v", path, lines)
		}
	}
}
