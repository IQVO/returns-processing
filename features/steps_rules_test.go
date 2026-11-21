package features_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// steps_rules_test.go — the wave-3 step vocabulary: the documentation-derived
// rules (features/*_rules.feature). It only ADDS to the existing vocabulary:
// versioned policy seeds, response-prefix and paging assertions, keys of a
// chosen length, and a reflective assertion on the domain events the use
// cases published (the facts the outbox would carry).

func registerRulesSteps(sc *godog.ScenarioContext, w *world) {
	registerRulesSeedSteps(sc, w)
	registerRulesWireSteps(sc, w)
	registerRulesEventSteps(sc, w)
	registerNetworkAdviceSteps(sc, w)
}

// registerRulesSeedSteps seeds policy versions and the manifest facts the
// Kafka envelope can deliver incomplete (no package id, no occurrence time).
func registerRulesSeedSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a manifest fact (\S+) for work unit (\S+) without a package id is consumed$`,
		func(eventID, workUnit string) error {
			w.run(func() error {
				out, err := w.consumers.manifest.Handle(w.ctx, eventID, workUnit, "", w.clock.now)
				w.consumeOut = out
				return err
			})
			return nil
		})

	sc.Step(`^a manifest fact (\S+) for work unit (\S+) without an occurrence time is consumed$`,
		func(eventID, workUnit string) error {
			w.run(func() error {
				out, err := w.consumers.manifest.Handle(w.ctx, eventID, workUnit, "pkg-1029", time.Time{})
				w.consumeOut = out
				return err
			})
			return nil
		})

	sc.Step(`^a policy version (\d+) is declared with window (\d+) days and cap (\d+) units per line$`,
		func(version, windowDays, maxUnits int) error {
			return declarePolicy(w, version, windowDays, maxUnits, nil)
		})

	sc.Step(`^a policy version (\d+) is declared where grade (\S+) maps to (\S+) by default$`,
		func(version int, grade, outcome string) error {
			rules := []disposition.Rule{
				{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
				{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
				{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
				{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
			}
			for i := range rules {
				if rules[i].Grade == shared.Grade(grade) {
					rules[i].Outcome = disposition.Outcome(outcome)
				}
			}
			return declareRules(w, version, rules)
		})
}

// declareRules installs a policy version (window 30 days, cap 5 units) with
// exactly the given rule table.
func declareRules(w *world, version int, rules []disposition.Rule) error {
	w.run(func() error {
		_, err := w.uc.declare.Execute(w.ctx, usecases.PolicyDeclaration{
			Version: int64(version), ReturnWindowDays: 30, MaxUnitsPerLine: 5, Rules: rules,
		})
		return err
	})
	return nil
}

// registerRulesWireSteps registers the extra request and response steps.
func registerRulesWireSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response field "([^"]+)" starts with (.+)$`, func(path, prefix string) error {
		v, ok := w.field(path)
		if !ok {
			return fmt.Errorf("no field %q in body: %s", path, w.lastBody)
		}
		if got := scalar(v); !strings.HasPrefix(got, prefix) {
			return fmt.Errorf("field %q = %q, want a value starting with %q", path, got, prefix)
		}
		return nil
	})

	// The next page of a list: the same request plus the nextCursor the
	// previous answer carried.
	sc.Step(`^the next page of "([^"]+)" is requested$`, func(path string) error {
		v, ok := w.field("nextCursor")
		if !ok {
			return fmt.Errorf("the last answer has no nextCursor: %s", w.lastBody)
		}
		return w.do("api", "GET", path+"&cursor="+url.QueryEscape(scalar(v)), "", "")
	})

	sc.Step(`^a (POST|PUT) request is sent to "([^"]+)" with a key of (\d+) characters and the JSON body (.*)$`,
		func(method, path string, n int, body string) error {
			return w.do("api", method, path, strings.Repeat("k", n), body)
		})

	sc.Step(`^no receipt events are published$`, func() error {
		return wantInt("receipt events", len(w.ev.ReceiptEventNames()), 0)
	})

	sc.Step(`^no disposition events are published$`, func() error {
		return wantInt("disposition events", len(w.ev.DispositionEventNames()), 0)
	})
}

