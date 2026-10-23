package http_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	inboundhttp "github.com/claudioed/returns-processing/internal/adapters/inbound/http"
	"github.com/claudioed/returns-processing/internal/analytics/report"
)

// fakeReportReader serves a fixed model: one decision, one staged line with
// a known cycle time, and a controllable failure.
type fakeReportReader struct {
	mix      []report.MixRow
	day      report.RecoveryDay
	cycles   []report.CycleDay
	lastAt   *time.Time
	mixErr   error
	dayErr   error
	cycErr   error
	freshErr error
}

func (f *fakeReportReader) DispositionMix(context.Context, report.MixQuery) ([]report.MixRow, error) {
	return f.mix, f.mixErr
}

func (f *fakeReportReader) ValueRecoveryDays(context.Context, report.SiteQuery) ([]report.RecoveryDay, error) {
	if f.dayErr != nil {
		return nil, f.dayErr
	}
	return []report.RecoveryDay{f.day}, nil
}

func (f *fakeReportReader) CycleTimeDays(context.Context, report.SiteQuery) ([]report.CycleDay, error) {
	return f.cycles, f.cycErr
}

func (f *fakeReportReader) LastEventAt(context.Context) (*time.Time, error) {
	return f.lastAt, f.freshErr
}

func fixedNow() time.Time { return time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC) }

func newReportsServer(reader report.Reader) http.Handler {
	return inboundhttp.NewReportsRouter(&inboundhttp.ReportsServer{Reader: reader, Now: fixedNow})
}

func getReport(t *testing.T, h http.Handler, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	var body map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: not JSON: %v: %s", path, err, rec.Body)
		}
	}
	return rec.Code, body
}

// The wire rows each report test expects, as plain maps.
func mixRow(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	rows, _ := body["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", body["rows"])
	}
	return rows[0].(map[string]any)
}

func recoverySite(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	days, _ := body["days"].([]any)
	if len(days) != 1 {
		t.Fatalf("days = %v", body["days"])
	}
	sites, _ := days[0].(map[string]any)["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("sites = %v", days[0])
	}
	return sites[0].(map[string]any)
}

func cycleDay(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	days, _ := body["days"].([]any)
	if len(days) != 1 || days[0].(map[string]any)["day"] != "2026-10-21" {
		t.Fatalf("days = %v", body["days"])
	}
	return days[0].(map[string]any)
}

func wantValues(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s = %v, want %v (row %v)", key, got[key], value, got)
		}
	}
}

func TestReportsRouterServesTheDocumentedReports(t *testing.T) {
	asOf := time.Date(2026, 10, 21, 11, 59, 55, 0, time.UTC)
	p50, p95 := 390.0, 390.0
	h := newReportsServer(&fakeReportReader{
		mix: []report.MixRow{{PolicyVersion: 3, Outcome: "RESTOCK", Lines: 2, Units: 5}},
		day: report.RecoveryDay{Day: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
			Sites: []report.RecoverySite{{SiteID: "SITE-1", RestockedLines: 2, RestockedUnits: 5}}},
		cycles: []report.CycleDay{{
			Day: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC), StagedLines: 2,
			CycleTime: report.Timing{Count: 1, P50: &p50, P95: &p95},
		}},
		lastAt: &asOf,
	})

	t.Run("healthz", func(t *testing.T) {
		code, body := getReport(t, h, "/healthz")
		if code != http.StatusOK || body["status"] != "ok" {
			t.Fatalf("healthz = %d %v", code, body)
		}
	})

	t.Run("disposition-mix", func(t *testing.T) {
		code, body := getReport(t, h, "/reports/disposition-mix?from=2026-10-21T00:00:00Z&to=2026-10-22T00:00:00Z")
		if code != http.StatusOK {
			t.Fatalf("mix = %d %v", code, body)
		}
		wantValues(t, mixRow(t, body), map[string]any{
			"policy_version": float64(3), "outcome": "RESTOCK", "lines": float64(2), "units": float64(5),
		})
	})

	t.Run("value-recovery", func(t *testing.T) {
		code, body := getReport(t, h, "/reports/value-recovery?from=2026-10-21T00:00:00Z&to=2026-10-22T00:00:00Z&siteId=SITE-1")
		if code != http.StatusOK {
			t.Fatalf("recovery = %d %v", code, body)
		}
		wantValues(t, recoverySite(t, body), map[string]any{
			"site_id": "SITE-1", "restocked_lines": float64(2), "restocked_units": float64(5),
		})
	})

	t.Run("return-cycle-time", func(t *testing.T) {
		code, body := getReport(t, h, "/reports/return-cycle-time?from=2026-10-21T00:00:00Z&to=2026-10-22T00:00:00Z")
		if code != http.StatusOK {
			t.Fatalf("cycle = %d %v", code, body)
		}
		lat, _ := cycleDay(t, body)["cycle_time"].(map[string]any)
		wantValues(t, lat, map[string]any{"count": float64(1), "p50_seconds": float64(390)})
	})

	t.Run("freshness", func(t *testing.T) {
		code, body := getReport(t, h, "/reports/freshness")
		wantValues(t, body, map[string]any{"as_of": "2026-10-21T11:59:55Z", "lag_seconds": float64(5)})
		if code != http.StatusOK {
			t.Fatalf("freshness = %d %v", code, body)
		}
	})
}

func TestReportsRouterRejectsBadInput(t *testing.T) {
	h := newReportsServer(&fakeReportReader{})
	for _, path := range []string{
		"/reports/disposition-mix?from=not-a-time",
		"/reports/disposition-mix?from=2026-10-21T00:00:00Z&to=2026-10-20T00:00:00Z",
		"/reports/value-recovery?granularity=hour",
		"/reports/return-cycle-time?from=not-a-time",
	} {
		code, body := getReport(t, h, path)
		if code != http.StatusBadRequest || body["type"] == nil {
			t.Fatalf("%s = %d %v, want a 400 problem", path, code, body)
		}
	}
}

func TestReportsRouterHidesStoreFailures(t *testing.T) {
	boom := errors.New("connection refused: sqlstate XX000")
	h := newReportsServer(&fakeReportReader{mixErr: boom, dayErr: boom, cycErr: boom, freshErr: boom})
	for _, path := range []string{
		"/reports/disposition-mix",
		"/reports/value-recovery",
		"/reports/return-cycle-time",
		"/reports/freshness",
	} {
		code, body := getReport(t, h, path)
		if code != http.StatusInternalServerError || body["type"] == nil || body["detail"] == boom.Error() {
			t.Fatalf("%s = %d %v, want a 500 problem that does not echo the cause", path, code, body)
		}
	}
}

// The freshness of an empty projection is null fields, not zeros.
func TestReportsRouterFreshnessOfEmptyProjection(t *testing.T) {
	code, body := getReport(t, newReportsServer(&fakeReportReader{}), "/reports/freshness")
	if code != http.StatusOK || body["as_of"] != nil || body["lag_seconds"] != nil {
		t.Fatalf("freshness = %d %v, want null fields", code, body)
	}
}
