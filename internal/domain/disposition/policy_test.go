package disposition

import (
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

func TestPolicyRulesReturnsTheTableAndDefendsItsCopy(t *testing.T) {
	p, err := Declare(PolicyInput{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		Rules: []Rule{
			{Grade: shared.GradeA, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
			{Grade: shared.GradeC, ProductClass: AnyProductClass, Outcome: OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: AnyProductClass, Outcome: OutcomeDispose},
		},
		DeclaredAt: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := p.Rules()
	if len(got) != 4 || got[0].Grade != shared.GradeA || got[3].Outcome != OutcomeDispose {
		t.Fatalf("Rules() = %+v", got)
	}
	got[0].Outcome = OutcomeRTV // mutating the copy must not touch the policy
	if again := p.Rules(); !reflect.DeepEqual(again[0], Rule{Grade: shared.GradeA, ProductClass: "*", Outcome: OutcomeRestock}) {
		t.Fatalf("the table changed through Rules(): %+v", again[0])
	}
}
