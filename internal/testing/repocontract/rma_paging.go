package repocontract

import (
	"slices"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// rmaListByOrderPages walks ListByOrder page by page for several page sizes
// and requires every row exactly once, in (line, id) order — including RMAs
// that share a line (rejected ones free the line, so one line can hold many).
func rmaListByOrderPages(t *testing.T, r ports.ReturnAuthorizationRepo) {
	must(t, r.Save(bg, Authorized(t, "rma-3a", "ord-1", 3, 1)))
	must(t, r.Save(bg, Rejected(t, "rma-2b", "ord-1", 2)))
	must(t, r.Save(bg, Authorized(t, "rma-2c", "ord-1", 2, 1)))
	must(t, r.Save(bg, Rejected(t, "rma-2a", "ord-1", 2)))
	must(t, r.Save(bg, Authorized(t, "rma-1a", "ord-1", 1, 1)))
	must(t, r.Save(bg, Authorized(t, "rma-9z", "ord-2", 1, 1)))
	want := []string{"rma-1a", "rma-2a", "rma-2b", "rma-2c", "rma-3a"}
	for _, limit := range []int{1, 2, 4, 5, 6} {
		var (
			got   []string
			after *views.ReturnKey
		)
		for pages := 0; ; pages++ {
			if pages > len(want)+1 {
				t.Fatalf("limit %d: the walk never ended (got %v)", limit, got)
			}
			rows, err := r.ListByOrder(bg, "ord-1", after, limit)
			must(t, err)
			if len(rows) > limit {
				t.Fatalf("limit %d: a page holds %d rows", limit, len(rows))
			}
			for _, row := range rows {
				got = append(got, row.ID())
			}
			if len(rows) < limit {
				break
			}
			last := rows[len(rows)-1]
			after = &views.ReturnKey{LineNo: last.Line().LineNo(), ID: last.ID()}
		}
		if !slices.Equal(got, want) {
			t.Fatalf("limit %d: walked %v, want %v (no duplicate, no skip)", limit, got, want)
		}
	}
}
