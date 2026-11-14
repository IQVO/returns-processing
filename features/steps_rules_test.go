package features_test

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/returns-processing/internal/application/usecases"
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
