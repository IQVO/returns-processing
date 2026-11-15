package kafka_test

import (
	"context"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// Pinned-contract tests (E1-E4) for the order-management edge. The pinned
// copy is byte-equal to IQVO/order-management@develop (contracts/pinned.json;
// the pinned-contracts CI job fails on drift). Fixtures: the spec's own
// examples (2 for OrderAllocated, 2 for OrderPartiallyAllocated).
const orderManagementPinnedSpec = "testdata/order-management.asyncapi.yaml"

// allocationsPinConsumer is the order lines consumer over the memory doubles
// (a unit-test consumer: handlers only, no reader).
func allocationsPinConsumer(t *testing.T) (*kafka.Consumer, *memory.AllocatedLines) {
	t.Helper()
	lines := memory.NewAllocatedLines()
	uc := &usecases.ApplyAllocations{Processed: memory.NewProcessedEvents(), Lines: lines}
	return &kafka.Consumer{
		Name:              "order lines consumer",
		Topic:             kafka.OrderManagementTopic,
		Handlers:          kafka.AllocationHandlers(uc, quietLogger()),
		Logger:            quietLogger(),
		DLQ:               &recordingDLQ{},
		DeadLetterInvalid: true,
	}, lines
}

// E1: the topic our reader config pins (OrderManagementTopic) and BOTH
// CloudEvents types our consumer handles must stay in the producer's catalog.
func TestOrderManagementPinCoversOurSubscription(t *testing.T) {
	doc := loadPinnedSpec(t, orderManagementPinnedSpec)
	requirePinnedDefaultContentType(t, doc)
	types := pinnedChannelTypes(t, doc, kafka.OrderManagementTopic)
	for _, handled := range []string{kafka.TypeOrderAllocated, kafka.TypeOrderPartiallyAllocated} {
		key, ok := types[handled]
		if !ok {
			t.Fatalf("pinned order-management spec no longer documents %s on %s", handled, kafka.OrderManagementTopic)
		}
		requirePinnedMessageContentType(t, doc, key)
	}
}

// E2: every spec example of both handled types decodes through the real
// consumer and applies into the allocated-lines local copy with the payload's
// own order id and line numbers; a redelivery dedupes on the CloudEvents id.
func TestOrderManagementPinExamplesApply(t *testing.T) {
	doc := loadPinnedSpec(t, orderManagementPinnedSpec)
	for _, handled := range []string{kafka.TypeOrderAllocated, kafka.TypeOrderPartiallyAllocated} {
		examples := pinnedExamples(t, doc, kafka.OrderManagementTopic, handled)
		if len(examples) == 0 {
			t.Fatalf("pinned spec ships no %s examples", handled)
		}
		for _, payload := range examples {
			assertAllocationExampleApplies(t, payload)
		}
	}
}

// assertAllocationExampleApplies runs one spec example through the consumer
// twice — the redelivery must dedupe on the CloudEvents id and apply every
// documented line exactly once.
func assertAllocationExampleApplies(t *testing.T, payload map[string]any) {
	t.Helper()
	c, lines := allocationsPinConsumer(t)
	value := pinnedEventValue(t, payload)
	for delivery := 1; delivery <= 2; delivery++ {
		if err := c.HandleMessage(context.Background(), value); err != nil {
			t.Fatalf("example %v (delivery %d): %v", payload["id"], delivery, err)
		}
	}
	data, _ := payload["data"].(map[string]any)
	orderID, _ := data["order_id"].(string)
	rawLines, _ := data["lines"].([]any)
	for _, raw := range rawLines {
		line, _ := raw.(map[string]any)
		stored, ok := lines.Get(orderID, int32(pinnedNum(line["line_no"])))
		if !ok {
			t.Fatalf("example %v: line %v is not in the copy", payload["id"], line["line_no"])
		}
		if wantSKU, _ := line["sku"].(string); stored.SKU != wantSKU {
			t.Fatalf("example %v: line %v stored sku %q, want %q", payload["id"], line["line_no"], stored.SKU, wantSKU)
		}
	}
}

// E3: the pinned examples of both handled types carry the fleet CloudEvents
// 1.0 invariants, and their envelope consts match the pinned schemas.
func TestOrderManagementPinEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, orderManagementPinnedSpec)
	types := pinnedChannelTypes(t, doc, kafka.OrderManagementTopic)
	for _, handled := range []string{kafka.TypeOrderAllocated, kafka.TypeOrderPartiallyAllocated} {
		schema := pinnedMessageSchema(t, doc, types[handled])
		for _, payload := range pinnedExamples(t, doc, kafka.OrderManagementTopic, handled) {
			requirePinnedEnvelope(t, payload)
			assertEnvelopeConstsMatch(t, schema, payload)
		}
	}
}

// E4: additive evolution (an unknown data key) is tolerated; documented types
// we do not handle (OrderRepromised, SiteSkuDemandChanged) and non-CloudEvent
// messages are skipped, leaving the applied copy untouched.
func TestOrderManagementPinToleranceAndUnknownTypes(t *testing.T) {
	doc := loadPinnedSpec(t, orderManagementPinnedSpec)
	examples := pinnedExamples(t, doc, kafka.OrderManagementTopic, kafka.TypeOrderAllocated)
	if len(examples) == 0 {
		t.Fatal("no OrderAllocated examples to perturb")
	}
	c, lines := allocationsPinConsumer(t)

	// An unknown data key must not break decode or apply.
	withUnknown := pinnedWithUnknownDataKey(t, examples[0])
	if err := c.HandleMessage(context.Background(), pinnedEventValue(t, withUnknown)); err != nil {
		t.Fatalf("unknown data key must not fail: %v", err)
	}
	data, _ := examples[0]["data"].(map[string]any)
	orderID, _ := data["order_id"].(string)
	firstSKU := firstDocumentedSKU(t, data)
	if line, ok := lines.Get(orderID, 1); !ok || line.SKU != firstSKU {
		t.Fatalf("unknown data key must still apply the allocation: %+v ok=%t", line, ok)
	}

	// Documented-but-unhandled types and non-CloudEvent messages are
	// committed past; the applied line is unchanged.
	for typ := range pinnedChannelTypes(t, doc, kafka.OrderManagementTopic) {
		if typ == kafka.TypeOrderAllocated || typ == kafka.TypeOrderPartiallyAllocated {
			continue
		}
		payload := pinnedSkipFixture(t, typ, "/warehouse/order-management")
		if err := c.HandleMessage(context.Background(), pinnedEventValue(t, payload)); err != nil {
			t.Fatalf("documented-but-unhandled type %s must be skipped, got %v", typ, err)
		}
	}
	if err := c.HandleMessage(context.Background(), legacyFlatEnvelope()); err != nil {
		t.Fatalf("a non-CloudEvent message must be skipped, got %v", err)
	}
	if line, ok := lines.Get(orderID, 1); !ok || line.SKU != firstSKU {
		t.Fatalf("skipped messages must leave the copy untouched: %+v ok=%t", line, ok)
	}
}

// firstDocumentedSKU returns the sku of the payload's first line (line 1).
func firstDocumentedSKU(t *testing.T, data map[string]any) string {
	t.Helper()
	rawLines, _ := data["lines"].([]any)
	for _, raw := range rawLines {
		line, _ := raw.(map[string]any)
		if pinnedNum(line["line_no"]) == 1 {
			sku, _ := line["sku"].(string)
			return sku
		}
	}
	t.Fatal("no line 1 in the example payload")
	return ""
}
