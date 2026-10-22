package kafka

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/returns-processing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/returns-processing/internal/analytics/report"
)

// headerValue returns the value of a Kafka header, "" when absent.
func headerValue(msg kafkago.Message, key string) string {
	for _, h := range msg.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func analyticsEnvelope(t *testing.T, id, entity, name, subject string, at time.Time, data any) kafkago.Message {
	t.Helper()
	value, err := cloudevents.New(cloudevents.Spec{
		ID: id, Entity: entity, EventName: name, Subject: subject, Time: at,
		Stream: cloudevents.StreamAnalytics, Version: 1, Data: data,
	})
	if err != nil {
		t.Fatal(err)
	}
	return kafkago.Message{Topic: "t.analytics", Key: []byte(subject), Value: value, Headers: []kafkago.Header{cloudevents.ContentTypeHeader()}}
}

// fakeProjection records applies: a replay reports applied=false, a forced
// error surfaces verbatim.
type fakeProjection struct {
	applied []report.Event
	replay  map[string]bool
	err     error
}

func newFakeProjection() *fakeProjection { return &fakeProjection{replay: map[string]bool{}} }

func (p *fakeProjection) Apply(_ context.Context, e report.Event) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	if p.replay[e.EventID] {
		return false, nil
	}
	p.replay[e.EventID] = true
	p.applied = append(p.applied, e)
	return true, nil
}

func newTestAnalyticsConsumer(r Reader, p report.Projection, dlq DeadLetterWriter) *AnalyticsConsumer {
	return &AnalyticsConsumer{
		Reader: r, Projection: p, DLQ: dlq, DLQTopic: "t.analytics.dlq",
		Logger: quietLogger(), Retry: RetryPolicy{Initial: time.Millisecond, Max: 5 * time.Millisecond},
	}
}

// The five projected kinds decode, validate and reach the projection with
// the payload mapped onto the report event.
func TestAnalyticsConsumerProjectsTheFiveKinds(t *testing.T) {
	at := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	msgs := []kafkago.Message{
		analyticsEnvelope(t, "e1", analyticsEntityReceipt, "ReturnReceiptOpened", "rcpt-1", at, map[string]any{
			"receipt_id": "rcpt-1", "rma_id": nil, "site_id": "SITE-1", "opened_at": at.Format(time.RFC3339),
			"lines": []map[string]any{{"line_no": 1, "sku": "SKU-1", "quantity": 2, "rma_id": "rma-1"}},
		}),
		analyticsEnvelope(t, "e2", analyticsEntityReceipt, "UnexpectedReturnReceived", "rcpt-1", at, map[string]any{
			"receipt_id": "rcpt-1", "line_no": 2, "sku": "SKU-X", "quantity": 1, "site_id": "SITE-1",
			"received_at": at.Format(time.RFC3339),
		}),
		analyticsEnvelope(t, "e3", analyticsEntityReceipt, "ReturnLineInspected", "rcpt-1", at.Add(5*time.Minute), map[string]any{
			"receipt_id": "rcpt-1", "line_no": 1, "sku": "SKU-1", "quantity": 2, "grade": "A",
			"reason_codes": []string{"like-new"}, "inspected_at": at.Add(5 * time.Minute).Format(time.RFC3339),
		}),
		analyticsEnvelope(t, "e4", analyticsEntityDisposition, "DispositionDecided", "disp-1", at.Add(6*time.Minute), map[string]any{
			"disposition_id": "disp-1", "receipt_id": "rcpt-1", "line_no": 1, "sku": "SKU-1", "quantity": 2,
			"grade": "A", "outcome": "RESTOCK", "policy_version": 3, "supersedes": nil, "reason": nil,
			"decided_at": at.Add(6 * time.Minute).Format(time.RFC3339),
		}),
		analyticsEnvelope(t, "e5", analyticsEntityDisposition, "ReturnLineStaged", "disp-1", at.Add(7*time.Minute), map[string]any{
			"receipt_id": "rcpt-1", "line_no": 1, "sku": "SKU-1", "quantity": 2, "condition": "Good",
			"received_at": at.Format(time.RFC3339), "rma_id": "rma-1", "disposition_id": "disp-1", "site_id": "SITE-1",
		}),
	}
	proj := newFakeProjection()
	c := newTestAnalyticsConsumer(&fakeReader{messages: msgs}, proj, &fakeDLQ{})
	for _, m := range msgs {
		if err := c.HandleMessage(context.Background(), m); err != nil {
			t.Fatalf("handle %s: %v", m.Topic, err)
		}
	}
	if len(proj.applied) != 5 {
		t.Fatalf("applied = %d events, want 5", len(proj.applied))
	}
	wantKinds := []report.Kind{report.KindReturnReceiptOpened, report.KindUnexpectedReturnReceived, report.KindReturnLineInspected, report.KindDispositionDecided, report.KindReturnLineStaged}
	for i, want := range wantKinds {
		if proj.applied[i].Kind != want {
			t.Fatalf("applied[%d] = %s, want %s", i, proj.applied[i].Kind, want)
		}
	}
	opened := proj.applied[0]
	if opened.SiteID != "SITE-1" || len(opened.Lines) != 1 || opened.Lines[0].RMAID != "rma-1" {
		t.Fatalf("opened event = %+v", opened)
	}
	decided := proj.applied[3]
	if decided.DispositionID != "disp-1" || decided.Outcome != "RESTOCK" || decided.PolicyVersion != 3 {
		t.Fatalf("decided event = %+v", decided)
	}
}

