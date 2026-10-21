//go:build integration

package kafka_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"

	"github.com/claudioed/returns-processing/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/returns-processing/internal/adapters/outbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
	"github.com/claudioed/returns-processing/internal/application/outbox"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/testing/kafkatest"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// The relay is proven end to end against real infrastructure, both started
// with testcontainers (never skip-gated, never a hardcoded broker address):
// the production Outbox publisher writes rows inside a real unit of work, the
// production OutboxRelay drains them through the production encoder and
// RelaySink to a real broker on explicitly created topics, and the CloudEvents
// are read back and compared with the encoder's golden envelopes — the SAME
// files its unit test pins, so the wire contract has one source of truth.

// The broker itself comes from internal/testing/kafkatest, which starts it
// through testcontainers-go/modules/kafka (referenced here so the module
// import stays reachable from this Kafka-touching file).
var _ = tckafka.Run

// Domain fixtures of the end-to-end proofs, built like the encoder's golden
// cases build theirs (same ids, same instants).
func itAuthorized(t *testing.T) returnauthorization.ReturnAuthorized {
	return returnauthorization.ReturnAuthorized{
		Header:   returnauthorization.Header{RMA: "rma-1", Version: 2, At: at(9, 30)},
		OrderRef: orderLine(t, "ord-7c9e", 3),
		SKU:      "SKU-BOOK-0001", Quantity: 2, Site: "site-1", ReasonCode: "DAMAGED",
		WindowEnds: at(11, 30), Authorized: at(9, 30),
	}
}

func itRejected(t *testing.T) returnauthorization.ReturnAuthorizationRejected {
	return returnauthorization.ReturnAuthorizationRejected{
		Header:   returnauthorization.Header{RMA: "rma-2", Version: 2, At: at(9, 31)},
		OrderRef: orderLine(t, "ord-7c9e", 4),
		SKU:      "SKU-9", Quantity: 5,
		RejectionCode: returnauthorization.RejectLineNotManifested, RejectedAt: at(9, 31),
	}
}

func itOpened() returnreceipt.ReturnReceiptOpened {
	return returnreceipt.ReturnReceiptOpened{
		Header: returnreceipt.Header{Receipt: "rcpt-1", Version: 2, At: at(10, 0)},
		RMAID:  "rma-1", SiteID: "site-1", Opened: at(10, 0),
		Lines: []returnreceipt.OpenedLine{
			{LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, RMAID: "rma-1"},
			{LineNo: 2, SKU: "SKU-X", Quantity: 1},
		},
	}
}

func itInspected() returnreceipt.ReturnLineInspected {
	return returnreceipt.ReturnLineInspected{
		Header: returnreceipt.Header{Receipt: "rcpt-1", Version: 3, At: at(10, 5)},
		LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, Grade: "A",
		ReasonCodes: []string{"like-new"}, Inspected: at(10, 5),
	}
}

func itDecided() disposition.DispositionDecided {
	return disposition.DispositionDecided{
		Header:    disposition.Header{Disposition: "disp-1", Version: 1, At: at(10, 6)},
		ReceiptID: "rcpt-1", LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2, Grade: "A",
		Outcome: disposition.OutcomeRestock, PolicyVersion: 4, DecidedAt: at(10, 6),
	}
}

func itStaged() disposition.ReturnLineStaged {
	return disposition.ReturnLineStaged{
		Header:    disposition.Header{Disposition: "disp-1", Version: 2, At: at(10, 6)},
		ReceiptID: "rcpt-1", LineNo: 1, SKU: "SKU-BOOK-0001", Quantity: 2,
		Condition: disposition.StagedCondition, ReceivedAt: at(10, 0),
		RMAID: "rma-1", SiteID: "site-1",
	}
}

// ids hands out fixed CloudEvents ids, in publication order, so the consumed
// envelopes can be compared with the golden files exactly, id included.
func ids(list ...string) func() string {
	i := 0
	return func() string {
		id := list[i%len(list)]
		i++
		return id
	}
}

