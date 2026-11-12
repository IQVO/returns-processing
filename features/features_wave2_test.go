package features_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/cucumber/godog"

	"github.com/claudioed/returns-processing/internal/analytics/report"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// features_wave2_test.go — the wave-2 step vocabulary: the REST edge through
// the real chi router (every operation of apis/openapi.yaml, the RFC 7807
// problem catalogue and the Idempotency-Key middleware), the ADR 0003
// Kafka-fact consumers, and the ADR 0007 analytics reports through the real
// reports router. The JSON steps are GENERIC (raw body sent verbatim, dotted
// response paths), so no step per field is needed.

// do sends one request to one of the scenario's servers and records the
// response. A 2xx body that identifies a resource is remembered, so the
// domain-worded assertion steps keep working after REST commands.
func (w *world) do(srv, method, path, key, body string) error {
	base := w.httpSrv.URL
	if srv == "reports" {
		base = w.reportsSrv.URL
	}
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(w.ctx, method, base+path, rd)
	if err != nil {
		return err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	w.lastStatus, w.lastHeader, w.lastBody = resp.StatusCode, resp.Header, raw
	w.rememberIDs(path, resp.StatusCode, raw)
	return nil
}

// rememberIDs tracks the rmaId / receiptId a 2xx response carries, so the
// shared assertion steps (the rma status is ..., receipt line N ...) see the
// resource the REST commands created.
func (w *world) rememberIDs(path string, status int, raw []byte) {
	if status >= 300 {
		return
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return
	}
	if id, ok := m["rmaId"].(string); ok && id != "" && strings.HasPrefix(path, "/returns") {
		w.rmaID = id
	}
	if id, ok := m["receiptId"].(string); ok && id != "" {
		w.receiptID = id
	}
}

// field reads a dotted path ("a.b.0.c") of the last response body.
func (w *world) field(path string) (any, bool) {
	var v any
	if json.Unmarshal(w.lastBody, &v) != nil {
		return nil, false
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		switch node := cur.(type) {
		case map[string]any:
			next, ok := node[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			cur = node[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

func registerWave2Steps(sc *godog.ScenarioContext, w *world) {
	registerRESTSteps(sc, w)
	registerConsumerSteps(sc, w)
	registerReportSteps(sc, w)
}

// registerRESTSteps: raw requests against the API server and assertions on
// status, problem slug, headers and dotted response fields.
func registerRESTSteps(sc *godog.ScenarioContext, w *world) {
	registerRequestSteps(sc, w)
	registerResponseSteps(sc, w)
}

// registerRequestSteps registers the raw request steps (the body goes out
// verbatim, so malformed JSON and NUL bytes are expressible).
func registerRequestSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^a (POST|PUT) request is sent to "([^"]+)" with the JSON body (.*)$`,
		func(method, path, body string) error { return w.do("api", method, path, "", body) })
	sc.Step(`^a (POST|PUT) request is sent to "([^"]+)" with key "([^"]+)" and the JSON body (.*)$`,
		func(method, path, key, body string) error { return w.do("api", method, path, key, body) })
	sc.Step(`^a GET request is sent to "([^"]+)"$`,
		func(path string) error { return w.do("api", http.MethodGet, path, "", "") })
}

// registerResponseSteps registers the assertions on the last response.
func registerResponseSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response status is (\d+)$`, func(want int) error {
		if w.lastStatus != want {
			return fmt.Errorf("status = %d, want %d (body: %s)", w.lastStatus, want, w.lastBody)
		}
		return nil
	})
	sc.Step(`^the response problem is "([^"]+)"$`, func(slug string) error {
		v, ok := w.field("type")
		if !ok {
			return fmt.Errorf("no problem type in body: %s", w.lastBody)
		}
		if got, _ := v.(string); !strings.HasSuffix(got, "/"+slug) {
			return fmt.Errorf("problem type = %q, want slug %q", got, slug)
		}
		return nil
	})
	sc.Step(`^the response header "([^"]+)" is "([^"]+)"$`, func(name, want string) error {
		if got := w.lastHeader.Get(name); got != want {
			return fmt.Errorf("header %s = %q, want %q", name, got, want)
		}
		return nil
	})
	registerBodySteps(sc, w)
}