// The two authorization events are mirrored on the topic but projected by
// nothing: they are acknowledged and ignored, not dead-lettered.
func TestAnalyticsConsumerIgnoresUnprojectedTypes(t *testing.T) {
	at := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	msgs := []kafkago.Message{
		analyticsEnvelope(t, "a1", analyticsEntityRMA, "ReturnAuthorized", "rma-1", at, map[string]any{
			"rma_id": "rma-1", "order_id": "ord-1", "line_no": 1, "sku": "SKU-1", "quantity": 2,
			"site_id": "SITE-1", "reason_code": "DAMAGED",
			"window_ends_at": at.Add(48 * time.Hour).Format(time.RFC3339), "authorized_at": at.Format(time.RFC3339),
		}),
		analyticsEnvelope(t, "a2", analyticsEntityRMA, "ReturnAuthorizationRejected", "rma-2", at, map[string]any{
			"rma_id": "rma-2", "order_id": "ord-1", "line_no": 2, "sku": "SKU-9", "quantity": 5,
			"rejection_code": "LINE_NOT_MANIFESTED", "rejected_at": at.Format(time.RFC3339),
		}),
	}
	proj := newFakeProjection()
	dlq := &fakeDLQ{}
	c := newTestAnalyticsConsumer(&fakeReader{messages: msgs}, proj, dlq)
	for _, m := range msgs {
		if err := c.HandleMessage(context.Background(), m); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}
	if len(proj.applied) != 0 || len(dlq.deadLettered()) != 0 {
		t.Fatalf("applied = %d, dead-lettered = %d; want both zero", len(proj.applied), len(dlq.deadLettered()))
	}
}