// registerRulesEventSteps registers the assertion on a published event.
func registerRulesEventSteps(sc *godog.ScenarioContext, w *world) {
	// `the ReturnLineStaged event number 2 carries` + a table of
	// field | value rows: the n-th published event of that name (1-based, in
	// publication order across the three aggregates' logs) has each field
	// equal to the value. Fields are the domain event's own exported fields
	// and header accessors; instants are RFC 3339 UTC, lists are joined.
	sc.Step(`^the (\w+) event number (\d+) carries$`, func(name string, n int, table *godog.Table) error {
		events := w.publishedNamed(name)
		if n < 1 || n > len(events) {
			return fmt.Errorf("%d %s event(s) published, want number %d", len(events), name, n)
		}
		ev := events[n-1]
		for i, row := range table.Rows {
			if i == 0 {
				continue // header: field | value
			}
			field, want := row.Cells[0].Value, row.Cells[1].Value
			got, err := eventField(ev, field)
			if err != nil {
				return fmt.Errorf("%s event %d: %w", name, n, err)
			}
			if got != want {
				return fmt.Errorf("%s event %d field %s = %q, want %q", name, n, field, got, want)
			}
		}
		return nil
	})
}

// publishedNamed returns the published events called name, in the order of
// their aggregate's log.
func (w *world) publishedNamed(name string) []any {
	var out []any
	for _, e := range w.ev.RMA {
		if e.EventName() == name {
			out = append(out, e)
		}
	}
	for _, e := range w.ev.Receipt {
		if e.EventName() == name {
			out = append(out, e)
		}
	}
	for _, e := range w.ev.Disposition {
		if e.EventName() == name {
			out = append(out, e)
		}
	}
	return out
}

// eventField renders one field (or zero-argument accessor, for the header
// promoted methods) of a domain event as the text a feature table writes.
func eventField(ev any, name string) (string, error) {
	v := reflect.ValueOf(ev)
	if f := v.FieldByName(name); f.IsValid() {
		return renderValue(f), nil
	}
	if m := v.MethodByName(name); m.IsValid() && m.Type().NumIn() == 0 && m.Type().NumOut() == 1 {
		return renderValue(m.Call(nil)[0]), nil
	}
	return "", errors.New("no field " + name)
}

func renderValue(v reflect.Value) string {
	switch x := v.Interface().(type) {
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case shared.OrderLineRef:
		return x.WorkUnit()
	case []string:
		return strings.Join(x, ",")
	case []returnreceipt.OpenedLine:
		parts := make([]string, 0, len(x))
		for _, l := range x {
			parts = append(parts, strconv.Itoa(int(l.LineNo))+":"+l.SKU+":"+strconv.FormatInt(l.Quantity, 10)+":"+l.RMAID)
		}
		return strings.Join(parts, ";")
	}
	if v.Kind() == reflect.String {
		return v.String()
	}
	return fmt.Sprint(v.Interface())
}

// ---------------------------------------------------------------------------
// The network return-advice seam (ADR 0012/0013), driven at the use-case
// level: the poll (PollReturnAdvices), the completion hook on the inspection
// and the outcome drain (SubmitReturnOutcomes), over the advice view double
// and a scripted network gateway. The real HTTP client — its derived
// idempotency key, its paging and its refusal-class table — is the
// adapter's own spec-pinned test surface, not this suite's.

// bddOutcomeRefusalError mirrors the adapter's OutcomeRefusal (refusal_classes.go):
// a verdict matches ports.ErrOutcomeRefused, a released refusal does not.
type bddOutcomeRefusalError struct {
	slug     string
	released bool
}

func (e bddOutcomeRefusalError) Error() string {
	return "network refused the outcome: " + e.slug
}
func (e bddOutcomeRefusalError) ProblemSlug() string { return e.slug }
func (e bddOutcomeRefusalError) Released() bool      { return e.released }
func (e bddOutcomeRefusalError) Unwrap() []error {
	if e.released {
		return nil
	}
	return []error{ports.ErrOutcomeRefused}
}

var _ ports.OutcomeRefusal = bddOutcomeRefusalError{}

// netAction is one scripted answer of the network to an outcome submission.
type netAction struct {
	kind string // accept | released | final | bare | unreachable
	tx   string
	slug string
}

// scriptedReturnsGateway is the BDD double of ports.NetworkReturnsGateway:
// the advice set the network currently hands out (re-delivery is normal),
// a per-authorization script of submission answers consumed in order, and
// a table of transaction verdicts.
type scriptedReturnsGateway struct {
	advices map[string]views.ReturnAdvice
	polls   []time.Time // the readyAfter of every poll, in order
	script  map[string][]netAction
	txs     map[string]views.ReturnOutcomeTransaction
	bodies  map[string][]views.ReturnOutcome
}

