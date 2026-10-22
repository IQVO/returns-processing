package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/returns-processing/internal/adapters/kafka/cloudevents"
	"github.com/claudioed/returns-processing/internal/analytics/report"
)

// skipWarnEvery bounds the WARN lines for skipped (non-CloudEvents) messages:
// the first skip is logged, then at most one line per interval carrying the
// number suppressed since.
const skipWarnEvery = time.Minute

// The `<entity>` segments of the types this service publishes
// (com.warehouse.wms.returns-processing.<entity>.<EventName>). They mirror
// the outbox aggregate segments without importing an outbound package
// (inbound adapters never depend on outbound ones, arch test).
const (
	analyticsEntityRMA         = "returnauthorization"
	analyticsEntityReceipt     = "returnreceipt"
	analyticsEntityDisposition = "disposition"
)

// analyticsKinds maps the FULL CloudEvents type of the five PROJECTED events
// to their report.Kind. The two authorization events and every other type on
// the topic are acknowledged and ignored (the authorization funnel answers no
// report of ADR 0007).
var analyticsKinds = map[string]report.Kind{
	cloudevents.Type(analyticsEntityReceipt, string(report.KindReturnReceiptOpened)):      report.KindReturnReceiptOpened,
	cloudevents.Type(analyticsEntityReceipt, string(report.KindUnexpectedReturnReceived)): report.KindUnexpectedReturnReceived,
	cloudevents.Type(analyticsEntityReceipt, string(report.KindReturnLineInspected)):      report.KindReturnLineInspected,
	cloudevents.Type(analyticsEntityDisposition, string(report.KindDispositionDecided)):   report.KindDispositionDecided,
	cloudevents.Type(analyticsEntityDisposition, string(report.KindReturnLineStaged)):     report.KindReturnLineStaged,
}

// analyticsLine is one line of a ReturnReceiptOpened payload.
type analyticsLine struct {
	LineNo   int32  `json:"line_no"`
	SKU      string `json:"sku"`
	Quantity int64  `json:"quantity"`
	RMAID    string `json:"rma_id"`
}

// analyticsData is the union of the analytics payload fields the projection
// reads (hand-mirrored from apis/asyncapi.yaml, never imported from the
// domain). rma_id / supersedes / reason are nullable on the wire and decode
// as "" when absent.
type analyticsData struct {
	ReceiptID     string          `json:"receipt_id"`
	RMAID         string          `json:"rma_id"`
	DispositionID string          `json:"disposition_id"`
	SiteID        string          `json:"site_id"`
	SKU           string          `json:"sku"`
	LineNo        int32           `json:"line_no"`
	Quantity      int64           `json:"quantity"`
	Grade         string          `json:"grade"`
	Outcome       string          `json:"outcome"`
	PolicyVersion int64           `json:"policy_version"`
	Supersedes    string          `json:"supersedes"`
	Reason        string          `json:"reason"`
	Lines         []analyticsLine `json:"lines"`
}

// AnalyticsConsumer is the projector's consumer (ADR 0007): it reads the
// analytics topic (warehouse.returns-processing.analytics) under a FIXED,
// env-supplied consumer group (at-least-once, offsets committed only after
// success) and folds each event into the analytical model through
// report.Projection.
//
// Outcomes per message (ADR 0007, the reference policy):
//   - not a CloudEvents 1.0 message: skipped and committed past, with a
//     rate-limited WARN;
//   - a valid CloudEvent of any other type (the two authorization events
//     included): ignored, committed past;
//   - a known type with an unusable payload, or one the store
//     deterministically rejects (report.ErrRejected): dead-lettered AT ONCE to
//     "<topic>.dlq", then committed past;
//   - a transient failure (database down, timeout): the SAME message is
//     retried with capped exponential backoff and the offset is NOT committed;
//     it is never dead-lettered.
type AnalyticsConsumer struct {
	Reader     Reader
	Projection report.Projection
	// DLQ publishes poison messages; DLQTopic is the projector's
	// "<topic>.dlq".
	DLQ      DeadLetterWriter
	DLQTopic string
	Logger   *slog.Logger
	Retry    RetryPolicy

	// Now is the clock of the skip-warning sampler (time.Now when nil).
	Now func() time.Time

	skipsOnce sync.Once
	skips     *skipSampler
	sleep     sleepFunc // test hook; nil => real, ctx-cancellable sleep
}

