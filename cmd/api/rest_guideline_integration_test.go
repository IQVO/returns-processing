//go:build integration

// The REST guideline on the real entry point, real Postgres and real Kafka
// consumers: noun routes keyed and replayed, key reuse is 409, the retired
// verb routes are gone, and GET /returns pages by keyset over Postgres.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/testing/kafkatest"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// doH performs a request and returns status, body and headers; key is sent as
// Idempotency-Key when non-empty.
func (s *service) doH(method, path, body, key string) (int, string, http.Header) {
	s.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, s.base+path, strings.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw), resp.Header
}

// mustH asserts the status and returns body and headers.
func (s *service) mustH(method, path, body, key string, want int) (string, http.Header) {
	s.t.Helper()
	status, raw, hdr := s.doH(method, path, body, key)
	if status != want {
		s.t.Fatalf("%s %s = %d %s, want %d", method, path, status, raw, want)
	}
	return raw, hdr
}

// field digs one top-level string out of a JSON body.
func field(t *testing.T, body, name string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %s", body)
	}
	v, _ := m[name].(string)
	if v == "" {
		t.Fatalf("%q missing in %s", name, body)
	}
	return v
}

// ensureTopic creates topic unless an earlier test of this package did.
func ensureTopic(t *testing.T, brokers []string, topic string) {
	t.Helper()
	if !topicExists(brokers, topic) {
		kafkatest.CreateTopic(t, topic)
	}
}

