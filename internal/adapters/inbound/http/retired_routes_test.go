package http

import (
	"net/http"
	"testing"
)

// TestRetiredRoutesAreGone: the verb-style routes of the REST guideline
// cutover answer 404 or 405 (no alias survives).
func TestRetiredRoutesAreGone(t *testing.T) {
	f := newFixture(t)
	retired := []struct{ method, path string }{
		{http.MethodPost, "/receipts/rcpt-1/lines/1/inspect"},
		{http.MethodPost, "/dispositions/disp-1/override"},
	}
	for _, route := range retired {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			status, raw, _ := f.call(route.method, route.path, `{}`, "k-retired")
			if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s = %d %s, want 404 or 405", route.method, route.path, status, raw)
			}
		})
	}
}
