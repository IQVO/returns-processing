package kafka_test

import (
	"context"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// Pinned-contract tests (E1-E4) for the product-master edge. The pinned copy
// is byte-equal to IQVO/product-master@develop (contracts/pinned.json; the
// pinned-contracts CI job fails on drift). Fixtures: the spec's own example
// (1 for ProductClassified — hazmat, frozen, DOT class 3, version 3).
const productMasterPinnedSpec = "testdata/product-master.asyncapi.yaml"

// classificationsPinConsumer is the product classifications consumer over the
// memory doubles (a unit-test consumer: handlers only, no reader).
func classificationsPinConsumer(t *testing.T) (*kafka.Consumer, *memory.ProductClasses) {
	t.Helper()
	classes := memory.NewProductClasses()
	uc := &usecases.ApplyProductClassification{Processed: memory.NewProcessedEvents(), Classes: classes}
	return &kafka.Consumer{
		Name:              "product classifications consumer",
		Topic:             kafka.ProductMasterTopic,
		Handlers:          kafka.ProductClassesHandlers(uc, quietLogger()),
		Logger:            quietLogger(),
		DLQ:               &recordingDLQ{},
		DeadLetterInvalid: true,
	}, classes
}

// E1: the topic our reader config pins (ProductMasterTopic) and the
// CloudEvents type our consumer handles must stay in the producer's catalog.
func TestProductMasterPinCoversOurSubscription(t *testing.T) {
	doc := loadPinnedSpec(t, productMasterPinnedSpec)
	requirePinnedDefaultContentType(t, doc)
	key, ok := pinnedChannelTypes(t, doc, kafka.ProductMasterTopic)[kafka.TypeProductClassified]
	if !ok {
		t.Fatalf("pinned product-master spec no longer documents %s on %s", kafka.TypeProductClassified, kafka.ProductMasterTopic)
	}
	requirePinnedMessageContentType(t, doc, key)
}

// E2: the spec example decodes through the real consumer and applies into the
// classification local copy with the payload's own version and temperature
// class (and the DOT class folded into the tag grammar); a redelivery of the
// same CloudEvents id changes nothing.
func TestProductMasterPinExampleApplies(t *testing.T) {
	doc := loadPinnedSpec(t, productMasterPinnedSpec)
	examples := pinnedExamples(t, doc, kafka.ProductMasterTopic, kafka.TypeProductClassified)
	if len(examples) == 0 {
		t.Fatal("pinned spec ships no ProductClassified examples")
	}
	payload := examples[0]
	c, classes := classificationsPinConsumer(t)
	value := pinnedEventValue(t, payload)
	for delivery := 1; delivery <= 2; delivery++ {
		if err := c.HandleMessage(context.Background(), value); err != nil {
			t.Fatalf("spec example (delivery %d): %v", delivery, err)
		}
	}
	data, _ := payload["data"].(map[string]any)
	sku, _ := data["sku"].(string)
	pc, ok := classes.Get(sku)
	if !ok {
		t.Fatalf("example: %s is not in the copy", sku)
	}
	if pc.Version != pinnedNum(data["version"]) {
		t.Fatalf("stored version = %d, want the payload's %v", pc.Version, data["version"])
	}
	if temp, _ := data["temperature_class"].(string); pc.TemperatureClass != temp {
		t.Fatalf("stored temperature_class = %q, want %q", pc.TemperatureClass, temp)
	}
	if !hasDotTag(pc.Tags, pinnedNum(data["dot_hazard_class"])) {
		t.Fatalf("stored tags = %v, want the DOT class folded in", pc.Tags)
	}
}

// hasDotTag reports whether the tag grammar carries `dot:<n>`.
func hasDotTag(tags []string, dot int64) bool {
	want := "dot:" + pinnedItoa(dot)
	for _, tag := range tags {
		if tag == want {
			return true
		}
	}
	return false
}

// E3: the pinned example carries the fleet CloudEvents 1.0 invariants, and
// its envelope consts match the pinned schema.
func TestProductMasterPinEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, productMasterPinnedSpec)
	key := pinnedChannelTypes(t, doc, kafka.ProductMasterTopic)[kafka.TypeProductClassified]
	schema := pinnedMessageSchema(t, doc, key)
	for _, payload := range pinnedExamples(t, doc, kafka.ProductMasterTopic, kafka.TypeProductClassified) {
		requirePinnedEnvelope(t, payload)
		assertEnvelopeConstsMatch(t, schema, payload)
	}
}

// E4: additive evolution (an unknown data key) is tolerated; documented types
// we do not handle (ProductRegistered, the pack-level family, ...) and
// non-CloudEvent messages are skipped, leaving the copy untouched.
func TestProductMasterPinToleranceAndUnknownTypes(t *testing.T) {
	doc := loadPinnedSpec(t, productMasterPinnedSpec)
	examples := pinnedExamples(t, doc, kafka.ProductMasterTopic, kafka.TypeProductClassified)
	if len(examples) == 0 {
		t.Fatal("no ProductClassified examples to perturb")
	}
	c, classes := classificationsPinConsumer(t)

	// An unknown data key must not break decode or apply.
	withUnknown := pinnedWithUnknownDataKey(t, examples[0])
	if err := c.HandleMessage(context.Background(), pinnedEventValue(t, withUnknown)); err != nil {
		t.Fatalf("unknown data key must not fail: %v", err)
	}
	data, _ := examples[0]["data"].(map[string]any)
	sku, _ := data["sku"].(string)
	if _, ok := classes.Get(sku); !ok {
		t.Fatal("unknown data key must still reach the local copy")
	}

	// Documented-but-unhandled types and non-CloudEvent messages are
	// committed past; the applied classification is unchanged.
	for typ := range pinnedChannelTypes(t, doc, kafka.ProductMasterTopic) {
		if typ == kafka.TypeProductClassified {
			continue
		}
		payload := pinnedSkipFixture(t, typ, "/warehouse/product-master")
		if err := c.HandleMessage(context.Background(), pinnedEventValue(t, payload)); err != nil {
			t.Fatalf("documented-but-unhandled type %s must be skipped, got %v", typ, err)
		}
	}
	if err := c.HandleMessage(context.Background(), legacyFlatEnvelope()); err != nil {
		t.Fatalf("a non-CloudEvent message must be skipped, got %v", err)
	}
	pc, ok := classes.Get(sku)
	if !ok || pc.Version != pinnedNum(data["version"]) {
		t.Fatalf("skipped messages must leave the copy untouched: %+v ok=%t", pc, ok)
	}
}