func newScriptedReturnsGateway() *scriptedReturnsGateway {
	return &scriptedReturnsGateway{
		advices: map[string]views.ReturnAdvice{},
		script:  map[string][]netAction{},
		txs:     map[string]views.ReturnOutcomeTransaction{},
		bodies:  map[string][]views.ReturnOutcome{},
	}
}

// PollAdvices returns the current advice set (a copy, newest readyAt first).
func (g *scriptedReturnsGateway) PollAdvices(_ context.Context, readyAfter time.Time) ([]views.ReturnAdvice, error) {
	g.polls = append(g.polls, readyAfter)
	out := make([]views.ReturnAdvice, 0, len(g.advices))
	for _, a := range g.advices {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReadyAt.Before(out[j].ReadyAt) })
	return out, nil
}

// SubmitReturnOutcome consumes the next scripted answer of that authorization.
func (g *scriptedReturnsGateway) SubmitReturnOutcome(_ context.Context, out views.ReturnOutcome) (string, error) {
	id := out.AuthorizationID
	g.bodies[id] = append(g.bodies[id], out)
	actions := g.script[id]
	if len(actions) == 0 {
		return "", fmt.Errorf("the scenario scripted no network answer for the outcome of %s", id)
	}
	act := actions[0]
	g.script[id] = actions[1:]
	switch act.kind {
	case "accept":
		return act.tx, nil
	case "released":
		return "", bddOutcomeRefusalError{slug: act.slug, released: true}
	case "final":
		return "", bddOutcomeRefusalError{slug: act.slug, released: false}
	case "bare":
		return "", fmt.Errorf("%w: 422 without a problem document", ports.ErrOutcomeRefused)
	default: // unreachable
		return "", errors.New("network unreachable")
	}
}

// OutcomeTransaction reads the scripted verdict of a transaction.
func (g *scriptedReturnsGateway) OutcomeTransaction(_ context.Context, transactionID string) (views.ReturnOutcomeTransaction, error) {
	tx, ok := g.txs[transactionID]
	if !ok {
		return views.ReturnOutcomeTransaction{}, fmt.Errorf("the scenario scripted no verdict for transaction %s", transactionID)
	}
	return tx, nil
}

// upsert replaces the network's truth for one authorization (re-delivery).
func (g *scriptedReturnsGateway) upsert(a views.ReturnAdvice) { g.advices[a.AuthorizationID] = a }

// registerNetworkAdviceSteps registers the ADR 0012/0013 vocabulary: the
// network's scripted answers, the poll and drain ticks, and the assertions
// on the advice view and the stored outcome.
func registerNetworkAdviceSteps(sc *godog.ScenarioContext, w *world) {
	registerNetworkTruthSteps(sc, w)
	registerNetworkTickSteps(sc, w)
	registerNetworkPollAssertions(sc, w)
	registerNetworkOutcomeAssertions(sc, w)
}

// registerNetworkTruthSteps group of the network-advice vocabulary.
func registerNetworkTruthSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the network hands the poller an advice$`, func(table *godog.Table) error {
		a, err := adviceFromTable(w, table)
		if err != nil {
			return err
		}
		w.netgw.upsert(a)
		return nil
	})

	sc.Step(`^the network accepts the outcome of (\S+) with transaction (\S+)$`,
		func(authID, tx string) error {
			w.netgw.script[authID] = append(w.netgw.script[authID], netAction{kind: "accept", tx: tx})
			return nil
		})

	sc.Step(`^the network refuses the outcome of (\S+) with slug (\S+) as (released|final)$`,
		func(authID, slug, class string) error {
			kind := "final"
			if class == "released" {
				kind = "released"
			}
			w.netgw.script[authID] = append(w.netgw.script[authID], netAction{kind: kind, slug: slug})
			return nil
		})

	sc.Step(`^the network refuses the outcome of (\S+) without a problem document$`,
		func(authID string) error {
			w.netgw.script[authID] = append(w.netgw.script[authID], netAction{kind: "bare"})
			return nil
		})

	sc.Step(`^the network cannot reach the outcome of (\S+)$`,
		func(authID string) error {
			w.netgw.script[authID] = append(w.netgw.script[authID], netAction{kind: "unreachable"})
			return nil
		})
}

// registerNetworkTickSteps group of the network-advice vocabulary.
func registerNetworkTickSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the network transaction (\S+) is (PROCESSING|SUCCEEDED|FAILED)( with errors (.+))?$`,
		func(tx, status, _, errorsText string) error {
			w.netgw.txs[tx] = views.ReturnOutcomeTransaction{Status: status, Errors: errorsText}
			return nil
		})

	// The two worker ticks.
	sc.Step(`^a poll tick runs$`, func() error {
		w.run(func() error {
			var err error
			w.pollOut, err = w.poll.Execute(w.ctx)
			return err
		})
		return nil
	})

	sc.Step(`^a drain tick runs$`, func() error {
		w.run(func() error {
			var err error
			w.drainOut, err = w.drain.Execute(w.ctx)
			return err
		})
		return nil
	})
}

