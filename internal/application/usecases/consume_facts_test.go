package usecases_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// The three consumer use cases over the memory doubles: the claim-and-apply
// skeleton (dedupe on the CloudEvents id), the deterministic refusal of a
// payload that can never be applied, and the version gating of the
// classification copy. The Postgres side of the same behaviour is proven by
// the consumer integration tests on real Kafka + Postgres.

func bg() context.Context { return context.Background() }

func TestApplyProductClassification_AppliesAndDedupes(t *testing.T) {
	classes := memory.NewProductClasses()
	uc := &usecases.ApplyProductClassification{Processed: memory.NewProcessedEvents(), Classes: classes}
	pc := views.ProductClass{Tags: []string{"class:Standard"}, TemperatureClass: "Ambient", Version: 3}

	if out, err := uc.Handle(bg(), "evt-1", "SKU-1", pc); err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("first handle = (%q, %v), want (applied, nil)", out, err)
	}
	// The SAME id redelivered is a duplicate whatever it carries: the id,
	// not the payload, is the dedupe key.
	pc2 := pc
	pc2.Version = 9
	if out, err := uc.Handle(bg(), "evt-1", "SKU-1", pc2); err != nil || out != usecases.OutcomeDuplicate {
		t.Fatalf("redelivery = (%q, %v), want (duplicate, nil)", out, err)
	}
	if got, ok := classes.Get("SKU-1"); !ok || got.Version != 3 {
		t.Fatalf("stored classification = %+v ok=%t, want version 3 (the duplicate changed nothing)", got, ok)
	}
}

func TestApplyProductClassification_OlderVersionDoesNotOverwrite(t *testing.T) {
	classes := memory.NewProductClasses()
	uc := &usecases.ApplyProductClassification{Processed: memory.NewProcessedEvents(), Classes: classes}
	if _, err := uc.Handle(bg(), "evt-1", "SKU-1", views.ProductClass{TemperatureClass: "Frozen", Version: 4}); err != nil {
		t.Fatal(err)
	}
	// A DIFFERENT id carrying an OLDER version is a fresh event (applied,
	// claimed) whose store write is the no-op of the LOCAL COPY RULE.
	if out, err := uc.Handle(bg(), "evt-2", "SKU-1", views.ProductClass{TemperatureClass: "Ambient", Version: 3}); err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("older version = (%q, %v), want (applied, nil): the gate is the store's", out, err)
	}
	if got, ok := classes.Get("SKU-1"); !ok || got.Version != 4 || got.TemperatureClass != "Frozen" {
		t.Fatalf("stored classification = %+v ok=%t, want version 4 Frozen", got, ok)
	}
}

func TestApplyProductClassification_RefusesWhatCanNeverApply(t *testing.T) {
	uc := &usecases.ApplyProductClassification{Processed: memory.NewProcessedEvents(), Classes: memory.NewProductClasses()}
	if _, err := uc.Handle(bg(), "evt-1", " ", views.ProductClass{Version: 1}); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("blank sku = %v, want ErrInvalidEvent", err)
	}
	if _, err := uc.Handle(bg(), "evt-2", "SKU-1", views.ProductClass{Version: 0}); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("version 0 = %v, want ErrInvalidEvent", err)
	}
	if _, err := uc.Handle(bg(), "", "SKU-1", views.ProductClass{Version: 1}); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("no event id = %v, want ErrInvalidEvent", err)
	}
}

func TestApplyAllocations_AppliesEveryLineOncePerEventID(t *testing.T) {
	lines := memory.NewAllocatedLines()
	uc := &usecases.ApplyAllocations{Processed: memory.NewProcessedEvents(), Lines: lines}
	in := []usecases.AllocatedLineIn{{LineNo: 1, SKU: "SKU-1"}, {LineNo: 2, SKU: "SKU-2"}}

	if out, err := uc.Handle(bg(), "evt-1", "ord-1", in); err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("first handle = (%q, %v), want (applied, nil)", out, err)
	}
	if out, err := uc.Handle(bg(), "evt-1", "ord-1", in); err != nil || out != usecases.OutcomeDuplicate {
		t.Fatalf("redelivery = (%q, %v), want (duplicate, nil)", out, err)
	}
	for _, want := range []struct {
		lineNo int32
		sku    string
	}{{1, "SKU-1"}, {2, "SKU-2"}} {
		if line, ok := lines.Get("ord-1", want.lineNo); !ok || line.SKU != want.sku {
			t.Fatalf("ord-1 line %d = %+v ok=%t, want sku %s", want.lineNo, line, ok, want.sku)
		}
	}
}

