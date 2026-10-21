package kafka

import (
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func TestNewWriterIsSynchronousAcksAllAndTopicFree(t *testing.T) {
	w := NewWriter([]string{"broker-a:9092", "broker-b:9092"})
	if w.RequiredAcks != kafkago.RequireAll {
		t.Fatalf("RequiredAcks = %v, want RequireAll", w.RequiredAcks)
	}
	if w.Async {
		t.Fatal("writer must be synchronous so a failed write reaches the relay")
	}
	if w.Topic != "" {
		t.Fatalf("writer Topic = %q, want empty (messages carry their own topic)", w.Topic)
	}
}

// The fleet convention: the broker auto-creates every business topic on first
// write, so the relay's writer must ask for it.
func TestNewWriterAllowsAutoTopicCreation(t *testing.T) {
	w := NewWriter([]string{"broker-a:9092"})
	if !w.AllowAutoTopicCreation {
		t.Fatal("AllowAutoTopicCreation = false, want true (fleet topic convention: topics are auto-created on first write)")
	}
}