// registerNetworkPollAssertions group of the network-advice vocabulary.
func registerNetworkPollAssertions(sc *godog.ScenarioContext, w *world) {
	registerNetworkPollAssertionsPart1(sc, w)
	registerNetworkPollAssertionsPart2(sc, w)
}

// registerNetworkPollAssertionsPart1 of the network-advice vocabulary.
func registerNetworkPollAssertionsPart1(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the clock moves past the retry window$`, func() error {
		w.clock.now = time.Now().UTC().Add(25 * time.Hour)
		return nil
	})

	// Poll assertions.
	sc.Step(`^the poll tick saw (\d+) advice and applied (\d+)$`, func(seen, applied int) error {
		if w.pollOut.Seen != seen {
			return fmt.Errorf("poll saw %d advice, want %d", w.pollOut.Seen, seen)
		}
		if w.pollOut.Applied != applied {
			return fmt.Errorf("poll applied %d advice, want %d", w.pollOut.Applied, applied)
		}
		return nil
	})
}

// registerNetworkPollAssertionsPart2 of the network-advice vocabulary.
func registerNetworkPollAssertionsPart2(sc *godog.ScenarioContext, w *world) {
	registerNetworkPollAssertionsPart2S1(sc, w)
	registerNetworkPollAssertionsPart2S2(sc, w)
}

func registerNetworkPollAssertionsPart2S1(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^poll number (\d+) started from (.+)$`, func(n int, raw string) error {
		if n < 1 || n > len(w.netgw.polls) {
			return fmt.Errorf("%d poll(s) ran, want number %d", len(w.netgw.polls), n)
		}
		got := w.netgw.polls[n-1]
		if raw == "the beginning" {
			if !got.IsZero() {
				return fmt.Errorf("poll %d started from %s, want the beginning", n, got.Format(time.RFC3339))
			}
			return nil
		}
		want, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return err
		}
		if !got.Equal(want) {
			return fmt.Errorf("poll %d started from %s, want %s", n, got.Format(time.RFC3339), want.Format(time.RFC3339))
		}
		return nil
	})
}

func registerNetworkPollAssertionsPart2S2(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the advice (\S+) is recorded with state (\S+), disposition (\S+) and tracking (.+)$`,
		func(authID, state, disp, tracking string) error {
			a, ok, err := w.advices.Get(w.ctx, authID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no advice %s was ever polled", authID)
			}
			if a.State != state {
				return fmt.Errorf("advice %s state = %q, want %q", authID, a.State, state)
			}
			if a.Disposition != disp {
				return fmt.Errorf("advice %s disposition = %q, want %q", authID, a.Disposition, disp)
			}
			if got := strings.Join(a.TrackingNumbers, ","); got != tracking {
				return fmt.Errorf("advice %s tracking = %q, want %q", authID, got, tracking)
			}
			return nil
		})
}

// registerNetworkOutcomeAssertions group of the network-advice vocabulary.
func registerNetworkOutcomeAssertions(sc *godog.ScenarioContext, w *world) {
	registerNetworkOutcomeAssertionsPart1(sc, w)
	registerNetworkOutcomeAssertionsPart2(sc, w)
	registerNetworkOutcomeAssertionsPart3(sc, w)
}

// registerNetworkOutcomeAssertionsPart1 of the network-advice vocabulary.
func registerNetworkOutcomeAssertionsPart1(sc *godog.ScenarioContext, w *world) {
	registerNetworkOutcomeAssertionsPart1S1(sc, w)
	registerNetworkOutcomeAssertionsPart1S2(sc, w)
	registerNetworkOutcomeAssertionsPart1S3(sc, w)
	registerNetworkOutcomeAssertionsPart1S4(sc, w)
}

func registerNetworkOutcomeAssertionsPart1S1(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the advice (\S+) has (\d+) authorized lines?$`, func(authID string, want int) error {
		a, ok, err := w.advices.Get(w.ctx, authID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no advice %s was ever polled", authID)
		}
		return wantInt("authorized lines of "+authID, len(a.Lines), want)
	})
}

