package usecases_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/testing/repocontract"
)

// Execute walks every page: an order with more RMAs than one repository page
// (rejected RMAs free their line, so a line can hold many) comes back whole,
// once each, in (line, id) order.
func TestListReturnsByOrder_ExecuteWalksEveryPage(t *testing.T) {
	rmas := memory.NewRMAs()
	const perLine, lines = 400, 3
	for line := 1; line <= lines; line++ {
		for i := range perLine {
			id := fmt.Sprintf("rma-%d-%04d", line, i)
			if err := rmas.Save(context.Background(), repocontract.Rejected(t, id, "ord-big", int32(line))); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := (&usecases.ListReturnsByOrder{RMAs: rmas}).Execute(context.Background(), "ord-big")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != perLine*lines {
		t.Fatalf("Execute returned %d views, want %d", len(got), perLine*lines)
	}
	for i, v := range got {
		want := fmt.Sprintf("rma-%d-%04d", i/perLine+1, i%perLine)
		if v.ID != want {
			t.Fatalf("view %d = %s, want %s (order or duplicate/skip)", i, v.ID, want)
		}
	}
}