func TestRESTGuidelineOnRealPostgres(t *testing.T) {
	brokers := kafkatest.Brokers(t)
	e := newEnv(t, strings.Join(brokers, ","))
	pool := pgtest.Open(t, e.dbURL)
	s := e.start(t)
	for _, topic := range []string{topicEvents, topicAnalytics, topicOrderMgmt, topicFulfill} {
		ensureTopic(t, brokers, topic)
	}

	const table = `{"returnWindowDays":30,"maxUnitsPerLine":5,"vetoOutcome":"DISPOSE","rules":[` +
		`{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"RESTOCK"},` +
		`{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}`
	s.mustH(http.MethodPut, "/disposition-policies/1", table, "rg-policy", http.StatusCreated)

	// Three allocated + manifested lines of one order: three RMAs to page over.
	lines := []map[string]any{}
	for n := 1; n <= 3; n++ {
		lines = append(lines, map[string]any{"line_no": n, "sku": "SKU-" + strconv.Itoa(n), "path_id": "pick"})
	}
	kafkatest.Produce(t, topicOrderMgmt, cloudEvent(t, "alloc-ord-pg", "/warehouse/order-management", typeOrderAllocated, "ord-pg",
		map[string]any{"order_id": "ord-pg", "promise_date": "2026-11-21T10:01:00Z", "lines": lines}))
	for n := 1; n <= 3; n++ {
		unit := "ord-pg-line-" + strconv.Itoa(n)
		kafkatest.Produce(t, topicFulfill, cloudEvent(t, "manifest-"+unit, "/warehouse/fulfillment-execution", typePackageManifested, unit,
			map[string]any{"package_id": "pkg-" + unit, "order_ref": unit}))
	}
	waitFor(t, 90*time.Second, "the consumers to feed the local copies", func() bool {
		return pgtest.Count(t, pool, "allocated_lines") == 3 && pgtest.Count(t, pool, "manifested_units") == 3
	})

	var rmaIDs []string
	for n := 1; n <= 3; n++ {
		body := `{"orderId":"ord-pg","lineNo":` + strconv.Itoa(n) + `,"sku":"SKU-` + strconv.Itoa(n) + `","quantity":1,"siteId":"WH1","reasonCode":"DAMAGED"}`
		raw, _ := s.mustH(http.MethodPost, "/returns", body, "rg-auth-"+strconv.Itoa(n), http.StatusCreated)
		rmaIDs = append(rmaIDs, field(t, raw, "rmaId"))
	}

	t.Run("a replay is byte-identical and marked; reuse across body and route is 409", func(t *testing.T) {
		const body = `{"orderId":"ord-pg","lineNo":1,"sku":"SKU-1","quantity":1,"siteId":"WH1","reasonCode":"DAMAGED"}`
		replayed, firstHdr := s.mustH(http.MethodPost, "/returns", body, "rg-auth-1", http.StatusCreated)
		if firstHdr.Get("Idempotency-Replayed") != "true" || !strings.Contains(replayed, rmaIDs[0]) {
			t.Fatalf("replay of the first authorize = %s %v, want the stored RMA marked Idempotency-Replayed: true", replayed, firstHdr)
		}
		status, raw, _ := s.doH(http.MethodPost, "/returns", strings.Replace(body, `"quantity":1`, `"quantity":2`, 1), "rg-auth-1")
		if status != http.StatusConflict || !strings.Contains(raw, "idempotency-key-reused") {
			t.Fatalf("same key, other body = %d %s, want 409 idempotency-key-reused", status, raw)
		}
		status, raw, _ = s.doH(http.MethodPost, "/returns/"+rmaIDs[0]+"/receipts", "{}", "rg-auth-1")
		if status != http.StatusConflict || !strings.Contains(raw, "idempotency-key-reused") {
			t.Fatalf("same key on another route = %d %s, want 409 idempotency-key-reused", status, raw)
		}
	})

	t.Run("GET /returns walks every page over Postgres", func(t *testing.T) {
		var got []string
		cursor := ""
		for pages := 0; pages < 5; pages++ {
			path := "/returns?orderRef=ord-pg&limit=2"
			if cursor != "" {
				path += "&cursor=" + cursor
			}
			raw, _ := s.mustH(http.MethodGet, path, "", "", http.StatusOK)
			var pg struct {
				Items []struct {
					RMAID string `json:"rmaId"`
				} `json:"items"`
				NextCursor string `json:"nextCursor"`
			}
			if err := json.Unmarshal([]byte(raw), &pg); err != nil {
				t.Fatalf("page = %s", raw)
			}
			for _, it := range pg.Items {
				got = append(got, it.RMAID)
			}
			if cursor = pg.NextCursor; cursor == "" {
				break
			}
		}
		if !slices.Equal(got, rmaIDs) {
			t.Fatalf("walked %v, want %v (by line, no duplicate, no skip)", got, rmaIDs)
		}
		if status, raw, _ := s.doH(http.MethodGet, "/returns?orderRef=ord-pg&cursor=%21%21", "", ""); status != http.StatusBadRequest || !strings.Contains(raw, "invalid-cursor") {
			t.Fatalf("bad cursor = %d %s", status, raw)
		}
		if status, raw, _ := s.doH(http.MethodGet, "/returns?orderRef=ord-pg&limit=501", "", ""); status != http.StatusBadRequest || !strings.Contains(raw, "invalid-limit") {
			t.Fatalf("bad limit = %d %s", status, raw)
		}
	})

	t.Run("the noun routes are keyed, replay, and the verb routes are gone", func(t *testing.T) {
		receiptBody := `{"siteId":"WH1","lines":[{"lineNo":1,"sku":"SKU-1","quantity":2,"kind":"MATCHED","rmaId":"` + rmaIDs[0] + `"}]}`
		raw, _ := s.mustH(http.MethodPost, "/returns/"+rmaIDs[0]+"/receipts", receiptBody, "rg-receipt", http.StatusCreated)
		receipt := field(t, raw, "receiptId")

		inspections := "/receipts/" + receipt + "/lines/1/inspections"
		if status, raw, _ := s.doH(http.MethodPost, inspections, `{"grade":"A","quantity":2,"reasonCodes":["OPENED"]}`, ""); status != http.StatusBadRequest || !strings.Contains(raw, "idempotency-key-required") {
			t.Fatalf("inspections without a key = %d %s", status, raw)
		}
		first, firstHdr := s.mustH(http.MethodPost, inspections, `{"grade":"A","quantity":2,"reasonCodes":["OPENED"]}`, "rg-inspect", http.StatusOK)
		if firstHdr.Get("Idempotency-Replayed") != "" {
			t.Fatalf("first inspection carries Idempotency-Replayed: %v", firstHdr)
		}
		again, hdr := s.mustH(http.MethodPost, inspections, `{"grade":"A","quantity":2,"reasonCodes":["OPENED"]}`, "rg-inspect", http.StatusOK)
		if again != first || hdr.Get("Idempotency-Replayed") != "true" {
			t.Fatalf("inspection replay = %s %v, want byte-identical and marked", again, hdr)
		}
		var result struct {
			Disposition struct {
				DispositionID string `json:"dispositionId"`
			} `json:"disposition"`
		}
		if err := json.Unmarshal([]byte(first), &result); err != nil || result.Disposition.DispositionID == "" {
			t.Fatalf("inspection = %s", first)
		}

		overrides := "/dispositions/" + result.Disposition.DispositionID + "/overrides"
		if status, raw, _ := s.doH(http.MethodPost, overrides, `{"outcome":"RTV","reason":"vendor accepts"}`, ""); status != http.StatusBadRequest || !strings.Contains(raw, "idempotency-key-required") {
			t.Fatalf("overrides without a key = %d %s", status, raw)
		}
		first, firstHdr = s.mustH(http.MethodPost, overrides, `{"outcome":"RTV","reason":"vendor accepts"}`, "rg-override", http.StatusOK)
		again, hdr = s.mustH(http.MethodPost, overrides, `{"outcome":"RTV","reason":"vendor accepts"}`, "rg-override", http.StatusOK)
		if firstHdr.Get("Idempotency-Replayed") != "" || again != first || hdr.Get("Idempotency-Replayed") != "true" {
			t.Fatalf("override replay = %s %v (first %v)", again, hdr, firstHdr)
		}

		// One logical submit, one stored answer; the replay added nothing.
		for _, key := range []string{"rg-inspect", "rg-override"} {
			var n int
			pgtest.Scan(t, pool, `SELECT count(*) FROM idempotency_keys WHERE key = $1`, []any{key}, &n)
			if n != 1 {
				t.Fatalf("%s rows = %d, want 1", key, n)
			}
		}
		var dispositions int
		pgtest.Scan(t, pool, `SELECT count(*) FROM dispositions`, nil, &dispositions)
		if dispositions != 2 {
			t.Fatalf("dispositions = %d, want 2 (the decision and its one override)", dispositions)
		}

		var before int
		pgtest.Scan(t, pool, `SELECT count(*) FROM idempotency_keys`, nil, &before)
		for _, old := range []string{
			"/receipts/" + receipt + "/lines/1/inspect",
			"/dispositions/" + result.Disposition.DispositionID + "/override",
		} {
			if status, raw, _ := s.doH(http.MethodPost, old, `{}`, "rg-retired"); status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
				t.Fatalf("POST %s = %d %s, want 404 or 405", old, status, raw)
			}
		}
		var after int
		pgtest.Scan(t, pool, `SELECT count(*) FROM idempotency_keys`, nil, &after)
		if after != before {
			t.Fatalf("idempotency rows %d -> %d: a retired route reached the middleware", before, after)
		}
	})
	s.stop()
}