// registerBodySteps registers the dotted-path assertions of the last
// response body.
func registerBodySteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the response field "([^"]+)" is (.+)$`, func(path, want string) error {
		v, ok := w.field(path)
		if !ok {
			return fmt.Errorf("no field %q in body: %s", path, w.lastBody)
		}
		if got := scalar(v); got != want {
			return fmt.Errorf("field %q = %q, want %q (body: %s)", path, got, want, w.lastBody)
		}
		return nil
	})
	sc.Step(`^the response list "([^"]*)" has (\d+) items$`, func(path string, want int) error {
		var v any
		var ok bool
		if path == "" {
			v, ok = w.lastList(), true
		} else {
			v, ok = w.field(path)
		}
		if !ok {
			return fmt.Errorf("no list %q in body: %s", path, w.lastBody)
		}
		list, isList := v.([]any)
		if !isList {
			return fmt.Errorf("field %q is not a list: %s", path, w.lastBody)
		}
		if len(list) != want {
			return fmt.Errorf("list %q has %d items, want %d", path, len(list), want)
		}
		return nil
	})
	sc.Step(`^the response has no field "([^"]+)"$`, func(path string) error {
		if _, ok := w.field(path); ok {
			return fmt.Errorf("field %q present in body: %s", path, w.lastBody)
		}
		return nil
	})
}

// lastList returns the body itself when it is a JSON array.
func (w *world) lastList() any {
	var v any
	if json.Unmarshal(w.lastBody, &v) != nil {
		return nil
	}
	return v
}

// scalar renders a decoded JSON value the way the feature files write it.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return "null"
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// registerConsumerSteps: the ADR 0003 facts arrive through the same consume
// use cases the inbound Kafka adapters call.
func registerConsumerSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^an allocation fact (\S+) for order (\S+) is consumed with lines$`,
		func(eventID, orderID string, table *godog.Table) error {
			var lines []usecases.AllocatedLineIn
			for i, row := range table.Rows {
				if i == 0 {
					continue // header
				}
				lineNo, err := strconv.Atoi(row.Cells[0].Value)
				if err != nil {
					return err
				}
				lines = append(lines, usecases.AllocatedLineIn{LineNo: int32(lineNo), SKU: row.Cells[1].Value})
			}
			var out usecases.ConsumeOutcome
			w.run(func() error {
				var err error
				out, err = w.consumers.allocations.Handle(w.ctx, eventID, orderID, lines)
				return err
			})
			w.consumeOut = out
			return nil
		})
	sc.Step(`^a manifest fact (\S+) for work unit (\S+) in package (\S+) is consumed$`,
		func(eventID, workUnit, pkg string) error {
			var out usecases.ConsumeOutcome
			w.run(func() error {
				var err error
				out, err = w.consumers.manifest.Handle(w.ctx, eventID, workUnit, pkg, w.clock.now)
				return err
			})
			w.consumeOut = out
			return nil
		})
	sc.Step(`^a classification fact (\S+) for sku (\S+) version (\d+) is consumed with tags (.+)$`,
		func(eventID, sku string, version int, raw string) error {
			pc := views.ProductClass{Version: int64(version)}
			for _, t := range strings.Split(raw, ",") {
				pc.Tags = append(pc.Tags, strings.TrimSpace(t))
			}
			var out usecases.ConsumeOutcome
			w.run(func() error {
				var err error
				out, err = w.consumers.classification.Handle(w.ctx, eventID, sku, pc)
				return err
			})
			w.consumeOut = out
			return nil
		})
	sc.Step(`^the consume outcome is (\S+)$`, func(want string) error {
		if string(w.consumeOut) != want {
			return fmt.Errorf("consume outcome = %q, want %q", w.consumeOut, want)
		}
		return nil
	})
}

// registerReportSteps: the analytics projection (the same fold the inbound
// analytics consumer applies to the CloudEvents the outbox relay publishes)
// and the reports read side.
func registerReportSteps(sc *godog.ScenarioContext, w *world) {
	sc.Step(`^the published events are projected to the analytics store$`, func() error {
		return w.projectAll()
	})
	sc.Step(`^a reports GET request is sent to "([^"]+)"$`,
		func(path string) error { return w.do("reports", http.MethodGet, path, "", "") })
}