// NewAnalyticsConsumer constructs the consumer for topic under groupID (an
// env-configured value from the composition root, never a literal). Its
// dead-letter writer targets topic + DLQSuffix. Nothing dials Kafka here.
func NewAnalyticsConsumer(brokers []string, topic, groupID string, projection report.Projection, logger *slog.Logger) *AnalyticsConsumer {
	cfg := readerConfig(brokers, topic, groupID)
	cfg.ErrorLogger = readerErrorLogger(defaultLogger(logger), "analytics consumer", topic)
	return &AnalyticsConsumer{
		Reader:     kafkago.NewReader(cfg),
		Projection: projection,
		DLQ:        newDLQWriter(brokers),
		DLQTopic:   topic + DLQSuffix,
		Logger:     defaultLogger(logger),
	}
}

// Run consumes until ctx is cancelled or the reader fails.
func (c *AnalyticsConsumer) Run(ctx context.Context) error {
	loop := consumeLoop{
		reader: c.Reader,
		handle: c.HandleMessage,
		logger: defaultLogger(c.Logger),
		name:   "analytics consumer",
		retry:  c.Retry,
		sleep:  c.sleep,
	}
	// No maxAttempts/deadLetter on the loop: a transient failure retries
	// forever (ADR 0007); the deterministic dead-letter happens inside
	// HandleMessage, immediately.
	return loop.run(ctx)
}

// Close releases the reader and the dead-letter writer.
func (c *AnalyticsConsumer) Close() error {
	var err error
	if c.Reader != nil {
		err = c.Reader.Close()
	}
	if c.DLQ != nil {
		err = errors.Join(err, c.DLQ.Close())
	}
	return err
}

// HandleMessage processes one message; see the type comment for outcomes. A
// non-nil error is always transient (the loop retries the same message).
func (c *AnalyticsConsumer) HandleMessage(ctx context.Context, msg kafkago.Message) error {
	evt, err := cloudevents.Decode(msg.Value)
	if err != nil {
		c.skipped(ctx, msg, err)
		return nil
	}
	kind, known := analyticsKinds[evt.Type()]
	if !known {
		defaultLogger(c.Logger).DebugContext(ctx, "analytics: ignoring event of another type", "type", logSafe(evt.Type()), "event_id", logSafe(evt.ID()))
		return nil
	}
	pe, err := toReportEvent(kind, evt)
	if err != nil {
		//dlq:deterministic toReportEvent is a pure decode/validate of the message bytes (no ctx): the same message can never pass
		return c.deadLetter(ctx, msg, err)
	}
	if _, err := c.Projection.Apply(ctx, pe); err != nil {
		if errors.Is(err, report.ErrRejected) {
			//dlq:deterministic ErrRejected is minted only by the pure event validation or a Postgres SQLSTATE 22/23 rejection (analyticsstore.classify) -- never by a cancelled context
			return c.deadLetter(ctx, msg, err)
		}
		return fmt.Errorf("analytics: project %s %s: %w", kind, evt.ID(), err)
	}
	return nil
}

