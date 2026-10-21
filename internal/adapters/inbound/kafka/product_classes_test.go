package kafka_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// classificationsConsumer is the product classifications consumer over the
// memory doubles (a unit-test consumer: no reader, handlers only). The DLQ
// writer records what would be dead-lettered.
func classificationsConsumer(t *testing.T) (*kafka.Consumer, *memory.ProductClasses, *recordingDLQ) {
	t.Helper()
	classes := memory.NewProductClasses()
	uc := &usecases.ApplyProductClassification{Processed: memory.NewProcessedEvents(), Classes: classes}
	dlq := &recordingDLQ{}
	return &kafka.Consumer{
		Name:              "product classifications consumer",
		Topic:             kafka.ProductMasterTopic,
		Handlers:          kafka.ProductClassesHandlers(uc, quietLogger()),
		Logger:            quietLogger(),
		DLQ:               dlq,
		DeadLetterInvalid: true,
	}, classes, dlq
}

// recordingDLQ is the unit-test dead-letter writer.
type recordingDLQ struct {
	mu   sync.Mutex
	sent []string
}

func (d *recordingDLQ) WriteMessages(_ context.Context, msgs ...kafkago.Message) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, m := range msgs {
		d.sent = append(d.sent, string(m.Value))
	}
	return nil
}

func (d *recordingDLQ) Close() error { return nil }

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&strings.Builder{}, nil))
}

func TestHandleMessage_AppliesAValidProductClassified(t *testing.T) {
	c, classes, _ := classificationsConsumer(t)
	msg := envelope(t, kafka.TypeProductClassified, "/warehouse/product-master", "SKU-1", "evt-1",
		map[string]any{"sku": "SKU-1", "handling_tags": []string{"hazmat"}, "temperature_class": "Frozen",
			"dot_hazard_class": 3, "classification_source": "native", "version": 3})

	if err := c.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle: %v", err)
	}
	pc, ok := classes.Get("SKU-1")
	if !ok || pc.Version != 3 || pc.TemperatureClass != "Frozen" {
		t.Fatalf("stored = %+v ok=%t, want version 3 Frozen", pc, ok)
	}
	// dot_hazard_class was folded into the tag grammar.
	found := false
	for _, tag := range pc.Tags {
		if tag == "dot:3" {
			found = true
		}
	}
	if !found {
		t.Fatalf("tags = %v, want the folded dot:3", pc.Tags)
	}
}

func TestHandleMessage_IgnoresAnUnknownType(t *testing.T) {
	c, classes, _ := classificationsConsumer(t)
	msg := envelope(t, "com.warehouse.wms.product-master.product.ProductRenamed",
		"/warehouse/product-master", "SKU-1", "evt-1", map[string]any{"sku": "SKU-1", "version": 3})

	if err := c.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("an unknown type must be committed past, not failed: %v", err)
	}
	if _, ok := classes.Get("SKU-1"); ok {
		t.Fatal("an unknown type must not touch the copy")
	}
}

func TestHandleMessage_SkipsWhatIsNotACloudEvent(t *testing.T) {
	c, classes, _ := classificationsConsumer(t)
	cases := map[string][]byte{
		"the retired flat envelope": legacyFlatEnvelope(),
		"garbage bytes":             []byte("not json at all"),
		"empty value":               nil,
	}
	for name, value := range cases {
		if err := c.HandleMessage(context.Background(), value); err != nil {
			t.Fatalf("%s: a non-CloudEvent must WARN and commit past, not fail: %v", name, err)
		}
		if _, ok := classes.Get("SKU-1"); ok {
			t.Fatalf("%s: a non-CloudEvent must not touch the copy", name)
		}
	}
}

func TestHandleMessage_DeadLettersAPayloadThatCanNeverApply(t *testing.T) {
	c, _, _ := classificationsConsumer(t)
	// A blank sku is deterministic: errPoison (dead-lettered at once), so a
	// broken producer fact cannot wedge the partition.
	msg := envelope(t, kafka.TypeProductClassified, "/warehouse/product-master", "", "evt-1",
		map[string]any{"sku": "", "version": 3})
	err := c.HandleMessage(context.Background(), msg)
	if err == nil {
		t.Fatal("a poison payload must be reported for dead-lettering")
	}
	if !errors.Is(err, usecases.ErrInvalidEvent) {
		t.Fatalf("poison error = %v, want it to carry ErrInvalidEvent", err)
	}
}

func TestHandleMessage_DeadLettersUndecodableData(t *testing.T) {
	c, _, _ := classificationsConsumer(t)
	msg := envelope(t, kafka.TypeProductClassified, "/warehouse/product-master", "SKU-1", "evt-1",
		"product-classified-data-is-an-object-not-a-string")
	if err := c.HandleMessage(context.Background(), msg); err == nil {
		t.Fatal("undecodable data must be dead-lettered, not skipped")
	}
}

func TestWithDotTag(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		dot  int
		want []string
	}{
		{"no rating adds nothing", []string{"hazmat"}, 0, []string{"hazmat"}},
		{"a rating is folded in", []string{"hazmat"}, 3, []string{"hazmat", "dot:3"}},
		{"an explicit dot tag wins", []string{"dot:9"}, 3, []string{"dot:9"}},
		{"no tags, a rating", nil, 2, []string{"dot:2"}},
	}
	for _, tc := range cases {
		got := kafka.WithDotTag(tc.tags, tc.dot)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: WithDotTag(%v, %d) = %v, want %v", tc.name, tc.tags, tc.dot, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: WithDotTag(%v, %d) = %v, want %v", tc.name, tc.tags, tc.dot, got, tc.want)
			}
		}
	}
}

// The consumer wiring sanity: the constructor maps the full type and the
// topic, and the group id is a parameter, never a literal at the call site.
func TestNewProductClassesConsumer_Wiring(t *testing.T) {
	uc := &usecases.ApplyProductClassification{Processed: memory.NewProcessedEvents(), Classes: memory.NewProductClasses()}
	c := kafka.NewProductClassesConsumerForTopic([]string{"b1"}, "topic-x", "group-x", uc, nil)
	t.Cleanup(func() { _ = c.Close() })
	if c.Topic != "topic-x" || c.Name == "" {
		t.Fatalf("consumer = %q on %q", c.Name, c.Topic)
	}
	if _, ok := c.Handlers[kafka.TypeProductClassified]; !ok {
		t.Fatalf("handlers = %v, want %s", c.Handlers, kafka.TypeProductClassified)
	}
	if len(c.Handlers) != 1 {
		t.Fatalf("handlers = %v, want exactly the consumed type", c.Handlers)
	}
}
