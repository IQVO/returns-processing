package kafka

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// fakeReader hands out a fixed stream of messages and records the commits.
type fakeReader struct {
	mu       sync.Mutex
	messages []kafkago.Message
	commits  []int64 // offsets committed, in order
	closed   bool
	// errOnFetch, from the Nth FetchMessage on (1-based), makes FetchMessage
	// itself fail: the loop must surface it (the composition root exits).
	errOnFetch int
	fetches    int
}

// errNoMoreMessages is the fake's "empty partition" parking error: the run
// loop returns it once every handed-out message was handled.
var errNoMoreMessages = errors.New("no more messages")

func (r *fakeReader) FetchMessage(context.Context) (kafkago.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fetches++
	if r.errOnFetch > 0 && r.fetches >= r.errOnFetch {
		return kafkago.Message{}, errors.New("reader failed")
	}
	if len(r.messages) == 0 {
		return kafkago.Message{}, errNoMoreMessages
	}
	msg := r.messages[0]
	r.messages = r.messages[1:]
	return msg, nil
}

func (r *fakeReader) CommitMessages(_ context.Context, msgs ...kafkago.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range msgs {
		r.commits = append(r.commits, m.Offset)
	}
	return nil
}

func (r *fakeReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *fakeReader) committed() []int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int64{}, r.commits...)
}

// fakeDLQ records the dead-lettered messages.
type fakeDLQ struct {
	mu   sync.Mutex
	sent []kafkago.Message
}

func (d *fakeDLQ) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent = append(d.sent, msgs...)
	return nil
}

func (d *fakeDLQ) Close() error { return nil }

func (d *fakeDLQ) deadLettered() []kafkago.Message {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]kafkago.Message{}, d.sent...)
}

// loop builds a bounded consumeLoop over handle (maxAttempts 3) with a no-op
// sleep: no real backoff waits in unit tests.
func loop(reader Reader, handle func() error, withDLQ bool) (*consumeLoop, *fakeDLQ) {
	dlq := &fakeDLQ{}
	l := &consumeLoop{
		reader: reader,
		handle: func(context.Context, kafkago.Message) error { return handle() },
		logger: slog.New(slog.NewTextHandler(&strings.Builder{}, nil)),
		name:   "test consumer",
		sleep:  func(context.Context, time.Duration) error { return nil },
	}
	if withDLQ {
		l.maxAttempts = 3
		l.deadLetter = func(ctx context.Context, m kafkago.Message, _ error) error {
			return dlq.WriteMessages(ctx, m)
		}
	}
	return l, dlq
}

func msg(offset int64, value string) kafkago.Message {
	return kafkago.Message{Topic: "t", Partition: 0, Offset: offset, Value: []byte(value)}
}

func TestConsumeLoop_CommitsOnlyAfterHandled(t *testing.T) {
	reader := &fakeReader{messages: []kafkago.Message{msg(0, "a"), msg(1, "b")}}
	l, _ := loop(reader, func() error { return nil }, false)

	if err := l.run(context.Background()); err != errNoMoreMessages {
		t.Fatalf("run = %v, want the parking error", err)
	}
	if got := reader.committed(); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("commits = %v, want [0 1]", got)
	}
}

func TestConsumeLoop_RetriesTheSameTransientFailureUntilItPasses(t *testing.T) {
	reader := &fakeReader{messages: []kafkago.Message{msg(0, "a")}}
	attempts := 0
	l, dlq := loop(reader, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("db down") // transient twice
		}
		return nil
	}, true)

	if err := l.run(context.Background()); err != errNoMoreMessages {
		t.Fatalf("run = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
	if got := reader.committed(); len(got) != 1 || got[0] != 0 {
		t.Fatalf("commits = %v, want the single message after it passed", got)
	}
	if dead := dlq.deadLettered(); len(dead) != 0 {
		t.Fatalf("a passing message was dead-lettered: %v", dead)
	}
}

func TestConsumeLoop_DeadLettersAfterMaxAttemptsAndMovesOn(t *testing.T) {
	reader := &fakeReader{messages: []kafkago.Message{msg(0, "bad"), msg(1, "good")}}
	calls := 0
	l, dlq := loop(reader, func() error {
		calls++
		if calls <= 3 { // maxAttempts = 3: the first message never passes
			return errors.New("still failing")
		}
		return nil
	}, true)

	if err := l.run(context.Background()); err != errNoMoreMessages {
		t.Fatalf("run = %v", err)
	}
	if calls != 4 { // 3 failed attempts on the first, 1 pass on the second
		t.Fatalf("handler calls = %d, want 4", calls)
	}
	dead := dlq.deadLettered()
	if len(dead) != 1 || string(dead[0].Value) != "bad" {
		t.Fatalf("dead letters = %v, want the one stuck message", dead)
	}
	if got := reader.committed(); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("commits = %v, want both (the dead-lettered one committed past)", got)
	}
}

func TestConsumeLoop_DeadLettersPoisonAtOnce(t *testing.T) {
	reader := &fakeReader{messages: []kafkago.Message{msg(0, "poison")}}
	calls := 0
	l, dlq := loop(reader, func() error {
		calls++
		return fmt.Errorf("%w: blank sku", errPoison)
	}, true)

	if err := l.run(context.Background()); err != errNoMoreMessages {
		t.Fatalf("run = %v", err)
	}
	if calls != 1 {
		t.Fatalf("a poison message was retried %d times, want no retry", calls)
	}
	if dead := dlq.deadLettered(); len(dead) != 1 {
		t.Fatalf("dead letters = %d, want the poison message", len(dead))
	}
}

// The fleet escape-18b rule: a handler that failed because the process is
// stopping is NEVER dead-lettered — the offset stays uncommitted and the
// message is redelivered on restart.
func TestConsumeLoop_NeverDeadLettersACancelledContext(t *testing.T) {
	reader := &fakeReader{messages: []kafkago.Message{msg(0, "in-flight")}}
	ctx, cancel := context.WithCancel(context.Background())
	l, dlq := loop(reader, func() error {
		cancel() // shutdown while the message is being handled
		return ctx.Err()
	}, true)

	if err := l.run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("run = %v, want context.Canceled", err)
	}
	if dead := dlq.deadLettered(); len(dead) != 0 {
		t.Fatal("a message interrupted by shutdown was dead-lettered")
	}
	if got := reader.committed(); len(got) != 0 {
		t.Fatalf("commits = %v, want none: the offset must stay uncommitted", got)
	}
}

func TestConsumeLoop_AFetchFailureStopsTheLoop(t *testing.T) {
	reader := &fakeReader{errOnFetch: 1}
	l, _ := loop(reader, func() error { return nil }, false)
	if err := l.run(context.Background()); err == nil || errors.Is(err, errNoMoreMessages) {
		t.Fatalf("run = %v, want the reader failure surfaced", err)
	}
}
