package repocontract

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
)

// DispositionRepo runs the DispositionRepo contract. newRepo must return an
// EMPTY repository on every call.
func DispositionRepo(t *testing.T, newRepo func(t *testing.T) ports.DispositionRepo) {
	t.Helper()
	run(t, newRepo, map[string]func(t *testing.T, r ports.DispositionRepo){
		"an unknown disposition is not found":        dispositionNotFound,
		"save then find round trips a decision":      dispositionRoundTrip,
		"the line lookup returns the latest":         dispositionLatestPerLine,
		"the same id cannot be saved twice":          dispositionImmutable,
		"a second policy decision per line refuses":  dispositionOnePerLine,
		"a read-back decision has no pending events": dispositionNoPendingEvents,
	})
}

func dispositionNotFound(t *testing.T, r ports.DispositionRepo) {
	_, err := r.FindByID(bg, "disp-404")
	wantErr(t, err, ports.ErrNotFound)
	_, err = r.FindByReceiptLine(bg, "rc-1", 1)
	wantErr(t, err, ports.ErrNotFound)
}

func dispositionRoundTrip(t *testing.T, r ports.DispositionRepo) {
	in := Decided(t, "disp-1", "rc-1", 1)
	must(t, r.Save(bg, in))
	got, err := r.FindByID(bg, "disp-1")
	must(t, err)
	if got.Outcome() != disposition.OutcomeRestock || !got.Staged() {
		t.Fatalf("state = %s staged=%t", got.Outcome(), got.Staged())
	}
	if !reflect.DeepEqual(got.Snapshot(), in.Snapshot()) {
		t.Fatalf("round trip changed the state:\ngot  %+v\nwant %+v", got.Snapshot(), in.Snapshot())
	}
}

func dispositionLatestPerLine(t *testing.T, r ports.DispositionRepo) {
	orig := Decided(t, "disp-1", "rc-1", 1)
	must(t, r.Save(bg, orig))
	over, err := orig.Override(disposition.OverrideInput{
		NewDispositionID: "disp-2", Outcome: disposition.OutcomeRTV,
		Reason: "vendor-recall", DecidedAt: T0.Add(2 * time.Hour),
	})
	must(t, err)
	must(t, r.Save(bg, over))
	latest, err := r.FindByReceiptLine(bg, "rc-1", 1)
	must(t, err)
	if latest.ID() != "disp-2" || latest.Supersedes() != "disp-1" || latest.Reason() != "vendor-recall" {
		t.Fatalf("latest = %+v", latest.Snapshot())
	}
	// the original is still there by id
	old, err := r.FindByID(bg, "disp-1")
	must(t, err)
	if old.Supersedes() != "" {
		t.Fatalf("the original was mutated: %+v", old.Snapshot())
	}
}

func dispositionImmutable(t *testing.T, r ports.DispositionRepo) {
	in := Decided(t, "disp-1", "rc-1", 1)
	must(t, r.Save(bg, in))
	wantErr(t, r.Save(bg, in), ports.ErrConcurrentModification)
}

func dispositionOnePerLine(t *testing.T, r ports.DispositionRepo) {
	must(t, r.Save(bg, Decided(t, "disp-1", "rc-1", 1)))
	// a second POLICY decision for the same line is refused (an override
	// supersedes and is exempt)
	wantErr(t, r.Save(bg, Decided(t, "disp-2", "rc-1", 1)), ports.ErrConcurrentModification)
	// another line of the same receipt is fine
	must(t, r.Save(bg, Decided(t, "disp-3", "rc-1", 2)))
}

func dispositionNoPendingEvents(t *testing.T, r ports.DispositionRepo) {
	must(t, r.Save(bg, Decided(t, "disp-1", "rc-1", 1)))
	got, err := r.FindByID(bg, "disp-1")
	must(t, err)
	if events := got.DomainEvents(); len(events) != 0 {
		t.Fatalf("events = %v", events)
	}
}

