package http

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/http/page"
	"github.com/claudioed/returns-processing/internal/testing/repocontract"
)

type returnsPage struct {
	Items []struct {
		RMAID  string `json:"rmaId"`
		LineNo int    `json:"lineNo"`
	} `json:"items"`
	NextCursor string `json:"nextCursor"`
}

// seedOrder stores five RMAs of ord-page (three of them share line 2, a
// rejected RMA frees its line) and one RMA of another order.
func (f *fixture) seedOrder(t *testing.T) []string {
	t.Helper()
	ctx := t.Context()
	for _, err := range []error{
		f.rmas.Save(ctx, repocontract.Authorized(t, "rma-3a", "ord-page", 3, 1)),
		f.rmas.Save(ctx, repocontract.Rejected(t, "rma-2b", "ord-page", 2)),
		f.rmas.Save(ctx, repocontract.Authorized(t, "rma-2c", "ord-page", 2, 1)),
		f.rmas.Save(ctx, repocontract.Rejected(t, "rma-2a", "ord-page", 2)),
		f.rmas.Save(ctx, repocontract.Authorized(t, "rma-1a", "ord-page", 1, 1)),
		f.rmas.Save(ctx, repocontract.Authorized(t, "rma-9z", "ord-other", 1, 1)),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return []string{"rma-1a", "rma-2a", "rma-2b", "rma-2c", "rma-3a"}
}

// GET /returns pages by keyset: more rows than limit, every page walked, no
// duplicate and no skipped row (rows sharing line 2 included).
func TestListReturnsWalksEveryPage(t *testing.T) {
	f := newFixture(t)
	want := f.seedOrder(t)
	for _, limit := range []string{"1", "2", "5", "6", ""} {
		var got []string
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			q := url.Values{"orderRef": {"ord-page"}}
			if limit != "" {
				q.Set("limit", limit)
			}
			if cursor != "" {
				q.Set("cursor", cursor)
			}
			raw := f.mustCall(http.MethodGet, "/returns?"+q.Encode(), "", "", http.StatusOK)
			var pg returnsPage
			if err := json.Unmarshal([]byte(raw), &pg); err != nil {
				t.Fatalf("page = %s: %v", raw, err)
			}
			for _, it := range pg.Items {
				got = append(got, it.RMAID)
			}
			if cursor = pg.NextCursor; cursor == "" {
				break
			}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("limit %q: walked %v, want %v", limit, got, want)
		}
	}
}

// A bad limit is 400 invalid-limit, a bad cursor 400 invalid-cursor.
func TestListReturnsRejectsBadLimitAndCursor(t *testing.T) {
	f := newFixture(t)
	f.seedOrder(t)
	for _, limit := range []string{"0", "501", "-1", "abc", "1.5"} {
		status, raw, _ := f.call(http.MethodGet, "/returns?orderRef=ord-page&limit="+limit, "", "")
		wantProblem(t, status, raw, "invalid-limit", "")
		if status != http.StatusBadRequest {
			t.Fatalf("limit %s: status = %d", limit, status)
		}
	}
	for name, cursor := range map[string]string{
		"not base64":   "!!!",
		"not json":     "bm90LWpzb24",
		"no id":        page.Encode(page.Position{Sort: "1"}),
		"non-numeric":  page.Encode(page.Position{Sort: "x", ID: "rma-1a"}),
		"line too big": page.Encode(page.Position{Sort: "99999999999", ID: "rma-1a"}),
	} {
		t.Run(name, func(t *testing.T) {
			status, raw, _ := f.call(http.MethodGet, "/returns?orderRef=ord-page&cursor="+url.QueryEscape(cursor), "", "")
			wantProblem(t, status, raw, "invalid-cursor", "")
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d", status)
			}
		})
	}
}
