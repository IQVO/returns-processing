//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/analyticsstore"
	"github.com/claudioed/returns-processing/internal/analytics/report"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// The infrastructure comes from testcontainers, never from the environment.
func TestMain(m *testing.M) {
	// The drain step below stops the real run() with a SIGTERM delivered to
	// this very process. This handler is registered FIRST, before run()'s
	// own NotifyContext, and is never stopped: os/signal falls back to the
	// default (fatal) disposition the moment no handler remains, and the
	// test binary must survive to observe run()'s return value.
	signal.Notify(make(chan os.Signal, 1), syscall.SIGTERM)
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// The real entry point over a real analytical database: an EMPTY database is
// migrated by the repo's own analytics migrations, seeded through the
// projector's own writer (the Projection the projector binary drives), the
// documented probe answers, the documented reports answer from the seeded
// facts, serving the reports writes nothing, and a SIGTERM drains the real
// run() cleanly (ADR 0007).
func TestReportsServeTheSeededFactsAndStopCleanly(t *testing.T) {
	analyticsURL := pgtest.EmptyURL(t)
	if err := analyticsstore.RunMigrations(analyticsURL); err != nil {
		t.Fatalf("analytics migrations: %v", err)
	}
	ctx := context.Background()
	writer, err := analyticsstore.NewPool(ctx, analyticsURL)
	if err != nil {
		t.Fatalf("writer pool: %v", err)
	}
	defer writer.Close()
	projection := analyticsstore.NewProjection(writer)

	// One full return line lifecycle (the projector test's golden shapes):
	// receipt opened, inspected grade A, decided RESTOCK under policy
	// version 2, staged 6m30s (390s) after the receipt opened.
	day := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	openedAt := day.Add(10 * time.Hour)
	seed := []report.Event{
		{Kind: report.KindReturnReceiptOpened, EventID: "rep-1", At: openedAt,
			ReceiptID: "rcpt-rep-1", SiteID: "SITE-REP",
			Lines: []report.OpenedLine{{LineNo: 1, SKU: "SKU-REP", Quantity: 2, RMAID: "rma-rep-1"}}},
		{Kind: report.KindReturnLineInspected, EventID: "rep-2", At: openedAt.Add(5 * time.Minute),
			ReceiptID: "rcpt-rep-1", LineNo: 1, SKU: "SKU-REP", Quantity: 2, Grade: "A"},
		{Kind: report.KindDispositionDecided, EventID: "rep-3", At: openedAt.Add(6 * time.Minute),
			DispositionID: "disp-rep-1", ReceiptID: "rcpt-rep-1", LineNo: 1, SKU: "SKU-REP", Quantity: 2,
			Grade: "A", Outcome: "RESTOCK", PolicyVersion: 2},
		{Kind: report.KindReturnLineStaged, EventID: "rep-4", At: openedAt.Add(390 * time.Second),
			DispositionID: "disp-rep-1", ReceiptID: "rcpt-rep-1", LineNo: 1, SKU: "SKU-REP", Quantity: 2,
			RMAID: "rma-rep-1", SiteID: "SITE-REP"},
	}
	for _, e := range seed {
		applied, err := projection.Apply(ctx, e)
		if err != nil {
			t.Fatalf("seed %s: %v", e.EventID, err)
		}
		if !applied {
			t.Fatalf("seed %s was not applied (a fresh database must accept it)", e.EventID)
		}
	}

	// A free port for the real server (never the default :8092, so parallel
	// runs and a locally running binary never collide).
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	t.Setenv("ANALYTICS_DATABASE_URL", analyticsURL)
	t.Setenv("HTTP_ADDR", addr)
	t.Setenv("LOG_LEVEL", "error")
	done := make(chan error, 1)
	go func() { done <- run() }()

	// The documented probe: /healthz answers on the reports listener.
	base := "http://" + addr
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if code, _ := getJSON(t, base+"/healthz"); code == http.StatusOK {
			break
		}
	}
	if code, _ := getJSON(t, base+"/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz never answered 200 on %s", addr)
	}

	from := day.Format(time.RFC3339)
	oneDay := day.Add(24 * time.Hour).Format(time.RFC3339)
	twoDays := day.Add(48 * time.Hour).Format(time.RFC3339)

	// Disposition mix: one RESTOCK cell under policy version 2, 1 line,
	// 2 units — the decision counts in its own day (ADR 0007 rules).
	code, mix := getJSON(t, base+"/reports/disposition-mix?from="+from+"&to="+oneDay)
	rows, _ := mix["rows"].([]any)
	if code != http.StatusOK || len(rows) != 1 {
		t.Fatalf("disposition-mix = %d %v", code, mix)
	}
	row := rows[0].(map[string]any)
	if row["policy_version"] != float64(2) || row["outcome"] != "RESTOCK" ||
		row["lines"] != float64(1) || row["units"] != float64(2) {
		t.Fatalf("disposition-mix row = %v", row)
	}

	// Value recovery over a two-day window: days[] is dense (one row per UTC
	// day intersecting [from, to), zeros included), the staged line counts in
	// its own day at its site.
	code, recovery := getJSON(t, base+"/reports/value-recovery?from="+from+"&to="+twoDays)
	days, _ := recovery["days"].([]any)
	if code != http.StatusOK || len(days) != 2 {
		t.Fatalf("value-recovery = %d %v", code, recovery)
	}
	sites, _ := days[0].(map[string]any)["sites"].([]any)
	if len(sites) != 1 {
		t.Fatalf("value-recovery day 0 sites = %v", days[0])
	}
	site := sites[0].(map[string]any)
	if site["site_id"] != "SITE-REP" || site["restocked_lines"] != float64(1) || site["restocked_units"] != float64(2) {
		t.Fatalf("value-recovery site = %v", site)
	}
	// The dense zero day: day 1 carries no activity, and its zeros are
	// included (ADR 0007: "one row per UTC calendar day intersecting
	// [from, to), zeros included") — the site's row reads 0/0.
	zeroSites, _ := days[1].(map[string]any)["sites"].([]any)
	if len(zeroSites) != 1 {
		t.Fatalf("value-recovery day 1 sites = %v, want the site's zero row", days[1])
	}
	zero := zeroSites[0].(map[string]any)
	if zero["site_id"] != "SITE-REP" || zero["restocked_lines"] != float64(0) || zero["restocked_units"] != float64(0) {
		t.Fatalf("value-recovery day 1 site = %v, want 0/0", zero)
	}

	// Return cycle time: the staged line's receipt-to-staged latency is
	// 390s, so p50 is 390 (percentile_cont over one sample).
	code, cycle := getJSON(t, base+"/reports/return-cycle-time?from="+from+"&to="+oneDay)
	days, _ = cycle["days"].([]any)
	if code != http.StatusOK || len(days) != 1 {
		t.Fatalf("return-cycle-time = %d %v", code, cycle)
	}
	latency, _ := days[0].(map[string]any)["cycle_time"].(map[string]any)
	if days[0].(map[string]any)["staged_lines"] != float64(1) ||
		latency["count"] != float64(1) || latency["p50_seconds"] != float64(390) {
		t.Fatalf("return-cycle-time day = %v", days[0])
	}

	// Freshness: as_of is the newest applied CloudEvents time (the staged
	// event) and the lag is never negative.
	code, fresh := getJSON(t, base+"/reports/freshness")
	if code != http.StatusOK || fresh["as_of"] != openedAt.Add(390*time.Second).Format(time.RFC3339) {
		t.Fatalf("freshness = %d %v", code, fresh)
	}
	if lag, ok := fresh["lag_seconds"].(float64); !ok || lag < 0 {
		t.Fatalf("lag_seconds = %v, want a non-negative number", fresh["lag_seconds"])
	}

	// The documented range and granularity rules are RFC 7807 problems (the
	// type is the fleet error namespace URL ending in the problem slug).
	code, problem := getJSON(t, base+"/reports/disposition-mix?from="+oneDay+"&to="+from)
	if code != http.StatusBadRequest || !strings.HasSuffix(problem["type"].(string), "invalid-report-range") {
		t.Fatalf("inverted range = %d %v", code, problem)
	}
	code, problem = getJSON(t, base+"/reports/value-recovery?from="+from+"&to="+oneDay+"&granularity=hour")
	if code != http.StatusBadRequest || !strings.HasSuffix(problem["type"].(string), "invalid-report-granularity") {
		t.Fatalf("granularity=hour = %d %v", code, problem)
	}

	// The binary only reads: serving every report left the facts untouched.
	if n := pgtest.Count(t, writer, "return_line_facts"); n != 1 {
		t.Fatalf("return_line_facts = %d rows after serving the reports, want 1", n)
	}
	if n := pgtest.Count(t, writer, "disposition_facts"); n != 1 {
		t.Fatalf("disposition_facts = %d rows after serving the reports, want 1", n)
	}

	// Clean drain: run() takes no context, it stops on SIGTERM (its own
	// signal.NotifyContext) and must return nil after srv.Shutdown.
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a SIGTERM'd reports server must drain cleanly, got %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the reports server did not stop")
	}
}

func getJSON(t *testing.T, url string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}