// DispositionPolicyRepo runs the DispositionPolicyRepo contract. newRepo
// must return an EMPTY repository on every call.
func DispositionPolicyRepo(t *testing.T, newRepo func(t *testing.T) ports.DispositionPolicyRepo) {
	t.Helper()
	run(t, newRepo, map[string]func(t *testing.T, r ports.DispositionPolicyRepo){
		"an empty store has no current policy":      policyEmpty,
		"save marks the version current":            policyCurrent,
		"a new version appends and becomes current": policyAppend,
		"a re-declared version replaces its row":    policyRedeclare,
		"every declared version stays readable":     policyHistory,
	})
}

func policyEmpty(t *testing.T, r ports.DispositionPolicyRepo) {
	_, err := r.Current(bg)
	wantErr(t, err, ports.ErrNotFound)
	_, err = r.FindByVersion(bg, 1)
	wantErr(t, err, ports.ErrNotFound)
}

func policyCurrent(t *testing.T, r ports.DispositionPolicyRepo) {
	must(t, r.Save(bg, Declared(t, 1)))
	cur, err := r.Current(bg)
	must(t, err)
	if cur.Version() != 1 || cur.ReturnWindowDays() != 30 || cur.MaxUnitsPerLine() != 5 {
		t.Fatalf("current = %+v", cur.Snapshot())
	}
	if cur.VetoOutcome() != disposition.OutcomeDispose {
		t.Fatalf("veto = %s", cur.VetoOutcome())
	}
}

func policyAppend(t *testing.T, r ports.DispositionPolicyRepo) {
	must(t, r.Save(bg, Declared(t, 1)))
	v2, err := disposition.Declare(disposition.PolicyInput{
		Version: 2, ReturnWindowDays: 45, MaxUnitsPerLine: 6,
		Rules: []disposition.Rule{
			{Grade: "A", ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: "B", ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: "C", ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: "SCRAP", ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
		DeclaredAt: T0.Add(time.Hour),
	})
	must(t, err)
	must(t, r.Save(bg, v2))
	cur, err := r.Current(bg)
	must(t, err)
	if cur.Version() != 2 || cur.ReturnWindowDays() != 45 {
		t.Fatalf("current = %+v", cur.Snapshot())
	}
	old, err := r.FindByVersion(bg, 1)
	must(t, err)
	if old.ReturnWindowDays() != 30 {
		t.Fatalf("v1 was rewritten: %+v", old.Snapshot())
	}
}

func policyRedeclare(t *testing.T, r ports.DispositionPolicyRepo) {
	must(t, r.Save(bg, Declared(t, 1)))
	v1b, err := disposition.Declare(disposition.PolicyInput{
		Version: 1, ReturnWindowDays: 60, MaxUnitsPerLine: 7,
		Rules: []disposition.Rule{
			{Grade: "A", ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: "B", ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: "C", ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: "SCRAP", ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
		DeclaredAt: T0.Add(2 * time.Hour),
	})
	must(t, err)
	must(t, r.Save(bg, v1b))
	cur, err := r.Current(bg)
	must(t, err)
	if cur.Version() != 1 || cur.ReturnWindowDays() != 60 {
		t.Fatalf("re-declared v1 = %+v", cur.Snapshot())
	}
}

func policyHistory(t *testing.T, r ports.DispositionPolicyRepo) {
	must(t, r.Save(bg, Declared(t, 1)))
	must(t, r.Save(bg, Declared(t, 2)))
	must(t, r.Save(bg, Declared(t, 3)))
	for v := int64(1); v <= 3; v++ {
		got, err := r.FindByVersion(bg, v)
		must(t, err)
		if got.Version() != v {
			t.Fatalf("FindByVersion(%d) = v%d", v, got.Version())
		}
	}
	if _, err := r.FindByVersion(bg, 9); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("missing version err = %v", err)
	}
	cur, err := r.Current(bg)
	must(t, err)
	if cur.Version() != 3 {
		t.Fatalf("current = v%d", cur.Version())
	}
}