func registerNetworkOutcomeAssertionsPart1S2(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the advice (\S+) outcome state is (none|PENDING|SUBMITTED|SUCCEEDED|FAILED)$`,
		func(authID, want string) error {
			a, ok, err := w.advices.Get(w.ctx, authID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("no advice %s was ever polled", authID)
			}
			if want == "none" {
				want = ""
			}
			if a.OutcomeState != want {
				return fmt.Errorf("advice %s outcome state = %q, want %q", authID, a.OutcomeState, want)
			}
			return nil
		})
}

func registerNetworkOutcomeAssertionsPart1S3(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^no outcome is pending$`, func() error {
		pending, err := w.advices.PendingOutcomes(w.ctx)
		if err != nil {
			return err
		}
		if len(pending) != 0 {
			return fmt.Errorf("%d outcome(s) pending, want none", len(pending))
		}
		return nil
	})
}

func registerNetworkOutcomeAssertionsPart1S4(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^one outcome is pending with lines$`, func(table *godog.Table) error {
		pending, err := w.advices.PendingOutcomes(w.ctx)
		if err != nil {
			return err
		}
		if len(pending) != 1 {
			return fmt.Errorf("%d outcome(s) pending, want exactly 1", len(pending))
		}
		lines := pending[0].Outcome.Lines
		if len(table.Rows)-1 != len(lines) {
			return fmt.Errorf("the pending outcome has %d line(s), the table lists %d", len(lines), len(table.Rows)-1)
		}
		for i, row := range table.Rows {
			if i == 0 {
				continue // header: item | owner | restockable | unsellable
			}
			got := lines[i-1]
			want := row.Cells
			if got.CatalogItemID != want[0].Value || got.OwnerRef != want[1].Value ||
				strconv.FormatInt(got.QtyRestockable, 10) != want[2].Value || strconv.FormatInt(got.QtyUnsellable, 10) != want[3].Value {
				return fmt.Errorf("outcome line %d = %s:%s:restock %d:unsell %d, want %s:%s:restock %s:unsell %s",
					i, got.CatalogItemID, got.OwnerRef, got.QtyRestockable, got.QtyUnsellable,
					want[0].Value, want[1].Value, want[2].Value, want[3].Value)
			}
		}
		return nil
	})
}

// registerNetworkOutcomeAssertionsPart2 of the network-advice vocabulary.
func registerNetworkOutcomeAssertionsPart2(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the one pending outcome is for authorization (\S+) at node (\S+) received at (.+)$`,
		func(authID, node, raw string) error {
			want, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return err
			}
			pending, err := w.advices.PendingOutcomes(w.ctx)
			if err != nil {
				return err
			}
			if len(pending) != 1 {
				return fmt.Errorf("%d outcome(s) pending, want exactly 1", len(pending))
			}
			out := pending[0].Outcome
			if out.AuthorizationID != authID {
				return fmt.Errorf("pending outcome authorization = %q, want %q", out.AuthorizationID, authID)
			}
			if out.NodeID != node {
				return fmt.Errorf("pending outcome node = %q, want %q", out.NodeID, node)
			}
			return wantTime("pending outcome received at", out.ReceivedAt, want)
		})

	sc.Step(`^the one pending outcome carries transaction (\S+)$`, func(tx string) error {
		pending, err := w.advices.PendingOutcomes(w.ctx)
		if err != nil {
			return err
		}
		if len(pending) != 1 {
			return fmt.Errorf("%d outcome(s) pending, want exactly 1", len(pending))
		}
		return wantStr("pending outcome transaction", pending[0].TransactionID, tx)
	})

	sc.Step(`^the recorded refusal of (\S+) mentions (.+)$`, func(authID, text string) error {
		if got := w.advices.Refusal(authID); !strings.Contains(got, text) {
			return fmt.Errorf("recorded refusal of %s = %q, want it to mention %q", authID, got, text)
		}
		return nil
	})

	sc.Step(`^the recorded refusal of (\S+) is empty$`, func(authID string) error {
		if got := w.advices.Refusal(authID); got != "" {
			return fmt.Errorf("recorded refusal of %s = %q, want empty", authID, got)
		}
		return nil
	})
}

