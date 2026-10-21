package usecases_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// decidedOnce inspects line 1 of a fresh receipt under the standard policy
// (grade A -> RESTOCK) and returns the decision.
func (f fixture) decidedOnce(t *testing.T) *disposition.Disposition {
	t.Helper()
	f.declare(t)
	rc := f.inspectedReceipt(t)
	d, err := f.inspectUC().Execute(context.Background(), usecases.Inspection{
		ReceiptID: rc, LineNo: 1, Grade: shared.GradeA, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f fixture) overrideUC() *usecases.OverrideDisposition {
	return &usecases.OverrideDisposition{
		Dispositions: f.disps, Events: f.events, IDs: f.ids, Clock: f.clock(),
	}
}

func TestOverrideDisposition_Explained(t *testing.T) {
	f := newFixture(t)
	orig := f.decidedOnce(t)
	over, err := f.overrideUC().Execute(context.Background(), usecases.Override{
		DispositionID: orig.ID(), Outcome: disposition.OutcomeRTV, Reason: "vendor-recall",
	})
	if err != nil {
		t.Fatal(err)
	}
	if over.Supersedes() != orig.ID() || over.Reason() != "vendor-recall" || over.Outcome() != disposition.OutcomeRTV {
		t.Fatalf("override supersedes=%s reason=%s outcome=%s", over.Supersedes(), over.Reason(), over.Outcome())
	}
	if over.Grade() != shared.GradeA || over.PolicyVersion() != orig.PolicyVersion() {
		t.Fatalf("override lost the original's grade/policy: %s v%d", over.Grade(), over.PolicyVersion())
	}
	// the line's latest decision is the override, and an RTV stages nothing
	latest, err := f.disps.FindByReceiptLine(context.Background(), orig.ReceiptID(), 1)
	if err != nil || latest.ID() != over.ID() {
		t.Fatalf("latest err=%v id=%s", err, latest.ID())
	}
	if names := f.events.DispositionEventNames(); len(names) != 2 {
		// decidedOnce published DispositionDecided + ReturnLineStaged (grade A
		// restocks under the standard policy); the RTV override added none.
		t.Fatalf("non-restock override published extra facts: %v", names)
	}
}

// TestOverrideDisposition_RestockStages: a RESTOCK override is a staging
// instruction in its own right.
func TestOverrideDisposition_RestockStages(t *testing.T) {
	f := newFixture(t)
	orig := f.decidedOnce(t)
	over, err := f.overrideUC().Execute(context.Background(), usecases.Override{
		DispositionID: orig.ID(), Outcome: disposition.OutcomeRestock, Reason: "regrade after photos",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !over.Staged() {
		t.Fatal("restock override did not stage")
	}
	// decidedOnce published DispositionDecided + ReturnLineStaged; the
	// override adds exactly one more staging on its own disposition id.
	if names := f.events.DispositionEventNames(); len(names) != 3 || names[2] != "ReturnLineStaged" {
		t.Fatalf("events = %v, want two from the decision plus one staging", names)
	}
}

func TestOverrideDisposition_Refusals(t *testing.T) {
	cases := []struct {
		name string
		want error
		run  func(f fixture) error
	}{
		{
			name: "unknown disposition",
			want: ports.ErrNotFound,
			run: func(f fixture) error {
				f.declare(t)
				_, err := f.overrideUC().Execute(context.Background(), usecases.Override{
					DispositionID: "nope", Outcome: disposition.OutcomeRTV, Reason: "x",
				})
				return err
			},
		},
		{
			name: "reason is mandatory",
			want: disposition.ErrOverrideReasonRequired,
			run: func(f fixture) error {
				orig := f.decidedOnce(t)
				_, err := f.overrideUC().Execute(context.Background(), usecases.Override{
					DispositionID: orig.ID(), Outcome: disposition.OutcomeRTV, Reason: " ",
				})
				return err
			},
		},
		{
			name: "outcome must be one of the five",
			want: disposition.ErrInvalidPolicy,
			run: func(f fixture) error {
				orig := f.decidedOnce(t)
				_, err := f.overrideUC().Execute(context.Background(), usecases.Override{
					DispositionID: orig.ID(), Outcome: disposition.Outcome("BURN"), Reason: "x",
				})
				return err
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
