package usecases_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

func TestOpenReturnReceipt_Happy(t *testing.T) {
	f := newFixture(t)
	r := f.openReceipt(t,
		f.matchedLine(1, 3, "rma-1"),
		usecases.ReceiptLine{LineNo: 2, SKU: "sku-9", Quantity: 1},
	)
	if r.Version() != 1 || r.SiteID() != "SITE1" || r.ID() != "rcpt-0001" {
		t.Fatalf("receipt id=%s v=%d site=%s", r.ID(), r.Version(), r.SiteID())
	}
	if lines := r.Lines(); len(lines) != 2 ||
		lines[0].Kind() != returnreceipt.KindMatched || lines[0].RMAID() != "rma-1" ||
		lines[1].Kind() != returnreceipt.KindUnexpected || lines[1].RMAID() != "" {
		t.Fatalf("lines = %+v", lines)
	}
	want := []string{"ReturnReceiptOpened", "UnexpectedReturnReceived"}
	if names := f.events.ReceiptEventNames(); len(names) != 2 || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("events = %v, want %v", names, want)
	}
	if _, err := f.receipts.FindByID(context.Background(), r.ID()); err != nil {
		t.Fatalf("not persisted: %v", err)
	}
}

// TestOpenReturnReceipt_Refusals names the domain errors of the open path:
// the input's rma-id presence is the line-kind discriminator, so the
// third state is inexpressible and the remaining failures are validation.
func TestOpenReturnReceipt_Refusals(t *testing.T) {
	cases := []struct {
		name  string
		want  error
		lines []usecases.ReceiptLine
	}{
		{
			name:  "no lines",
			want:  returnreceipt.ErrNoLines,
			lines: nil,
		},
		{
			name:  "duplicate line numbers",
			want:  returnreceipt.ErrDuplicateLineNo,
			lines: []usecases.ReceiptLine{f_line(1), f_line(1)},
		},
		{
			name:  "quantity below one",
			want:  shared.ErrQuantityBelowOne,
			lines: []usecases.ReceiptLine{{LineNo: 1, SKU: "sku-1", Quantity: 0}},
		},
		{
			name:  "blank sku",
			want:  shared.ErrBlankID,
			lines: []usecases.ReceiptLine{{LineNo: 1, SKU: " ", Quantity: 1}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			uc := &usecases.OpenReturnReceipt{Receipts: f.receipts, Events: f.events, IDs: f.ids, Clock: f.clock()}
			_, err := uc.Execute(context.Background(), usecases.ReturnReceiptInput{SiteID: "SITE1", Lines: tc.lines})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if names := f.events.ReceiptEventNames(); len(names) != 0 {
				t.Fatalf("refusal published %v", names)
			}
		})
	}
}

func f_line(no int32) usecases.ReceiptLine {
	return usecases.ReceiptLine{LineNo: no, SKU: "sku-1", Quantity: 1, RMAID: "rma-1"}
}

// inspectedReceipt opens a receipt (one matched line of 3) and returns the
// receipt id.
func (f fixture) inspectedReceipt(t *testing.T) string {
	t.Helper()
	r := f.openReceipt(t, f.matchedLine(1, 3, "rma-1"))
	return r.ID()
}

func (f fixture) inspectUC() *usecases.InspectReturnLine {
	return &usecases.InspectReturnLine{
		Receipts: f.receipts, Dispositions: f.disps, Policies: f.policies,
		Products: f.products, Events: f.events, IDs: f.ids, Clock: f.clock(),
	}
}

func (f fixture) inspect(t *testing.T, receiptID string, lineNo int32, grade shared.Grade, qty int64) error {
	t.Helper()
	_, err := f.inspectUC().Execute(context.Background(), usecases.Inspection{
		ReceiptID: receiptID, LineNo: lineNo, Grade: grade, Quantity: qty, ReasonCodes: []string{"torn"},
	})
	return err
}

