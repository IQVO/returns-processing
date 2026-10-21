package kafka

import (
	"context"
	"fmt"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/returns-processing/internal/application/outbox"
)

// Writer is the subset of *kafkago.Writer RelaySink depends on, so unit tests
// can substitute a fake.
type Writer interface {
	WriteMessages(ctx context.Context, msgs ...kafkago.Message) error
}

// RelaySink writes already-encoded outbox messages to the topic each names.
// Its Writer has NO fixed Topic (kafka-go requires exactly one of Writer.Topic
// and Message.Topic, and the relay routes every message — events and
// dead-letters alike — per message); settings are the ones of NewWriter:
// acks=all, the Hash balancer on the key (one aggregate's events stay ordered
// on one partition) and a short BatchTimeout. Constructing it never dials.
type RelaySink struct {
	writer Writer
}

// NewRelaySink constructs a RelaySink over brokers. It does not dial.
func NewRelaySink(brokers []string) *RelaySink { return NewRelaySinkWithWriter(NewWriter(brokers)) }

// NewRelaySinkWithWriter constructs a RelaySink over an explicit Writer (a
// fake in tests). The writer must NOT have a Topic set.
func NewRelaySinkWithWriter(w Writer) *RelaySink { return &RelaySink{writer: w} }

// Send writes msgs in one WriteMessages call. A message without a topic is
// rejected before anything is written.
func (s *RelaySink) Send(ctx context.Context, msgs ...outbox.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]kafkago.Message, len(msgs))
	for i, m := range msgs {
		if m.Topic == "" {
			return fmt.Errorf("kafka relay sink: message %d (%s) has no topic", i, m.EventType)
		}
		headers := make([]kafkago.Header, len(m.Headers))
		for j, h := range m.Headers {
			headers[j] = kafkago.Header{Key: h.Key, Value: []byte(h.Value)}
		}
		out[i] = kafkago.Message{Topic: m.Topic, Key: m.Key, Value: m.Value, Headers: headers}
	}
	if err := s.writer.WriteMessages(ctx, out...); err != nil {
		return fmt.Errorf("kafka relay sink: write %d message(s): %w", len(out), err)
	}
	return nil
}

// Close releases the underlying Kafka writer.
func (s *RelaySink) Close() error {
	if w, ok := s.writer.(*kafkago.Writer); ok {
		return w.Close()
	}
	return nil
}
