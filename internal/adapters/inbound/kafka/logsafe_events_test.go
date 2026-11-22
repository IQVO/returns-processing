package kafka

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	kafkago "github.com/segmentio/kafka-go"
)

// TestHandleMessageLogsNoEventDerivedLineBreaks feeds the consumer a payload
// that is not a CloudEvent and a CloudEvent whose id, type, subject and data
// carry CR/LF, with a handler refusing it with an error quoting the payload:
// no log attribute may carry the line break.
func TestHandleMessageLogsNoEventDerivedLineBreaks(t *testing.T) {
	rec := newCRLFRecorder()
	evilType := "logsafe.Evil" + crlfForged
	c := &Consumer{
		Name: "logsafe", Topic: "logsafe.topic", Logger: slog.New(rec),
		Handlers: map[string]HandlerFunc{
			evilType: func(context.Context, ce.Event) error {
				return fmt.Errorf("%w: field"+crlfForged, errBadPayload)
			},
		},
	}
	ctx := context.Background()
	_ = c.HandleMessage(ctx, []byte("not a CloudEvent"+crlfForged))
	_ = c.HandleMessage(ctx, crlfCloudEvent(t, "id"+crlfForged, evilType))
	rec.assertNoLineBreaks(t, 2)
}

// crlfCloudEvent is a structured-mode CloudEvent whose string attributes are
// the given, hostile values.
func crlfCloudEvent(t *testing.T, id, typ string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"specversion": "1.0", "id": id, "type": typ, "source": "urn:logsafe",
		"subject": "subject" + crlfForged, "datacontenttype": "application/json",
		"data": map[string]any{"field": "value" + crlfForged},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestConsumeLoopLogsNoEventDerivedLineBreaks drives the dead-letter path
// with a handler error and a dead-letter publish error that both quote the
// event: the retry, give-up and publish-retry logs must carry no line break.
func TestConsumeLoopLogsNoEventDerivedLineBreaks(t *testing.T) {
	rec := newCRLFRecorder()
	publishes := 0
	l := &consumeLoop{
		logger: slog.New(rec), name: "logsafe", maxAttempts: 2,
		handle: func(context.Context, kafkago.Message) error {
			return errors.New("use case refused event" + crlfForged)
		},
		deadLetter: func(context.Context, kafkago.Message, error) error {
			publishes++
			if publishes == 1 {
				return errors.New("dead-letter write failed" + crlfForged)
			}
			return nil
		},
	}
	noSleep := func(context.Context, time.Duration) error { return nil }
	msg := kafkago.Message{Topic: "logsafe.topic", Key: []byte("key" + crlfForged), Value: []byte("value" + crlfForged)}
	if err := l.handleWithDeadLetter(context.Background(), RetryPolicy{}.withDefaults(), noSleep, msg); err != nil {
		t.Fatal(err)
	}
	rec.assertNoLineBreaks(t, 3)
}