// itctx bounds one interaction with Postgres or Kafka (fleet rule: <= 30s).
func itctx(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// assertConsumedGolden checks one consumed Kafka message against its golden
// file: the key, exactly the CloudEvents content-type header, a valid
// CloudEvents 1.0 value and the whole envelope.
func assertConsumedGolden(t *testing.T, msg kafkatest.Message, wantKey, goldenName string) {
	t.Helper()
	if msg.Key != wantKey {
		t.Errorf("%s: key = %q, want %q", goldenName, msg.Key, wantKey)
	}
	if len(msg.Headers) != 1 || msg.Headers["content-type"] != cloudevents.MediaType {
		t.Errorf("%s: headers = %v, want exactly the content-type %q", goldenName, msg.Headers, cloudevents.MediaType)
	}
	if _, err := cloudevents.Decode(msg.Value); err != nil {
		t.Fatalf("%s: not a valid CloudEvent: %v", goldenName, err)
	}
	if got, want := decodeJSON(t, goldenName, msg.Value), readGolden(t, goldenName); !reflect.DeepEqual(got, want) {
		t.Errorf("%s: envelope differs from the golden\n got: %s\nwant: %v", goldenName, msg.Value, want)
	}
}

// TestRelay_RealPostgresAndKafka_PublishesTheExactEnvelopes publishes one
// event per aggregate (two for the two-aggregate cases) through the
// production publisher inside one real unit of work, runs one relay pass and
// asserts the exact CloudEvents JSON on warehouse.returns-processing.events.
func TestRelay_RealPostgresAndKafka_PublishesTheExactEnvelopes(t *testing.T) {
	ctx, cancel := itctx(t)
	defer cancel()

	eventsTopic := kafkatest.Topic(t, outboundkafka.TopicEvents)
	brokers := kafkatest.Brokers(t)
	pool := pgtest.NewPool(t)

	enc := outboundkafka.NewEncoder()
	enc.EventsTopic = eventsTopic

	pub := postgres.NewOutbox(pool)
	pub.NewID = ids(
		"9a7d2e64-1b3f-4c8a-9d5e-2f6b8a4c1e70", // ReturnAuthorized
		"3e8b1c5d-9f4a-4d7e-b3c2-6a1e8f0d9b53", // ReturnReceiptOpened
		"5d2f7a8e-3c6b-4e19-a7d4-8b0c2e5f1a66", // ReturnLineInspected
		"1b6a9d3c-8e47-4c05-b2f8-5d1e9a7c4b02", // DispositionDecided
		"4e7c2f9b-6a13-4d58-9c8e-3b5f1a2d7e94", // ReturnLineStaged
	)
	uow := postgres.NewUnitOfWork(pool)
	err := uow.Do(ctx, func(ctx context.Context) error {
		if err := pub.PublishRMAEvents(ctx, itAuthorized(t)); err != nil {
			return err
		}
		if err := pub.PublishReceiptEvents(ctx, itOpened(), itInspected()); err != nil {
			return err
		}
		return pub.PublishDispositionEvents(ctx, itDecided(), itStaged())
	})
	if err != nil {
		t.Fatalf("publish unit of work: %v", err)
	}
	if n := pgtest.Count(t, pool, "outbox"); n != 5 {
		t.Fatalf("outbox has %d rows before the relay, want 5", n)
	}

	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	relay := postgres.NewOutboxRelay(pool, enc, sink,
		postgres.WithLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil))),
		postgres.WithInterval(time.Millisecond),
		postgres.WithMaxAttempts(postgres.DefaultOutboxMaxAttempts),
	)
	n, err := relay.RelayOnce(ctx)
	if err != nil || n != 5 {
		t.Fatalf("RelayOnce = (%d, %v), want (5, nil)", n, err)
	}
	if n := pgtest.Count(t, pool, "outbox"); n != 0 {
		t.Fatalf("outbox has %d rows after the relay, want 0 (sent rows are deleted)", n)
	}

	got := kafkatest.ReadN(t, eventsTopic, 5, 30*time.Second)
	assertConsumedGolden(t, got[0], "rma-1", "return_authorized.golden.json")
	assertConsumedGolden(t, got[1], "rcpt-1", "return_receipt_opened.golden.json")
	assertConsumedGolden(t, got[2], "rcpt-1", "return_line_inspected.golden.json")
	assertConsumedGolden(t, got[3], "disp-1", "disposition_decided.golden.json")
	assertConsumedGolden(t, got[4], "disp-1", "return_line_staged.golden.json")
}

// poisonPayload is an outbox row the encoder refuses: an event name the
// service does not publish.
const poisonPayload = `{"type":"Bogus","data":{"x":1}}`

