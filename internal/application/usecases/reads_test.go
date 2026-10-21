package usecases_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
)

func TestGetReturn_View(t *testing.T) {
	f := newFixture(t)
	rma := f.authorize(t, "ord-1", 2, 3)
	uc := &usecases.GetReturn{RMAs: f.rmas}
	v, err := uc.Execute(context.Background(), rma.ID())
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != rma.ID() || v.OrderID != "ord-1" || v.LineNo != 2 || v.WorkUnit != "ord-1-line-2" {
		t.Fatalf("view identity wrong: %+v", v)
	}
	if v.SKU != "sku-1" || v.Quantity != 3 || v.SiteID != "SITE1" || v.ReasonCode != "DAMAGED" {
		t.Fatalf("view facts wrong: %+v", v)
	}
	if v.Status != string(returnauthorization.StatusAuthorized) || v.Version != 1 || v.RejectionCode != "" {
		t.Fatalf("view state wrong: %+v", v)
	}
	if !v.WindowEndsAt.Equal(f.now.AddDate(0, 0, 30)) {
		t.Fatalf("view window = %v", v.WindowEndsAt)
	}
}

func TestGetReturn_NotFound(t *testing.T) {
	f := newFixture(t)
	_, err := (&usecases.GetReturn{RMAs: f.rmas}).Execute(context.Background(), "nope")
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("err = %v, want ports.ErrNotFound", err)
	}
}

func TestListReturnsByOrder_OrdersByLine(t *testing.T) {
	f := newFixture(t)
	first := f.authorize(t, "ord-1", 2, 1)
	second := f.authorize(t, "ord-1", 5, 1)
	other := f.authorize(t, "ord-2", 1, 1)
	uc := &usecases.ListReturnsByOrder{RMAs: f.rmas}
	got, err := uc.Execute(context.Background(), "ord-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != first.ID() || got[1].ID != second.ID() {
		t.Fatalf("list = %+v", got)
	}
	if got[0].LineNo != 2 || got[1].LineNo != 5 {
		t.Fatalf("list not ordered by line: %d, %d", got[0].LineNo, got[1].LineNo)
	}
	empty, err := uc.Execute(context.Background(), "ord-9")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty order: err=%v len=%d", err, len(empty))
	}
	_ = other
}

func TestGetDispositionPolicy_CurrentAndVersion(t *testing.T) {
	f := newFixture(t)
	f.declare(t) // v1: window 30, cap 5
	uc := &usecases.GetDispositionPolicy{Policies: f.policies}
	cur, err := uc.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cur.Version != 1 || cur.ReturnWindowDays != 30 || cur.MaxUnitsPerLine != 5 || cur.VetoOutcome != "DISPOSE" {
		t.Fatalf("current view = %+v", cur)
	}
	v1, err := uc.Version(context.Background(), 1)
	if err != nil || v1.Version != 1 {
		t.Fatalf("version(1) err=%v v=%d", err, v1.Version)
	}
	if _, err := uc.Version(context.Background(), 9); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing version err = %v", err)
	}
}