func TestApplyAllocations_RefusesWhatCanNeverApply(t *testing.T) {
	uc := &usecases.ApplyAllocations{Processed: memory.NewProcessedEvents(), Lines: memory.NewAllocatedLines()}
	if _, err := uc.Handle(bg(), "evt-1", "ord-1", nil); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("no lines = %v, want ErrInvalidEvent", err)
	}
	if _, err := uc.Handle(bg(), "evt-2", "ord-1", []usecases.AllocatedLineIn{{LineNo: 0, SKU: "SKU-1"}}); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("line 0 = %v, want ErrInvalidEvent", err)
	}
	if _, err := uc.Handle(bg(), "evt-3", "ord-1", []usecases.AllocatedLineIn{{LineNo: 1, SKU: ""}}); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("blank sku = %v, want ErrInvalidEvent", err)
	}
	if _, err := uc.Handle(bg(), "evt-4", "", []usecases.AllocatedLineIn{{LineNo: 1, SKU: "SKU-1"}}); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("blank order = %v, want ErrInvalidEvent", err)
	}
}

func TestApplyManifest_ReversesTheWorkUnitFormula(t *testing.T) {
	units := memory.NewManifestedUnits()
	uc := &usecases.ApplyManifest{Processed: memory.NewProcessedEvents(), Units: units}
	at := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)

	if out, err := uc.Handle(bg(), "evt-1", "ord-7c9e-line-3", "pkg-1029", at); err != nil || out != usecases.OutcomeApplied {
		t.Fatalf("first handle = (%q, %v), want (applied, nil)", out, err)
	}
	if out, err := uc.Handle(bg(), "evt-1", "ord-7c9e-line-3", "pkg-1029", at); err != nil || out != usecases.OutcomeDuplicate {
		t.Fatalf("redelivery = (%q, %v), want (duplicate, nil)", out, err)
	}
	// The reversal: order_ref ord-7c9e-line-3 is the line (ord-7c9e, 3).
	if unit, ok := units.GetByOrderLine("ord-7c9e", 3); !ok || unit.PackageID != "pkg-1029" || !unit.ManifestedAt.Equal(at) {
		t.Fatalf("by order line = %+v ok=%t, want pkg-1029 at %s", unit, ok, at)
	}
	if unit, ok := units.GetByPackage("pkg-1029"); !ok || unit.PackageID != "pkg-1029" {
		t.Fatalf("by package = %+v ok=%t", unit, ok)
	}
	if _, ok := units.GetByOrderLine("ord-7c9e", 4); ok {
		t.Fatal("another line of the order must not read as manifested")
	}
}

func TestApplyManifest_AnOrderIdMayContainTheSeparator(t *testing.T) {
	units := memory.NewManifestedUnits()
	uc := &usecases.ApplyManifest{Processed: memory.NewProcessedEvents(), Units: units}
	// The split is on the LAST -line-: an order id that itself carries the
	// separator still reverses (shared.ParseWorkUnit's rule).
	if _, err := uc.Handle(bg(), "evt-1", "ord-line-2-line-7", "pkg-1", time.Now()); err != nil {
		t.Fatalf("split on the last separator: %v", err)
	}
	if _, ok := units.GetByOrderLine("ord-line-2", 7); !ok {
		t.Fatal("the reversed key (ord-line-2, 7) is not in the copy")
	}
}

func TestApplyManifest_RefusesWhatCanNeverApply(t *testing.T) {
	uc := &usecases.ApplyManifest{Processed: memory.NewProcessedEvents(), Units: memory.NewManifestedUnits()}
	at := time.Date(2026, 10, 21, 9, 30, 0, 0, time.UTC)
	if _, err := uc.Handle(bg(), "evt-1", "not-a-work-unit", "pkg-1", at); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("bad order_ref = %v, want ErrInvalidEvent", err)
	}
	if _, err := uc.Handle(bg(), "evt-2", "ord-1-line-x", "pkg-1", at); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("non-numeric line = %v, want ErrInvalidEvent", err)
	}
	if _, err := uc.Handle(bg(), "evt-3", "ord-1-line-1", "", at); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("no package id = %v, want ErrInvalidEvent", err)
	}
	if _, err := uc.Handle(bg(), "evt-4", "ord-1-line-1", "pkg-1", time.Time{}); !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("no occurred-at = %v, want ErrInvalidEvent", err)
	}
}
