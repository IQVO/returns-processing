package usecases_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// scriptedGateway answers the submissions of the drain from a script, one
// answer per call (the last repeats), and remembers which outcome each call
// carried: the adapter derives the idempotency key from the authorization
// id, so the same id on every call is the same key on every call.
type scriptedGateway struct {
	fakeReturnsGateway
	answers []error
	calls   int
}

func (g *scriptedGateway) SubmitReturnOutcome(ctx context.Context, out views.ReturnOutcome) (string, error) {
	g.submits = append(g.submits, out)
	answer := g.answers[min(g.calls, len(g.answers)-1)]
	g.calls++
	if answer != nil {
		return "", answer
	}
	return g.submitTx, nil
}

type steppingClock struct{ now time.Time }

func (c *steppingClock) Now() time.Time { return c.now }

// releasedRefusal is what the adapter hands up for a refusal the callee
// releases (replay class RETRYABLE): not a verdict.
func releasedRefusal(slug string) error {
	return &fakeRefusalError{slug: slug, released: true, cause: errors.New("network: 404 " + slug + ": the advice is not consumed yet")}
}

func terminalRefusal(slug string) error {
	return &fakeRefusalError{slug: slug, cause: errors.New("network: 409 " + slug + ": no")}
}

// fakeRefusalError is the adapter's refusal error as the drain sees it (ports.OutcomeRefusal).
type fakeRefusalError struct {
	slug     string
	released bool
	cause    error
}

func (e *fakeRefusalError) Error() string       { return e.cause.Error() }
func (e *fakeRefusalError) ProblemSlug() string { return e.slug }
func (e *fakeRefusalError) Released() bool      { return e.released }

func (e *fakeRefusalError) Unwrap() []error {
	if e.released {
		return []error{e.cause}
	}
	return []error{ports.ErrOutcomeRefused, e.cause}
}

func pendingOutcome(t *testing.T, id string) *memory.ReturnAdvices {
	t.Helper()
	ctx := context.Background()
	store := memory.NewReturnAdvices()
	if err := store.ApplyReturnAdvice(ctx, adviceAt(id, time.Unix(1000, 0))); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOutcomePending(ctx, id, views.ReturnOutcome{AuthorizationID: id, NodeID: "n-1", ReceivedAt: time.Unix(3000, 0)}); err != nil {
		t.Fatal(err)
	}
	return store
}

func stateOf(t *testing.T, store *memory.ReturnAdvices, id string) string {
	t.Helper()
	got, _, err := store.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return got.OutcomeState
}

// wantReleased fails unless the tick released exactly one refusal of the slug without failing or erroring.
func wantReleased(t *testing.T, tick string, res usecases.OutcomeDrainResult, err error, slug string) {
	t.Helper()
	if err != nil || res.Failed != 0 || res.Submitted != 0 || len(res.Released) != 1 {
		t.Fatalf("%s = %+v err=%v (a released refusal is neither a failure nor an error)", tick, res, err)
	}
	if res.Released[0].AuthorizationID != "ret-1" || res.Released[0].Slug != slug {
		t.Fatalf("%s released = %+v, want %s", tick, res.Released, slug)
	}
}

func wantNote(t *testing.T, store *memory.ReturnAdvices, contains, absent string) {
	t.Helper()
	note := store.Refusal("ret-1")
	if !strings.Contains(note, contains) || (absent != "" && strings.Contains(note, absent)) {
		t.Fatalf("the operator's note = %q, want the last refusal %q (not %q)", note, contains, absent)
	}
}

func TestReleasedRefusalKeepsTheOutcomePendingAndALaterSuccessCompletesIt(t *testing.T) {
	ctx := context.Background()
	store := pendingOutcome(t, "ret-1")
	gw := &scriptedGateway{
		fakeReturnsGateway: fakeReturnsGateway{submitTx: "tx-1", txs: map[string]views.ReturnOutcomeTransaction{"tx-1": {Status: views.TransactionSucceeded}}},
		answers:            []error{releasedRefusal("not-found"), releasedRefusal("concurrent-update"), nil},
	}
	uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store}

	res, err := uc.Execute(ctx)
	wantReleased(t, "tick 1", res, err, "not-found")
	if got := stateOf(t, store, "ret-1"); got != views.AdviceOutcomePending {
		t.Fatalf("state after a released refusal = %q, want PENDING", got)
	}
	wantNote(t, store, "not-found", "")

	res, err = uc.Execute(ctx)
	wantReleased(t, "tick 2", res, err, "concurrent-update")
	wantNote(t, store, "concurrent-update", "not-found")

	res, err = uc.Execute(ctx)
	if err != nil || res.Submitted != 1 || len(res.Released) != 0 || store.Refusal("ret-1") != "" {
		t.Fatalf("tick 3 = %+v err=%v note=%q (an accepted submission clears the note)", res, err, store.Refusal("ret-1"))
	}
	res, err = uc.Execute(ctx)
	if err != nil || res.Settled != 1 || stateOf(t, store, "ret-1") != views.AdviceOutcomeSucceeded {
		t.Fatalf("tick 4 = %+v err=%v state=%q", res, err, stateOf(t, store, "ret-1"))
	}

	// Every attempt carried the same outcome: the adapter derives ONE key from it.
	if len(gw.submits) != 3 {
		t.Fatalf("submissions = %d, want 3", len(gw.submits))
	}
	for _, s := range gw.submits {
		if s.AuthorizationID != "ret-1" || s.NodeID != "n-1" || !s.ReceivedAt.Equal(time.Unix(3000, 0)) {
			t.Fatalf("the retried outcome changed: %+v", s)
		}
	}
}