// projectAll folds every recorded domain event into the in-memory
// analytical store, mirroring the consumer's CloudEvent -> report.Event
// mapping (toReportEvent) one for one. Synthesised event ids are unique, so
// every fact applies exactly once.
func (w *world) projectAll() error {
	seq := 0
	apply := func(e report.Event) error {
		seq++
		e.EventID = fmt.Sprintf("bdd-evt-%04d", seq)
		applied, err := w.analytics.Apply(w.ctx, e)
		if err != nil {
			return fmt.Errorf("project %s: %w", e.Kind, err)
		}
		if !applied {
			return fmt.Errorf("project %s: id %s reported a replay", e.Kind, e.EventID)
		}
		return nil
	}
	for _, ev := range w.ev.Receipt {
		if err := w.projectReceipt(apply, ev); err != nil {
			return err
		}
	}
	for _, ev := range w.ev.Disposition {
		if err := w.projectDisposition(apply, ev); err != nil {
			return err
		}
	}
	return nil
}

// projectReceipt maps one receipt fact, one for one with toReportEvent.
func (w *world) projectReceipt(apply func(report.Event) error, ev returnreceipt.Event) error {
	switch e := ev.(type) {
	case returnreceipt.ReturnReceiptOpened:
		pe := report.Event{Kind: report.KindReturnReceiptOpened, At: e.OccurredAt(), SiteID: e.SiteID,
			ReceiptID: e.ReceiptID(), RMAID: e.RMAID}
		for _, l := range e.Lines {
			pe.Lines = append(pe.Lines, report.OpenedLine{LineNo: l.LineNo, SKU: l.SKU, Quantity: l.Quantity, RMAID: l.RMAID})
		}
		return apply(pe)
	case returnreceipt.UnexpectedReturnReceived:
		return apply(report.Event{Kind: report.KindUnexpectedReturnReceived, At: e.OccurredAt(),
			SiteID: e.SiteID, ReceiptID: e.ReceiptID(), LineNo: e.LineNo, SKU: e.SKU, Quantity: e.Quantity})
	case returnreceipt.ReturnLineInspected:
		return apply(report.Event{Kind: report.KindReturnLineInspected, At: e.OccurredAt(),
			ReceiptID: e.ReceiptID(), LineNo: e.LineNo, SKU: e.SKU, Quantity: e.Quantity, Grade: e.Grade})
	}
	return nil
}

// projectDisposition maps one disposition fact, one for one with
// toReportEvent.
func (w *world) projectDisposition(apply func(report.Event) error, ev disposition.Event) error {
	switch e := ev.(type) {
	case disposition.DispositionDecided:
		return apply(report.Event{Kind: report.KindDispositionDecided, At: e.OccurredAt(),
			DispositionID: e.DispositionID(), ReceiptID: e.ReceiptID, LineNo: e.LineNo, SKU: e.SKU,
			Quantity: e.Quantity, Grade: e.Grade, Outcome: string(e.Outcome), PolicyVersion: e.PolicyVersion,
			Supersedes: e.Supersedes, Reason: e.Reason})
	case disposition.ReturnLineStaged:
		return apply(report.Event{Kind: report.KindReturnLineStaged, At: e.OccurredAt(), SiteID: e.SiteID,
			DispositionID: e.DispositionID(), ReceiptID: e.ReceiptID, LineNo: e.LineNo, SKU: e.SKU,
			Quantity: e.Quantity, RMAID: e.RMAID})
	}
	return nil
}
