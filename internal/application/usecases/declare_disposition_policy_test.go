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

func TestDeclareDispositionPolicy_Happy(t *testing.T) {
	f := newFixture(t)
	uc := &usecases.DeclareDispositionPolicy{Policies: f.policies, Clock: f.clock()}
	p, err := uc.Execute(context.Background(), usecases.PolicyDeclaration{
		Version: 3, ReturnWindowDays: 45, MaxUnitsPerLine: 2, VetoOutcome: disposition.OutcomeLiquidate,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.Version() != 3 || p.ReturnWindowDays() != 45 || p.MaxUnitsPerLine() != 2 {
		t.Fatalf("policy = v%d window=%d cap=%d", p.Version(), p.ReturnWindowDays(), p.MaxUnitsPerLine())
	}
	cur, err := f.policies.Current(context.Background())
	if err != nil || cur.Version() != 3 {
		t.Fatalf("current err=%v v=%d", err, cur.Version())
	}
}

// TestDeclareDispositionPolicy_Refusals names the domain's declaration
// errors (the table must be total, unique and five-outcome-valid).
func TestDeclareDispositionPolicy_Refusals(t *testing.T) {
	total := []disposition.Rule{
		{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
		{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
		{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
		{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
	}
	cases := []struct {
		name string
		want error
		in   usecases.PolicyDeclaration
	}{
		{
			name: "version below one",
			want: disposition.ErrInvalidPolicy,
			in:   usecases.PolicyDeclaration{Version: 0, ReturnWindowDays: 30, MaxUnitsPerLine: 5, Rules: total},
		},
		{
			name: "cap below one",
			want: disposition.ErrInvalidPolicy,
			in:   usecases.PolicyDeclaration{Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 0, Rules: total},
		},
		{
			name: "table not total",
			want: disposition.ErrPolicyNotTotal,
			in: usecases.PolicyDeclaration{Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
				Rules: total[:3]},
		},
		{
			name: "duplicate rule",
			want: disposition.ErrDuplicateRule,
			in: usecases.PolicyDeclaration{Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
				Rules: append(total, disposition.Rule{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRTV})},
		},
		{
			name: "unknown grade in a rule",
			want: disposition.ErrInvalidGrade,
			in: usecases.PolicyDeclaration{Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
				Rules: []disposition.Rule{{Grade: shared.Grade("E"), ProductClass: "*", Outcome: disposition.OutcomeDispose}}},
		},
		{
			name: "unknown veto outcome",
			want: disposition.ErrInvalidVetoOutcome,
			in: usecases.PolicyDeclaration{Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
				VetoOutcome: disposition.Outcome("BURN"), Rules: total},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			uc := &usecases.DeclareDispositionPolicy{Policies: f.policies, Clock: f.clock()}
			if _, err := uc.Execute(context.Background(), tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if cur, err := f.policies.Current(context.Background()); !errors.Is(err, ports.ErrNotFound) || cur != nil {
				t.Fatalf("refusal installed something: %v", err)
			}
		})
	}
}