// A message that is not a CloudEvents 1.0 event is skipped and committed
// past, never dead-lettered.
func TestAnalyticsConsumerSkipsNonCloudEvents(t *testing.T) {
	proj := newFakeProjection()
	dlq := &fakeDLQ{}
	c := newTestAnalyticsConsumer(&fakeReader{}, proj, dlq)
	if err := c.HandleMessage(context.Background(), kafkago.Message{Topic: "t.analytics", Value: []byte("not json")}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(proj.applied) != 0 || len(dlq.deadLettered()) != 0 {
		t.Fatalf("applied = %d, dead-lettered = %d; want both zero", len(proj.applied), len(dlq.deadLettered()))
	}
}

// A known type whose payload contradicts its subject can never be projected:
// dead-lettered at once, raw bytes and x-dlq-error included.
func TestAnalyticsConsumerDeadLettersSubjectMismatch(t *testing.T) {
	at := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	tampered := analyticsEnvelope(t, "bad-1", analyticsEntityDisposition, "ReturnLineStaged", "disp-OTHER", at, map[string]any{
		"receipt_id": "rcpt-1", "line_no": 1, "sku": "SKU-1", "quantity": 2, "condition": "Good",
		"received_at": at.Format(time.RFC3339), "rma_id": "rma-1", "disposition_id": "disp-1", "site_id": "SITE-1",
	})
	proj := newFakeProjection()
	dlq := &fakeDLQ{}
	c := newTestAnalyticsConsumer(&fakeReader{messages: []kafkago.Message{tampered}}, proj, dlq)
	if err := c.HandleMessage(context.Background(), tampered); err != nil {
		t.Fatalf("a poison message must not surface an error: %v", err)
	}
	dead := dlq.deadLettered()
	if len(dead) != 1 || string(dead[0].Value) != string(tampered.Value) || headerValue(dead[0], "x-dlq-error") == "" {
		t.Fatalf("dlq = %+v", dead)
	}
	if len(proj.applied) != 0 {
		t.Fatalf("applied = %d, want 0", len(proj.applied))
	}
}

// A payload the store deterministically rejects (report.ErrRejected) is
// dead-lettered; a transient error is returned for the loop to retry the
// SAME message, never dead-lettered.
func TestAnalyticsConsumerSeparatedRejectedFromTransient(t *testing.T) {
	at := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	msg := analyticsEnvelope(t, "e1", analyticsEntityReceipt, "UnexpectedReturnReceived", "rcpt-1", at, map[string]any{
		"receipt_id": "rcpt-1", "line_no": 1, "sku": "SKU-X", "quantity": 1, "site_id": "SITE-1",
		"received_at": at.Format(time.RFC3339),
	})

	proj := newFakeProjection()
	proj.err = fmt.Errorf("%w: boom", report.ErrRejected)
	dlq := &fakeDLQ{}
	c := newTestAnalyticsConsumer(&fakeReader{}, proj, dlq)
	if err := c.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("rejected event must dead-letter, not error: %v", err)
	}
	if len(dlq.deadLettered()) != 1 {
		t.Fatalf("dead-lettered = %d, want 1", len(dlq.deadLettered()))
	}

	proj2 := newFakeProjection()
	proj2.err = errors.New("connection refused")
	dlq2 := &fakeDLQ{}
	c2 := newTestAnalyticsConsumer(&fakeReader{}, proj2, dlq2)
	if err := c2.HandleMessage(context.Background(), msg); err == nil {
		t.Fatal("a transient failure must surface an error so the loop retries the same message")
	}
	if len(dlq2.deadLettered()) != 0 {
		t.Fatalf("a transient failure was dead-lettered: %+v", dlq2.deadLettered())
	}
}

// The run loop folds every message exactly once (a replayed id is a no-op at
// the store) and commits each offset after success.
func TestAnalyticsConsumerRunCommitsPastEverything(t *testing.T) {
	at := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	good := analyticsEnvelope(t, "e1", analyticsEntityReceipt, "UnexpectedReturnReceived", "rcpt-1", at, map[string]any{
		"receipt_id": "rcpt-1", "line_no": 1, "sku": "SKU-X", "quantity": 1, "site_id": "SITE-1",
		"received_at": at.Format(time.RFC3339),
	})
	good.Offset = 1
	good.Partition = 3
	notCE := kafkago.Message{Topic: "t.analytics", Value: []byte("junk"), Offset: 2, Partition: 3}
	replay := good
	replay.Offset = 3
	reader := &fakeReader{messages: []kafkago.Message{good, notCE, replay}}
	proj := newFakeProjection()
	c := newTestAnalyticsConsumer(reader, proj, &fakeDLQ{})
	if err := c.Run(context.Background()); !errors.Is(err, errNoMoreMessages) {
		t.Fatalf("run = %v, want the parking error after the stream", err)
	}
	if got := reader.committed(); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("commits = %v, want 1, 2, 3", got)
	}
	if len(proj.applied) != 1 {
		t.Fatalf("applied = %d, want 1 (the replay is a no-op)", len(proj.applied))
	}
}

func TestLogSafeStripsLineBreaks(t *testing.T) {
	if got := logSafe("a\r\nb\nc"); got != "abc" {
		t.Fatalf("logSafe = %q, want %q", got, "abc")
	}
}
