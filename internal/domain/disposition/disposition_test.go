package disposition

import (
	"errors"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var (
	declaredAt = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	receivedAt = time.Date(2026, 10, 22, 14, 5, 0, 0, time.UTC)
	decidedAt  = time.Date(2026, 10, 22, 15, 10, 1, 0, time.UTC)
)

func policy(t *testing.T) *DispositionPolicy {
	t.Helper()
	p, err := Declare(PolicyInput{
		Version: 3, ReturnWindowDays: 30, MaxUnitsPerLine: 10,
		Rules: []Rule{
			{Grade: shared.GradeA, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
			{Grade: shared.GradeC, ProductClass: AnyProductClass, Outcome: OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: AnyProductClass, Outcome: OutcomeDispose},
			{Grade: shared.GradeA, ProductClass: "Hazmat", Outcome: OutcomeRTV},
			{Grade: shared.GradeB, ProductClass: "Hazmat", Outcome: OutcomeRTV},
		},
		DeclaredAt: declaredAt,
	})
	if err != nil {
		t.Fatalf("Declare: %v", err)
	}
	return p
}

// allRestock is a hostile table: every grade says RESTOCK, so only the
// guardrails can keep C, SCRAP and unrated hazmat out of the ledger.
func allRestock(version int64, veto Outcome) PolicyInput {
	return PolicyInput{
		Version: version, ReturnWindowDays: 30, MaxUnitsPerLine: 10, VetoOutcome: veto,
		Rules: []Rule{
			{Grade: shared.GradeA, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
			{Grade: shared.GradeC, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
			{Grade: shared.GradeScrap, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
		},
		DeclaredAt: declaredAt,
	}
}

var classify = []Classification{
	{SKU: "SKU-1", ProductClass: "Standard", Classified: true},
	{SKU: "SKU-2", ProductClass: "Hazmat", Hazmat: true, DoTHazardClass: 3, Classified: true},
	{SKU: "SKU-3", ProductClass: "", Classified: false},
	{SKU: "SKU-4", ProductClass: "SomethingNew", Classified: true},
	{SKU: "SKU-5", ProductClass: "Hazmat", Hazmat: true, DoTHazardClass: 0, Classified: true},
	{SKU: "SKU-6", ProductClass: "Standard", Hazmat: true, DoTHazardClass: 0, Classified: true},
}

// The invariant: Decide is total over (grade x classification x policy) —
// every combination answers with a valid outcome.
func TestDecideTotalOverGradeXClassificationXPolicy(t *testing.T) {
	p := policy(t)
	for _, g := range shared.Grades {
		for _, c := range classify {
			out, err := Decide(g, c, p)
			if err != nil {
				t.Fatalf("Decide(%s, %+v) not total: %v", g, c, err)
			}
			if !out.Valid() {
				t.Fatalf("Decide(%s, %+v) = %q, not an outcome", g, c, out)
			}
		}
	}
}

// The invariant: Decide is deterministic — the same inputs always give the
// same answer, in any repetition.
func TestDecideDeterministic(t *testing.T) {
	p := policy(t)
	for _, g := range shared.Grades {
		for _, c := range classify {
			first, err := Decide(g, c, p)
			if err != nil {
				t.Fatalf("Decide(%s, %+v): %v", g, c, err)
			}
			for i := 0; i < 3; i++ {
				again, err := Decide(g, c, p)
				if err != nil || again != first {
					t.Fatalf("Decide(%s, %+v): %v then %v/%v", g, c, first, again, err)
				}
			}
		}
	}
}

// The table's own rows come back unchanged where no guardrail bites.
func TestDecideFollowsTheTable(t *testing.T) {
	p := policy(t)
	out, err := Decide(shared.GradeC, Classification{ProductClass: "Standard", Classified: true}, p)
	if err != nil || out != OutcomeLiquidate {
		t.Fatalf("grade C standard = %v, %v", out, err)
	}
	out, err = Decide(shared.GradeA, Classification{ProductClass: "Hazmat", Hazmat: true, DoTHazardClass: 3, Classified: true}, p)
	if err != nil || out != OutcomeRTV {
		t.Fatalf("grade A hazmat = %v, %v", out, err)
	}
}

// The invariant: RESTOCK only for grade A/B — a hostile all-RESTOCK table
// cannot restock C or SCRAP.
func TestRestockGuardrailDemotesLowGrades(t *testing.T) {
	hostile, err := Declare(allRestock(1, ""))
	if err != nil {
		t.Fatalf("Declare hostile: %v", err)
	}
	standard := Classification{ProductClass: "Standard", Classified: true}
	for _, g := range []shared.Grade{shared.GradeC, shared.GradeScrap} {
		out, err := Decide(g, standard, hostile)
		if err != nil || out != OutcomeDispose {
			t.Fatalf("grade %s must be demoted to the veto outcome, got %v/%v", g, out, err)
		}
	}
	for _, g := range []shared.Grade{shared.GradeA, shared.GradeB} {
		out, err := Decide(g, standard, hostile)
		if err != nil || out != OutcomeRestock {
			t.Fatalf("grade %s restocks, got %v/%v", g, out, err)
		}
	}
}

// The invariant: never RESTOCK for a DISPOSE-forced SKU — the forced outcome
// wins over even an all-RESTOCK table, for every grade.
func TestRestockGuardrailForcedOutcomeWins(t *testing.T) {
	hostile, err := Declare(allRestock(1, ""))
	if err != nil {
		t.Fatalf("Declare hostile: %v", err)
	}
	forced := Classification{ProductClass: "Standard", Classified: true, Forces: OutcomeDispose}
	for _, g := range shared.Grades {
		out, err := Decide(g, forced, hostile)
		if err != nil || out != OutcomeDispose {
			t.Fatalf("forced DISPOSE grade %s = %v/%v", g, out, err)
		}
	}
}

// The invariant: never RESTOCK for a hazmat SKU without a DOT rating
// (fail-closed, ADR 0001); a rated one restocks when the table says so.
func TestRestockGuardrailHazmatWithoutRating(t *testing.T) {
	hostile, err := Declare(allRestock(1, ""))
	if err != nil {
		t.Fatalf("Declare hostile: %v", err)
	}
	unrated := Classification{ProductClass: "Standard", Hazmat: true, DoTHazardClass: 0, Classified: true}
	for _, g := range shared.Grades {
		out, err := Decide(g, unrated, hostile)
		if err != nil || out == OutcomeRestock {
			t.Fatalf("unrated hazmat grade %s must not restock, got %v/%v", g, out, err)
		}
	}
	rated, err := Decide(shared.GradeA, Classification{ProductClass: "Standard", Hazmat: true, DoTHazardClass: 3, Classified: true}, hostile)
	if err != nil || rated != OutcomeRestock {
		t.Fatalf("rated hazmat grade A = %v/%v", rated, err)
	}
}

// A demoted RESTOCK falls back to the policy's veto outcome, DISPOSE by
// default and whatever the operator declared otherwise.
func TestRestockVetoOutcome(t *testing.T) {
	custom, err := Declare(allRestock(2, OutcomeRTV))
	if err != nil {
		t.Fatalf("Declare custom veto: %v", err)
	}
	if custom.VetoOutcome() != OutcomeRTV {
		t.Fatalf("veto = %v, want RTV", custom.VetoOutcome())
	}
	standard := Classification{ProductClass: "Standard", Classified: true}
	out, err := Decide(shared.GradeScrap, standard, custom)
	if err != nil || out != OutcomeRTV {
		t.Fatalf("custom veto = %v/%v", out, err)
	}
	p := policy(t)
	if p.VetoOutcome() != OutcomeDispose {
		t.Fatalf("default veto = %v, want DISPOSE", p.VetoOutcome())
	}
}

// The failing paths of Decide: an unknown grade, a missing policy, a bad
// forced outcome.
func TestDecideValidation(t *testing.T) {
	p := policy(t)
	if _, err := Decide(shared.Grade("D"), Classification{}, p); !errors.Is(err, ErrInvalidGrade) {
		t.Fatalf("bad grade = %v", err)
	}
	if _, err := Decide(shared.GradeA, Classification{}, nil); !errors.Is(err, ErrNoPolicy) {
		t.Fatalf("no policy = %v", err)
	}
	badForced := Classification{Forces: Outcome("KEEP")}
	if _, err := Decide(shared.GradeA, badForced, p); !errors.Is(err, ErrInvalidForcedOutcome) {
		t.Fatalf("bad forced = %v", err)
	}
	if !OutcomeRestock.Valid() || Outcome("X").Valid() || OutcomeRestock.String() != "RESTOCK" {
		t.Fatal("outcome validity")
	}
}

func fullRules() []Rule {
	return []Rule{
		{Grade: shared.GradeA, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
		{Grade: shared.GradeB, ProductClass: AnyProductClass, Outcome: OutcomeRestock},
		{Grade: shared.GradeC, ProductClass: AnyProductClass, Outcome: OutcomeLiquidate},
		{Grade: shared.GradeScrap, ProductClass: AnyProductClass, Outcome: OutcomeDispose},
	}
}

// The policy is data: its bounds are validated on declare.
func TestPolicyValidationBounds(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*PolicyInput)
		want error
	}{
		{"version 0", func(in *PolicyInput) { in.Version = 0 }, ErrInvalidPolicy},
		{"negative window", func(in *PolicyInput) { in.ReturnWindowDays = -1 }, ErrInvalidPolicy},
		{"max 0", func(in *PolicyInput) { in.MaxUnitsPerLine = 0 }, ErrInvalidPolicy},
		{"zero declaredAt", func(in *PolicyInput) { in.DeclaredAt = time.Time{} }, ErrNoTimestamp},
		{"bad veto", func(in *PolicyInput) { in.VetoOutcome = Outcome("X") }, ErrInvalidVetoOutcome},
	}
	for _, tc := range cases {
		in := PolicyInput{Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 10, Rules: fullRules(), DeclaredAt: declaredAt}
		tc.mut(&in)
		if _, err := Declare(in); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Declare = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// The invariant: the policy bounds are inclusive at the floor — a zero-day
// window (same-day returns) and a single-unit cap are both declarable.
func TestPolicyAcceptsFloorBoundaries(t *testing.T) {
	zeroWindow, err := Declare(PolicyInput{Version: 1, ReturnWindowDays: 0,
		MaxUnitsPerLine: 10, Rules: fullRules(), DeclaredAt: declaredAt})
	if err != nil {
		t.Fatalf("Declare with a zero-day window = %v, want accepted", err)
	}
	if !zeroWindow.WindowEndsFrom(declaredAt).Equal(declaredAt) {
		t.Fatalf("zero-day window end = %v, want %v", zeroWindow.WindowEndsFrom(declaredAt), declaredAt)
	}
	oneCap, err := Declare(PolicyInput{Version: 1, ReturnWindowDays: 30,
		MaxUnitsPerLine: 1, Rules: fullRules(), DeclaredAt: declaredAt})
	if err != nil {
		t.Fatalf("Declare with a one-unit cap = %v, want accepted", err)
	}
	if oneCap.MaxUnitsPerLine() != 1 {
		t.Fatalf("cap = %d, want 1", oneCap.MaxUnitsPerLine())
	}
}

// The table is validated on declare: total (a "*" default per grade), no
// duplicates, valid grades, outcomes and classes.
func TestPolicyValidationRules(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*PolicyInput)
		want error
	}{
		{"not total", func(in *PolicyInput) { in.Rules = in.Rules[:3] }, ErrPolicyNotTotal},
		{"no rules", func(in *PolicyInput) { in.Rules = nil }, ErrPolicyNotTotal},
		{"duplicate pair", func(in *PolicyInput) {
			in.Rules = append(in.Rules, Rule{Grade: shared.GradeA, ProductClass: AnyProductClass, Outcome: OutcomeRTV})
		}, ErrDuplicateRule},
		{"bad rule grade", func(in *PolicyInput) {
			in.Rules = append(in.Rules, Rule{Grade: shared.Grade("D"), ProductClass: "X", Outcome: OutcomeRTV})
		}, ErrInvalidGrade},
		{"bad rule outcome", func(in *PolicyInput) {
			in.Rules = append(in.Rules, Rule{Grade: shared.GradeA, ProductClass: "X", Outcome: Outcome("KEEP")})
		}, ErrInvalidPolicy},
		{"blank class", func(in *PolicyInput) {
			in.Rules = append(in.Rules, Rule{Grade: shared.GradeA, ProductClass: "", Outcome: OutcomeRTV})
		}, ErrInvalidPolicy},
	}
	for _, tc := range cases {
		in := PolicyInput{Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 10, Rules: fullRules(), DeclaredAt: declaredAt}
		tc.mut(&in)
		if _, err := Declare(in); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Declare = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// The policy accessors and the exact window math.
func TestPolicyAccessors(t *testing.T) {
	p := policy(t)
	if p.Version() != 3 || p.ReturnWindowDays() != 30 || p.MaxUnitsPerLine() != 10 || p.DeclaredAt() != declaredAt {
		t.Fatalf("policy accessors = %v %v %v %v", p.Version(), p.ReturnWindowDays(), p.MaxUnitsPerLine(), p.DeclaredAt())
	}
	if got, want := p.WindowEndsFrom(decidedAt), decidedAt.AddDate(0, 0, 30); got != want {
		t.Fatalf("window = %v, want %v", got, want)
	}
}

func decision() LineDecision {
	return LineDecision{
		DispositionID: "disp-1", ReceiptID: "rcpt-1", LineNo: 1,
		SKU: "SKU-1", Quantity: 2, Grade: shared.GradeB,
		Classification: Classification{ProductClass: "Standard", Classified: true},
		SiteID:         "WH1", RMAID: "rma-1",
		ReceivedAt: receivedAt, DecidedAt: decidedAt,
	}
}

func decide(t *testing.T, in LineDecision) *Disposition {
	t.Helper()
	in.Policy = policy(t)
	d, err := DecideForLine(in)
	if err != nil {
		t.Fatalf("DecideForLine: %v", err)
	}
	return d
}

// The invariant: ReturnLineStaged is emitted once per RESTOCK disposition —
// DecideForLine raises it with the decided event and a second Stage is
// refused without a second event.
func TestRestockDecisionStagesExactlyOnce(t *testing.T) {
	d := decide(t, decision()) // grade B standard -> RESTOCK
	if !d.Staged() {
		t.Fatal("a restock decision must be staged")
	}
	if err := d.Stage(); !errors.Is(err, ErrAlreadyStaged) {
		t.Fatalf("second Stage = %v, want ErrAlreadyStaged", err)
	}
	evs := d.DomainEvents()
	if len(evs) != 2 {
		t.Fatalf("events = %d, want exactly 2 (decided + staged)", len(evs))
	}
}

// The decided event carries the line, the policy version and empty
// supersedes/reason on a policy decision.
func TestRestockDecisionEvent(t *testing.T) {
	d := decide(t, decision())
	evs := d.DomainEvents()
	dec, ok := evs[0].(DispositionDecided)
	if !ok {
		t.Fatalf("first event = %T", evs[0])
	}
	if dec.Outcome != OutcomeRestock || dec.PolicyVersion != 3 ||
		dec.Supersedes != "" || dec.Reason != "" || dec.ReceiptID != "rcpt-1" ||
		dec.LineNo != 1 || dec.SKU != "SKU-1" || dec.Quantity != 2 || dec.Grade != "B" {
		t.Fatalf("DispositionDecided = %+v", dec)
	}
}

// The staged event is inbound-receiving's ReceiptLineReceived shape plus
// rma_id, disposition_id (in the header) and site_id.
func TestRestockStagedEvent(t *testing.T) {
	d := decide(t, decision())
	staged, ok := d.DomainEvents()[1].(ReturnLineStaged)
	if !ok {
		t.Fatalf("second event = %T", d.DomainEvents()[1])
	}
	if staged.ReceiptID != "rcpt-1" || staged.LineNo != 1 || staged.SKU != "SKU-1" ||
		staged.Quantity != 2 || staged.Condition != "Good" || staged.ReceivedAt != receivedAt ||
		staged.RMAID != "rma-1" || staged.SiteID != "WH1" || staged.DispositionID() != "disp-1" {
		t.Fatalf("ReturnLineStaged = %+v", staged)
	}
}

// The invariant: only a RESTOCK disposition stages — a LIQUIDATE decision
// raises no staged event and Stage is refused.
func TestNonRestockDecisionNeverStages(t *testing.T) {
	in := decision()
	in.DispositionID = "disp-2"
	in.Grade = shared.GradeC // grade C standard -> LIQUIDATE
	d := decide(t, in)
	if d.Staged() {
		t.Fatal("a liquidate decision must not stage")
	}
	if err := d.Stage(); !errors.Is(err, ErrNotRestock) {
		t.Fatalf("Stage on liquidate = %v, want ErrNotRestock", err)
	}
	if evs := d.DomainEvents(); len(evs) != 1 {
		t.Fatalf("liquidate events = %d, want 1", len(evs))
	}
}

// The invariant: an override creates a NEW Disposition carrying supersedes
// and a non-empty reason; the original is untouched.
func TestOverrideCarriesSupersedesAndReason(t *testing.T) {
	original := decide(t, decision())
	over, err := original.Override(OverrideInput{
		NewDispositionID: "disp-2", Outcome: OutcomeRTV,
		Reason: "vendor accepts open-box returns this quarter", DecidedAt: decidedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Override: %v", err)
	}
	if over.Supersedes() != "disp-1" || over.Reason() == "" || over.ID() == original.ID() {
		t.Fatalf("override = %+v", over)
	}
	if original.Supersedes() != "" || len(original.DomainEvents()) != 2 {
		t.Fatal("the original decision must be untouched")
	}
}

// The override copies every line fact onto the new disposition and only a
// RESTOCK override ever stages.
func TestOverrideCopiesLineFacts(t *testing.T) {
	original := decide(t, decision())
	over, err := original.Override(OverrideInput{
		NewDispositionID: "disp-2", Outcome: OutcomeRTV,
		Reason: "vendor accepts open-box returns this quarter", DecidedAt: decidedAt.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Override: %v", err)
	}
	if over.Outcome() != OutcomeRTV || over.PolicyVersion() != 3 || over.ReceiptID() != "rcpt-1" ||
		over.LineNo() != 1 || over.SKU() != "SKU-1" || over.Quantity() != 2 || over.Grade() != shared.GradeB {
		t.Fatalf("override fields = %+v", over)
	}
	if over.RMAID() != "rma-1" || over.SiteID() != "WH1" {
		t.Fatalf("override line refs = %+v", over)
	}
	if over.Staged() {
		t.Fatal("an RTV override must not stage")
	}
}

// A RESTOCK override stages its own line exactly once.
func TestRestockOverrideStagesItsOwnLine(t *testing.T) {
	original := decide(t, decision())
	restockOver, err := original.Override(OverrideInput{
		NewDispositionID: "disp-3", Outcome: OutcomeRestock,
		Reason: "ops reworked the units to grade A", DecidedAt: decidedAt.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Override restock: %v", err)
	}
	if restockOver.Staged() {
		t.Fatal("an override must not stage before Stage is called")
	}
	if err := restockOver.Stage(); err != nil {
		t.Fatalf("Stage a restock override: %v", err)
	}
	if err := restockOver.Stage(); !errors.Is(err, ErrAlreadyStaged) {
		t.Fatalf("second Stage = %v", err)
	}
}

// The failing paths: a blank reason, an unknown outcome, a zero time, a bad
// id.
func TestOverrideValidation(t *testing.T) {
	original := decide(t, decision())
	cases := []struct {
		name string
		in   OverrideInput
		want error
	}{
		{"blank reason", OverrideInput{NewDispositionID: "disp-2", Outcome: OutcomeRTV, Reason: " ", DecidedAt: decidedAt}, ErrOverrideReasonRequired},
		{"bad outcome", OverrideInput{NewDispositionID: "disp-2", Outcome: Outcome("KEEP"), Reason: "why", DecidedAt: decidedAt}, ErrInvalidPolicy},
		{"zero time", OverrideInput{NewDispositionID: "disp-2", Outcome: OutcomeRTV, Reason: "why", DecidedAt: time.Time{}}, ErrNoTimestamp},
		{"blank id", OverrideInput{NewDispositionID: "", Outcome: OutcomeRTV, Reason: "why", DecidedAt: decidedAt}, shared.ErrBlankID},
	}
	for _, tc := range cases {
		if _, err := original.Override(tc.in); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Override = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// The failing paths of the line decision: every identity field, the quantity
// floor, the timestamps and the grade.
func TestLineDecisionValidation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*LineDecision)
		want error
	}{
		{"blank disposition id", func(d *LineDecision) { d.DispositionID = " " }, shared.ErrBlankID},
		{"nul in disposition id", func(d *LineDecision) { d.DispositionID = "disp\x00" }, shared.ErrNULInID},
		{"blank receipt id", func(d *LineDecision) { d.ReceiptID = "" }, shared.ErrBlankID},
		{"blank sku", func(d *LineDecision) { d.SKU = "" }, shared.ErrBlankID},
		{"quantity below one", func(d *LineDecision) { d.Quantity = 0 }, shared.ErrQuantityBelowOne},
		{"blank site", func(d *LineDecision) { d.SiteID = " " }, shared.ErrBlankID},
		{"bad rma id", func(d *LineDecision) { d.RMAID = "\x00" }, shared.ErrNULInID},
		{"line below one", func(d *LineDecision) { d.LineNo = 0 }, shared.ErrLineNoBelowOne},
		{"zero receivedAt", func(d *LineDecision) { d.ReceivedAt = time.Time{} }, ErrNoTimestamp},
		{"zero decidedAt", func(d *LineDecision) { d.DecidedAt = time.Time{} }, ErrNoTimestamp},
		{"bad grade", func(d *LineDecision) { d.Grade = shared.Grade("D") }, ErrInvalidGrade},
	}
	for _, tc := range cases {
		d := decision()
		d.Policy = policy(t)
		tc.mut(&d)
		if _, err := DecideForLine(d); !errors.Is(err, tc.want) {
			t.Fatalf("%s: DecideForLine = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// The accessors of a decided line.
func TestDecidedAccessors(t *testing.T) {
	d := decide(t, decision())
	checkIdentity(t, d)
	checkLineRefs(t, d)
}

func checkIdentity(t *testing.T, d *Disposition) {
	t.Helper()
	if d.ID() != "disp-1" || d.ReceiptID() != "rcpt-1" || d.LineNo() != 1 {
		t.Fatalf("identity = %s %s %d", d.ID(), d.ReceiptID(), d.LineNo())
	}
	if d.SKU() != "SKU-1" || d.Quantity() != 2 || d.Grade() != shared.GradeB {
		t.Fatalf("line = %s %d %s", d.SKU(), d.Quantity(), d.Grade())
	}
}

func checkLineRefs(t *testing.T, d *Disposition) {
	t.Helper()
	if d.Outcome() != OutcomeRestock || d.PolicyVersion() != 3 || !d.Staged() {
		t.Fatalf("decision = %s v%d staged=%v", d.Outcome(), d.PolicyVersion(), d.Staged())
	}
	if d.Supersedes() != "" || d.Reason() != "" || d.RMAID() != "rma-1" ||
		d.SiteID() != "WH1" || d.DecidedAt() != decidedAt {
		t.Fatalf("refs = %q %q %s %s %v", d.Supersedes(), d.Reason(), d.RMAID(), d.SiteID(), d.DecidedAt())
	}
}