func TestInspectReturnLine_RestockDecision(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	rc := f.inspectedReceipt(t)
	f.products.Put("sku-1", productClass([]string{"class:Standard"}))
	d, err := f.inspectUC().Execute(context.Background(), usecases.Inspection{
		ReceiptID: rc, LineNo: 1, Grade: shared.GradeA, Quantity: 2, ReasonCodes: []string{"torn"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Outcome() != "RESTOCK" || d.PolicyVersion() != 1 || d.RMAID() != "rma-1" {
		t.Fatalf("decision outcome=%s policyV=%d rma=%s", d.Outcome(), d.PolicyVersion(), d.RMAID())
	}
	if d.ReceiptID() != rc || d.LineNo() != 1 || d.SKU() != "sku-1" || d.Quantity() != 2 || !d.Staged() {
		t.Fatalf("decision facts wrong: %+v", d)
	}
}

// TestInspectReturnLine_RestockPublishesAndPersists covers the event set and
// persistence of the RESTOCK decision (split from the decision test to keep
// each function under the complexity gate).
func TestInspectReturnLine_RestockPublishesAndPersists(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	rc := f.inspectedReceipt(t)
	f.products.Put("sku-1", productClass([]string{"class:Standard"}))
	if _, err := f.inspectUC().Execute(context.Background(), usecases.Inspection{
		ReceiptID: rc, LineNo: 1, Grade: shared.GradeA, Quantity: 2, ReasonCodes: []string{"torn"},
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"DispositionDecided", "ReturnLineStaged"}
	if names := f.events.DispositionEventNames(); len(names) != 2 || names[0] != want[0] || names[1] != want[1] {
		t.Fatalf("disposition events = %v, want %v", names, want)
	}
	wantReceipt := []string{"ReturnReceiptOpened", "ReturnLineInspected"}
	if names := f.events.ReceiptEventNames(); len(names) != 2 || names[0] != wantReceipt[0] || names[1] != wantReceipt[1] {
		t.Fatalf("receipt events = %v, want %v (the open plus this inspection, nothing replayed)", names, wantReceipt)
	}
	stored, err := f.receipts.FindByID(context.Background(), rc)
	if err != nil || stored.Version() != 2 {
		t.Fatalf("receipt v=%d err=%v", stored.Version(), err)
	}
	if _, err := f.disps.FindByReceiptLine(context.Background(), rc, 1); err != nil {
		t.Fatalf("decision not persisted by receipt line: %v", err)
	}
}

// TestInspectReturnLine_GuardrailsAndDefaults walks the classification
// derivations the tag grammar feeds. The restockAll rows run against a
// table that restocks every grade, so only a guardrail demotion can turn
// the outcome away from RESTOCK.
func TestInspectReturnLine_GuardrailsAndDefaults(t *testing.T) {
	cases := []struct {
		name       string
		tags       []string
		grade      shared.Grade
		restockAll bool
		outcome    string
	}{
		{name: "unclassified sku fails open to the default", grade: shared.GradeA, outcome: "RESTOCK"},
		{name: "grade below B demotes restock to the veto", grade: shared.GradeC, restockAll: true, outcome: "DISPOSE"},
		{name: "hazmat without dot rating is vetoed", tags: []string{"hazmat"}, grade: shared.GradeA, restockAll: true, outcome: "DISPOSE"},
		{name: "hazmat with dot rating restocks", tags: []string{"hazmat", "dot:3"}, grade: shared.GradeA, outcome: "RESTOCK"},
		{name: "forced outcome wins outright", tags: []string{"force:RTV"}, grade: shared.GradeA, outcome: "RTV"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if tc.restockAll {
				f.declareRestockAll(t)
			} else {
				f.declare(t)
			}
			rc := f.inspectedReceipt(t)
			if tc.tags != nil {
				f.products.Put("sku-1", productClass(tc.tags))
			}
			d, err := f.inspectUC().Execute(context.Background(), usecases.Inspection{
				ReceiptID: rc, LineNo: 1, Grade: tc.grade, Quantity: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if string(d.Outcome()) != tc.outcome {
				t.Fatalf("outcome = %s, want %s", d.Outcome(), tc.outcome)
			}
		})
	}
}

// TestInspectReturnLine_NoReplayAcrossLines pins the Save-consumes-events
// contract at the use-case level: two inspections publish exactly two
// ReturnLineInspected facts — no ReturnReceiptOpened replay.
func TestInspectReturnLine_NoReplayAcrossLines(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	r := f.openReceipt(t, f.matchedLine(1, 2, "rma-1"), f.matchedLine(2, 2, "rma-2"))
	if err := f.inspect(t, r.ID(), 1, shared.GradeA, 1); err != nil {
		t.Fatal(err)
	}
	if err := f.inspect(t, r.ID(), 2, shared.GradeB, 1); err != nil {
		t.Fatal(err)
	}
	names := f.events.ReceiptEventNames()
	want := []string{"ReturnReceiptOpened", "ReturnLineInspected", "ReturnLineInspected"}
	if len(names) != 3 || names[0] != want[0] || names[1] != want[1] || names[2] != want[2] {
		t.Fatalf("receipt events = %v, want %v (one open, two inspections, nothing replayed)", names, want)
	}
}

func TestInspectReturnLine_Refusals(t *testing.T) {
	cases := []struct {
		name string
		want error
		run  func(f fixture) error
	}{
		{
			name: "unknown receipt",
			want: ports.ErrNotFound,
			run: func(f fixture) error {
				f.declare(t)
				return f.inspect(t, "nope", 1, shared.GradeA, 1)
			},
		},
		{
			name: "no policy declared",
			want: ports.ErrNotFound,
			run: func(f fixture) error {
				return f.inspect(t, f.inspectedReceipt(t), 1, shared.GradeA, 1)
			},
		},
		{
			name: "no such line",
			want: returnreceipt.ErrLineNotFound,
			run: func(f fixture) error {
				f.declare(t)
				return f.inspect(t, f.inspectedReceipt(t), 9, shared.GradeA, 1)
			},
		},
		{
			name: "line inspected exactly once",
			want: returnreceipt.ErrLineAlreadyInspected,
			run: func(f fixture) error {
				f.declare(t)
				rc := f.inspectedReceipt(t)
				if err := f.inspect(t, rc, 1, shared.GradeA, 1); err != nil {
					t.Fatal(err)
				}
				return f.inspect(t, rc, 1, shared.GradeA, 1)
			},
		},
		{
			name: "inspected over received",
			want: returnreceipt.ErrInspectedExceedsReceived,
			run: func(f fixture) error {
				f.declare(t)
				return f.inspect(t, f.inspectedReceipt(t), 1, shared.GradeA, 4)
			},
		},
		{
			name: "invalid grade",
			want: returnreceipt.ErrInvalidGrade,
			run: func(f fixture) error {
				f.declare(t)
				return f.inspect(t, f.inspectedReceipt(t), 1, shared.Grade("X"), 1)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			if err := tc.run(f); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