func TestFinalRefusalEndsTerminalAsBefore(t *testing.T) {
	ctx := context.Background()
	for _, refusal := range []error{terminalRefusal("ack-window-closed"), errRefusedOutcome} {
		store := pendingOutcome(t, "ret-1")
		gw := &scriptedGateway{answers: []error{refusal}}
		uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store}

		res, err := uc.Execute(ctx)
		if err != nil || res.Failed != 1 || len(res.Released) != 0 {
			t.Fatalf("drain = %+v err=%v", res, err)
		}
		if got := stateOf(t, store, "ret-1"); got != views.AdviceOutcomeFailed {
			t.Fatalf("state = %q, want FAILED", got)
		}
		if note := store.Refusal("ret-1"); note != refusal.Error() {
			t.Fatalf("recorded refusal = %q, want %q", note, refusal.Error())
		}
		if res, _ = uc.Execute(ctx); res.Failed != 0 || len(gw.submits) != 1 {
			t.Fatalf("a FAILED outcome drained again: %+v after %d submissions", res, len(gw.submits))
		}
	}
}

func TestAForeverRetryableRefusalEndsTerminalWhenTheWindowIsSpent(t *testing.T) {
	ctx := context.Background()
	store := pendingOutcome(t, "ret-1")
	since := pendingSince(t, store)
	clock := &steppingClock{now: since.Add(time.Minute)}
	gw := &scriptedGateway{answers: []error{releasedRefusal("illegal-transition")}}
	uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store, Clock: clock, RetryWindow: 2 * time.Hour}

	for _, at := range []time.Duration{time.Minute, time.Hour, 2*time.Hour - time.Second, 2 * time.Hour} {
		clock.now = since.Add(at)
		res, err := uc.Execute(ctx)
		if err != nil || res.Failed != 0 || len(res.Released) != 1 {
			t.Fatalf("at +%s: %+v err=%v (inside the window the outcome stays pending)", at, res, err)
		}
	}
	clock.now = since.Add(2*time.Hour + time.Second)
	res, err := uc.Execute(ctx)
	if err != nil || res.Failed != 1 || len(res.Released) != 0 {
		t.Fatalf("past the window: %+v err=%v (the same terminal state as a final refusal)", res, err)
	}
	if got := stateOf(t, store, "ret-1"); got != views.AdviceOutcomeFailed {
		t.Fatalf("state = %q, want FAILED", got)
	}
	if note := store.Refusal("ret-1"); !strings.Contains(note, "illegal-transition") {
		t.Fatalf("the terminal refusal text = %q, want the slug", note)
	}
}

func TestTheDefaultWindowIsADay(t *testing.T) {
	ctx := context.Background()
	store := pendingOutcome(t, "ret-1")
	since := pendingSince(t, store)
	clock := &steppingClock{now: since.Add(23 * time.Hour)}
	gw := &scriptedGateway{answers: []error{releasedRefusal("not-found")}}
	uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store, Clock: clock}

	if res, err := uc.Execute(ctx); err != nil || len(res.Released) != 1 {
		t.Fatalf("+23h = %+v err=%v", res, err)
	}
	clock.now = since.Add(25 * time.Hour)
	if res, err := uc.Execute(ctx); err != nil || res.Failed != 1 {
		t.Fatalf("+25h = %+v err=%v", res, err)
	}
}

// Without a clock wired the drain reads the system time: a fresh outcome is inside the window.
func TestTheDrainWithoutAClockReadsSystemTime(t *testing.T) {
	store := pendingOutcome(t, "ret-1")
	gw := &scriptedGateway{answers: []error{releasedRefusal("not-found")}}
	uc := &usecases.SubmitReturnOutcomes{Gateway: gw, Views: store, Feed: store}
	if res, err := uc.Execute(context.Background()); err != nil || len(res.Released) != 1 || res.Failed != 0 {
		t.Fatalf("drain = %+v err=%v", res, err)
	}
}

func pendingSince(t *testing.T, store *memory.ReturnAdvices) time.Time {
	t.Helper()
	pending, err := store.PendingOutcomes(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %+v err=%v", pending, err)
	}
	return pending[0].PendingSince
}