// registerNetworkOutcomeAssertionsPart3 of the network-advice vocabulary.
func registerNetworkOutcomeAssertionsPart3(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the drain tick submitted (\d+), settled (\d+) and failed (\d+)$`,
		func(submitted, settled, failed int) error {
			r := w.drainOut
			if r.Submitted != submitted || r.Settled != settled || r.Failed != failed {
				return fmt.Errorf("drain submitted %d, settled %d, failed %d; want %d/%d/%d",
					r.Submitted, r.Settled, r.Failed, submitted, settled, failed)
			}
			return nil
		})

	sc.Step(`^the drain tick released (\S+) for (\S+)$`, func(authID, slug string) error {
		for _, rel := range w.drainOut.Released {
			if rel.AuthorizationID == authID && rel.Slug == slug {
				return nil
			}
		}
		return fmt.Errorf("the drain released %v, want %s for %s", w.drainOut.Released, slug, authID)
	})

	sc.Step(`^the drain tick failed with a transport error$`, func() error {
		w.errChecked = true
		if w.lastErr == nil {
			return errors.New("the drain tick succeeded, want a transport error")
		}
		return nil
	})

	sc.Step(`^the network received (\d+) identical outcome bodies for (\S+)$`,
		func(want int, authID string) error {
			bodies := w.netgw.bodies[authID]
			if len(bodies) != want {
				return fmt.Errorf("the network received %d outcome bodies of %s, want %d", len(bodies), authID, want)
			}
			for i := 1; i < len(bodies); i++ {
				if fmt.Sprint(bodies[i]) != fmt.Sprint(bodies[0]) {
					return fmt.Errorf("submission %d of %s changed the body", i+1, authID)
				}
			}
			return nil
		})
}

// adviceFromTable builds a views.ReturnAdvice from a field|value table.
func adviceFromTable(w *world, table *godog.Table) (views.ReturnAdvice, error) {
	a := views.ReturnAdvice{State: views.AdviceStateAuthorized, ReadyAt: w.clock.now}
	for i, row := range table.Rows {
		if i == 0 {
			continue // header: field | value
		}
		field, value := row.Cells[0].Value, row.Cells[1].Value
		set, ok := adviceFieldSetters(w, &a)[field]
		if !ok {
			return a, fmt.Errorf("advice table: unknown field %q", field)
		}
		if err := set(value); err != nil {
			return a, err
		}
	}
	return a, nil
}

// adviceFieldSetters maps an advice table field to its setter.
func adviceFieldSetters(w *world, a *views.ReturnAdvice) map[string]func(string) error {
	return map[string]func(string) error{
		"authorizationId": func(v string) error { a.AuthorizationID = v; return nil },
		"order":           func(v string) error { a.OrderID = v; return nil },
		"group":           func(v string) error { g, err := strconv.ParseInt(v, 10, 64); a.GroupNo = g; return err },
		"vendor":          func(v string) error { a.VendorID = v; return nil },
		"node":            func(v string) error { a.NodeID = v; return nil },
		"disposition":     func(v string) error { a.Disposition = v; return nil },
		"method":          func(v string) error { a.Method = v; return nil },
		"state":           func(v string) error { a.State = v; return nil },
		"tracking":        func(v string) error { a.TrackingNumbers = splitList(v); return nil },
		"readyAt":         func(v string) error { r, err := time.Parse(time.RFC3339, v); a.ReadyAt = r; return err },
		"lines":           func(v string) error { ls, err := adviceLinesFrom(v); a.Lines = ls; return err },
	}
}

// adviceLinesFrom parses a comma list of item:qty:owner advice lines.
func adviceLinesFrom(value string) ([]views.ReturnAdviceLine, error) {
	var lines []views.ReturnAdviceLine
	for _, part := range strings.Split(value, ",") {
		bits := strings.SplitN(part, ":", 3)
		if len(bits) != 3 {
			return nil, fmt.Errorf("advice line %q is not item:qty:owner", part)
		}
		qty, err := strconv.ParseInt(bits[1], 10, 64)
		if err != nil {
			return nil, err
		}
		lines = append(lines, views.ReturnAdviceLine{CatalogItemID: bits[0], Quantity: qty, OwnerRef: bits[2]})
	}
	return lines, nil
}