// toReportEvent validates a decoded CloudEvent of a known kind and maps it to
// a report.Event. Every error is deterministic (the same bytes can never
// pass).
func toReportEvent(kind report.Kind, evt ce.Event) (report.Event, error) {
	var d analyticsData
	if err := evt.DataAs(&d); err != nil {
		return report.Event{}, fmt.Errorf("decode %s data: %w", kind, err)
	}
	e := report.Event{Kind: kind, EventID: evt.ID(), At: evt.Time().UTC(), SiteID: d.SiteID, SKU: d.SKU}
	switch kind {
	case report.KindReturnReceiptOpened:
		e.ReceiptID = d.ReceiptID
		e.RMAID = d.RMAID
		for _, l := range d.Lines {
			e.Lines = append(e.Lines, report.OpenedLine{LineNo: l.LineNo, SKU: l.SKU, Quantity: l.Quantity, RMAID: l.RMAID})
		}
	case report.KindUnexpectedReturnReceived:
		e.ReceiptID, e.LineNo, e.Quantity = d.ReceiptID, d.LineNo, d.Quantity
	case report.KindReturnLineInspected:
		e.ReceiptID, e.LineNo, e.Quantity, e.Grade = d.ReceiptID, d.LineNo, d.Quantity, d.Grade
	case report.KindDispositionDecided:
		e.DispositionID, e.ReceiptID, e.LineNo, e.Quantity = d.DispositionID, d.ReceiptID, d.LineNo, d.Quantity
		e.Grade, e.Outcome, e.PolicyVersion = d.Grade, d.Outcome, d.PolicyVersion
		e.Supersedes, e.Reason = d.Supersedes, d.Reason
	case report.KindReturnLineStaged:
		e.DispositionID, e.ReceiptID, e.LineNo, e.Quantity = d.DispositionID, d.ReceiptID, d.LineNo, d.Quantity
		e.RMAID = d.RMAID
	}
	if want := subjectOf(e); want != evt.Subject() {
		return report.Event{}, fmt.Errorf("%s %s: payload identifies %q but the subject is %q", kind, evt.ID(), want, evt.Subject())
	}
	if err := e.Validate(); err != nil {
		return report.Event{}, err
	}
	return e, nil
}

// subjectOf is the CloudEvents subject an event must carry: the receipt id
// for the receipt events, the disposition id for the disposition events.
func subjectOf(e report.Event) string {
	if e.Kind.IsDisposition() {
		return e.DispositionID
	}
	return e.ReceiptID
}

func (c *AnalyticsConsumer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// skipped logs a skipped non-CloudEvents message, sampled.
func (c *AnalyticsConsumer) skipped(ctx context.Context, msg kafkago.Message, cause error) {
	c.skipsOnce.Do(func() { c.skips = &skipSampler{every: skipWarnEvery, now: c.now} })
	if emit, suppressed := c.skips.allow(); emit {
		defaultLogger(c.Logger).WarnContext(ctx, "analytics: skipping message that is not a CloudEvents 1.0 event (further skips are counted, not logged, until the next interval)",
			"error", cause, "topic", msg.Topic, "partition", msg.Partition, "offset", msg.Offset, "suppressed_since_last_warning", suppressed)
	}
}

// deadLetter publishes the raw, unmodified message to the DLQ with error
// context in headers, then lets the loop commit past it. A DLQ write failure
// is returned (transient: the loop retries the message) so a poison message is
// never lost.
func (c *AnalyticsConsumer) deadLetter(ctx context.Context, msg kafkago.Message, cause error) error {
	defaultLogger(c.Logger).ErrorContext(ctx, "analytics: dead-lettering a message that can never be projected",
		"error", cause, "dlq_topic", c.DLQTopic, "partition", msg.Partition, "offset", msg.Offset)
	if err := publishDeadLetter(ctx, c.DLQ, c.DLQTopic, msg, cause); err != nil {
		return fmt.Errorf("analytics: publish to dead-letter topic %s: %w", c.DLQTopic, err)
	}
	return nil
}

// skipSampler lets the first call through, then at most one per interval,
// reporting how many were suppressed in between.
type skipSampler struct {
	mu         sync.Mutex
	every      time.Duration
	now        func() time.Time
	last       time.Time
	suppressed int64
}

func (s *skipSampler) allow() (emit bool, suppressed int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.last.IsZero() || now.Sub(s.last) >= s.every {
		suppressed, s.suppressed, s.last = s.suppressed, 0, now
		return true, suppressed
	}
	s.suppressed++
	return false, 0
}