// TestRelay_DeadLettersPoisonRowAndLaterRowsStillPublish inserts a poison row
// BEFORE a good one, runs one pass and asserts: the poison row lands on the
// DLQ topic with its payload verbatim and the cause in dlq-reason, the good
// row behind it still publishes, and the outbox ends empty.
func TestRelay_DeadLettersPoisonRowAndLaterRowsStillPublish(t *testing.T) {
	ctx, cancel := itctx(t)
	defer cancel()

	eventsTopic := kafkatest.Topic(t, outboundkafka.TopicEvents)
	dlqTopic := kafkatest.Topic(t, outboundkafka.TopicDLQ)
	brokers := kafkatest.Brokers(t)
	pool := pgtest.NewPool(t)

	if _, err := pool.Exec(ctx, `
		INSERT INTO outbox (event_id, aggreg_type, aggreg_id, payload, occurred_at)
		VALUES ($1, $2, $3, $4, $5)
	`, "poison-event-id", "returnauthorization", "rma-bad", poisonPayload, at(9, 59)); err != nil {
		t.Fatalf("insert poison row: %v", err)
	}
	pub := postgres.NewOutbox(pool)
	pub.NewID = ids("0f5c3a92-7d41-4b6e-8c2f-1a9b8d3e5f42") // ReturnAuthorizationRejected
	if err := pub.PublishRMAEvents(ctx, itRejected(t)); err != nil {
		t.Fatalf("publish good row: %v", err)
	}

	enc := outboundkafka.NewEncoder()
	enc.EventsTopic, enc.DLQTopic = eventsTopic, dlqTopic
	sink := outboundkafka.NewRelaySink(brokers)
	t.Cleanup(func() { _ = sink.Close() })
	relay := postgres.NewOutboxRelay(pool, enc, sink,
		postgres.WithLogger(slog.New(slog.NewTextHandler(&strings.Builder{}, nil))),
		postgres.WithInterval(time.Millisecond),
		postgres.WithMaxAttempts(postgres.DefaultOutboxMaxAttempts),
	)
	n, err := relay.RelayOnce(ctx)
	if err != nil || n != 2 {
		t.Fatalf("RelayOnce = (%d, %v), want (2, nil): the poison row is dead-lettered, the good row published", n, err)
	}
	if n := pgtest.Count(t, pool, "outbox"); n != 0 {
		t.Fatalf("outbox has %d rows after the pass, want 0 (poison left via the DLQ)", n)
	}

	dlq := kafkatest.ReadN(t, dlqTopic, 1, 30*time.Second)[0]
	if dlq.Key != "rma-bad" {
		t.Errorf("dlq key = %q, want rma-bad", dlq.Key)
	}
	var poison, got any
	if err := json.Unmarshal([]byte(poisonPayload), &poison); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(dlq.Value, &got); err != nil {
		t.Fatalf("dlq value is not JSON: %v\n%s", err, dlq.Value)
	}
	if !reflect.DeepEqual(got, poison) {
		t.Errorf("dlq value = %s, want the poison payload verbatim (%s)", dlq.Value, poisonPayload)
	}
	if dlq.Headers["content-type"] != "application/json" {
		t.Errorf("dlq content-type = %q, want application/json", dlq.Headers["content-type"])
	}
	if reason := dlq.Headers["dlq-reason"]; !strings.Contains(reason, "not a published event") {
		t.Errorf("dlq-reason = %q, want it to name the unpublished event", reason)
	}

	// The good row behind the poison one still published, exactly.
	good := kafkatest.ReadN(t, eventsTopic, 1, 30*time.Second)[0]
	assertConsumedGolden(t, good, "rma-2", "return_authorization_rejected.golden.json")
}

// stubSink is a switchable RelaySink double: it fails every send while fail
// is set and records everything it was handed.
type stubSink struct {
	fail     bool
	sent     []outbox.Message
	failWith error
}

func (s *stubSink) Send(_ context.Context, msgs ...outbox.Message) error {
	if s.fail {
		return s.failWith
	}
	s.sent = append(s.sent, msgs...)
	return nil
}

// captureLogger records every log record's message and attributes.
type captureLogger struct {
	records []string
	attrs   []map[string]any
}

func (c *captureLogger) Handle(_ context.Context, r slog.Record) error {
	c.records = append(c.records, r.Message)
	attrs := map[string]any{}
	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		return true
	})
	c.attrs = append(c.attrs, attrs)
	return nil
}

func (c *captureLogger) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (c *captureLogger) WithAttrs(_ []slog.Attr) slog.Handler         { return c }
func (c *captureLogger) WithGroup(_ string) slog.Handler              { return c }

// forceDue makes every outbox row due now, so the next pass claims it without
// waiting out its retry backoff.
func forceDue(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := itctx(t)
	defer cancel()
	if _, err := pool.Exec(ctx, `UPDATE outbox SET next_attempt_at = now()`); err != nil {
		t.Fatalf("force rows due: %v", err)
	}
}

// TestRelay_AttemptCapExcludesRowAfterNFailures proves the WithMaxAttempts
// contract on real Postgres: three failing passes raise a row's attempts to
// the cap, the pass that reaches it logs at ERROR with the row's id, topic,
// type and key, the row STAYS in the outbox excluded from claims, and an
// operator re-arm (attempts = 0) lets the next pass publish it.
func TestRelay_AttemptCapExcludesRowAfterNFailures(t *testing.T) {
	pool := pgtest.NewPool(t)
	pub := postgres.NewOutbox(pool)
	pub.NewID = ids("9a7d2e64-1b3f-4c8a-9d5e-2f6b8a4c1e70")

	ctx, cancel := itctx(t)
	defer cancel()
	if err := pub.PublishRMAEvents(ctx, itAuthorized(t)); err != nil {
		t.Fatalf("publish: %v", err)
	}

	boom := errors.New("broker down")
	sink := &stubSink{fail: true, failWith: boom}
	logs := &captureLogger{}
	relay := postgres.NewOutboxRelay(pool, outboundkafka.NewEncoder(), sink,
		postgres.WithLogger(slog.New(logs)),
		postgres.WithInterval(time.Millisecond),
		postgres.WithMaxAttempts(3),
	)

	var lastAttempts int
	for pass := 1; pass <= 3; pass++ {
		n, err := relay.RelayOnce(ctx)
		if n != 0 {
			t.Fatalf("pass %d published %d rows, want 0", pass, n)
		}
		var sf *outbox.SendFailure
		if !errors.As(err, &sf) {
			t.Fatalf("pass %d err = %v, want an *outbox.SendFailure", pass, err)
		}
		if !errors.Is(err, boom) {
			t.Fatalf("pass %d err = %v, want it to wrap the broker error", pass, err)
		}
		lastAttempts = sf.Attempts
		if lastAttempts != pass {
			t.Fatalf("pass %d recorded attempts = %d, want %d", pass, sf.Attempts, pass)
		}
		forceDue(t, pool)
	}
	if lastAttempts != 3 {
		t.Fatalf("attempts after three failures = %d, want 3", lastAttempts)
	}

	// The capped row stays, excluded from claims: a fourth pass finds nothing.
	n, err := relay.RelayOnce(ctx)
	if err != nil || n != 0 {
		t.Fatalf("pass after the cap = (%d, %v), want (0, nil): the capped row is excluded", n, err)
	}
	if c := pgtest.Count(t, pool, "outbox"); c != 1 {
		t.Fatalf("outbox has %d rows after the cap, want 1 (the row stays for inspection)", c)
	}
	var attempts int
	if err := pool.QueryRow(context.Background(), `SELECT attempts FROM outbox`).Scan(&attempts); err != nil || attempts != 3 {
		t.Fatalf("attempts = (%d, %v), want 3", attempts, err)
	}
	if len(logs.records) == 0 || logs.records[len(logs.records)-1] != postgres.AttemptCapReachedMsg {
		t.Fatalf("last log = %v, want %q", logs.records, postgres.AttemptCapReachedMsg)
	}
	capped := logs.attrs[len(logs.attrs)-1]
	if capped["outbox_id"] == nil || capped["topic"] != outboundkafka.TopicEvents ||
		capped["event_type"] != "com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorized" ||
		capped["key"] != "rma-1" || capped["attempts"] != int64(3) {
		t.Errorf("cap log attrs = %v, want id, topic, event type, key and attempts", capped)
	}

	// The operator re-arms the row and the next pass publishes it.
	if _, err := pool.Exec(context.Background(), `UPDATE outbox SET attempts = 0, next_attempt_at = now()`); err != nil {
		t.Fatalf("re-arm: %v", err)
	}
	sink.fail = false
	n, err = relay.RelayOnce(ctx)
	if err != nil || n != 1 {
		t.Fatalf("pass after re-arm = (%d, %v), want (1, nil)", n, err)
	}
	if len(sink.sent) != 1 || sink.sent[0].EventType != "com.warehouse.wms.returns-processing.returnauthorization.ReturnAuthorized" {
		t.Fatalf("sent = %+v, want the re-armed row's message", sink.sent)
	}
	if c := pgtest.Count(t, pool, "outbox"); c != 0 {
		t.Fatalf("outbox has %d rows after the re-armed send, want 0", c)
	}
}
