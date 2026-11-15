package kafka_test

import (
	"context"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// Pinned-contract tests (E1-E4) for the fulfillment-execution edge. The
// pinned copy is byte-equal to IQVO/fulfillment-execution@develop
// (contracts/pinned.json; the pinned-contracts CI job fails on drift).
// Fixtures: the spec's own examples (producer ships 3 for PackageManifested).
const fulfillmentPinnedSpec = "testdata/fulfillment-execution.asyncapi.yaml"

// FINDING (documented in the PR body, not fixed here — test-only PR):
// fulfillment-execution@develop documents PackageManifested.order_ref as "the
// package's order reference" with examples 'wu-8a1f' and plain order ids
// ('order-88421'), while this reader requires the WES work-unit formula
// {order_id}-line-{line_no} (shared.ParseWorkUnit) and dead-letters anything
// else. Every verbatim spec example is therefore POISON for this consumer.
// E2 asserts that routing decision on the verbatim examples and covers the
// apply path with an ADAPTED clone (formula-shaped order_ref, fresh
// CloudEvents id — the id is the idempotency key, never reused).

// fulfillmentPinConsumer is the manifests consumer over the memory doubles
// (a unit-test consumer: handlers only, no reader).
func fulfillmentPinConsumer(t *testing.T) (*kafka.Consumer, *memory.ManifestedUnits) {
	t.Helper()
	units := memory.NewManifestedUnits()
	uc := &usecases.ApplyManifest{Processed: memory.NewProcessedEvents(), Units: units}
	return &kafka.Consumer{
		Name:              "manifests consumer",
		Topic:             kafka.FulfillmentTopic,
		Handlers:          kafka.ManifestHandlers(uc, quietLogger()),
		Logger:            quietLogger(),
		DLQ:               &recordingDLQ{},
		DeadLetterInvalid: true,
	}, units
}

// adaptedManifestFixture clones a spec example into one the reader accepts:
// a formula-shaped order_ref and a FRESH CloudEvents id (the original was
// never applied, but a fresh id keeps the clone safe under any claim).
func adaptedManifestFixture(t *testing.T, payload map[string]any, packageID string) map[string]any {
	t.Helper()
	clone := pinnedCloneEnvelope(payload)
	data, _ := payload["data"].(map[string]any)
	adapted := make(map[string]any, len(data)+1)
	for k, v := range data {
		adapted[k] = v
	}
	adapted["package_id"] = packageID
	adapted["order_ref"] = "ord-pin-line-1"
	clone["id"] = "adapted-" + packageID + "-0001"
	clone["data"] = adapted
	return clone
}

// E1: the topic our reader config pins (FulfillmentTopic) and the CloudEvents
// type our consumer handles must stay in the producer's message catalog.
func TestFulfillmentPinCoversOurSubscription(t *testing.T) {
	doc := loadPinnedSpec(t, fulfillmentPinnedSpec)
	requirePinnedDefaultContentType(t, doc)
	key, ok := pinnedChannelTypes(t, doc, kafka.FulfillmentTopic)[kafka.TypePackageManifested]
	if !ok {
		t.Fatalf("pinned fulfillment spec no longer documents %s on %s", kafka.TypePackageManifested, kafka.FulfillmentTopic)
	}
	requirePinnedMessageContentType(t, doc, key)
}

// E2: every spec example of the handled type decodes through the real
// consumer and reaches a routing decision; the apply path is covered by an
// adapted clone (see the FINDING above for why verbatim examples cannot
// apply).
func TestFulfillmentPinExamplesRouteThroughConsumer(t *testing.T) {
	doc := loadPinnedSpec(t, fulfillmentPinnedSpec)
	examples := pinnedExamples(t, doc, kafka.FulfillmentTopic, kafka.TypePackageManifested)
	if len(examples) == 0 {
		t.Fatal("pinned spec ships no PackageManifested examples")
	}
	for _, payload := range examples {
		c, units := fulfillmentPinConsumer(t)
		err := c.HandleMessage(context.Background(), pinnedEventValue(t, payload))
		pkg, _ := payload["data"].(map[string]any)["package_id"].(string)
		if err == nil {
			t.Fatalf("example %v: order_ref %q cannot be applied; the consumer must surface poison, got nil",
				payload["id"], payload["data"].(map[string]any)["order_ref"])
		}
		if _, ok := units.GetByPackage(pkg); ok {
			t.Fatalf("example %v: a rejected order_ref must not touch the local copy", payload["id"])
		}
	}

	// The apply path: an adapted clone (formula-shaped order_ref, fresh id)
	// decodes, applies exactly once and dedupes on the CloudEvents id.
	c, units := fulfillmentPinConsumer(t)
	adapted := adaptedManifestFixture(t, examples[0], "pkg-pin-1")
	value := pinnedEventValue(t, adapted)
	for i := 0; i < 2; i++ { // the redelivery must change nothing
		if err := c.HandleMessage(context.Background(), value); err != nil {
			t.Fatalf("adapted example (delivery %d): %v", i, err)
		}
	}
	unit, ok := units.GetByOrderLine("ord-pin", 1)
	if !ok || unit.PackageID != "pkg-pin-1" {
		t.Fatalf("adapted example: by order line = %+v ok=%t, want pkg-pin-1", unit, ok)
	}
	if _, ok := units.GetByPackage("pkg-pin-1"); !ok {
		t.Fatal("adapted example: the applied unit must resolve by package id")
	}
}

// E3: the pinned examples carry the fleet CloudEvents 1.0 invariants, and
// their envelope consts match the pinned schema.
func TestFulfillmentPinEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, fulfillmentPinnedSpec)
	key := pinnedChannelTypes(t, doc, kafka.FulfillmentTopic)[kafka.TypePackageManifested]
	schema := pinnedMessageSchema(t, doc, key)
	for _, payload := range pinnedExamples(t, doc, kafka.FulfillmentTopic, kafka.TypePackageManifested) {
		requirePinnedEnvelope(t, payload)
		assertEnvelopeConstsMatch(t, schema, payload)
	}
}

// E4: additive evolution (unknown data key) is tolerated on the adapted
// fixture; documented types we do not handle are skipped untouched; a message
// that is not a CloudEvent is skipped per the repo's documented policy.
func TestFulfillmentPinToleranceAndUnknownTypes(t *testing.T) {
	doc := loadPinnedSpec(t, fulfillmentPinnedSpec)
	examples := pinnedExamples(t, doc, kafka.FulfillmentTopic, kafka.TypePackageManifested)

	// Unknown data keys must not break decode or apply.
	c, units := fulfillmentPinConsumer(t)
	withUnknown := pinnedWithUnknownDataKey(t, adaptedManifestFixture(t, examples[0], "pkg-pin-2"))
	if err := c.HandleMessage(context.Background(), pinnedEventValue(t, withUnknown)); err != nil {
		t.Fatalf("unknown data key must not fail: %v", err)
	}
	if _, ok := units.GetByPackage("pkg-pin-2"); !ok {
		t.Fatal("unknown data key must still reach the local copy")
	}

	// Documented-but-unhandled types (TaskAvailable, TaskCompleted, ... —
	// exception-management's, not ours) and non-CloudEvent messages are
	// committed past; the copy keeps exactly the one applied unit.
	for typ := range pinnedChannelTypes(t, doc, kafka.FulfillmentTopic) {
		if typ == kafka.TypePackageManifested {
			continue
		}
		payload := pinnedSkipFixture(t, typ, "/warehouse/fulfillment-execution")
		if err := c.HandleMessage(context.Background(), pinnedEventValue(t, payload)); err != nil {
			t.Fatalf("documented-but-unhandled type %s must be skipped, got %v", typ, err)
		}
	}
	if err := c.HandleMessage(context.Background(), legacyFlatEnvelope()); err != nil {
		t.Fatalf("a non-CloudEvent message must be skipped, got %v", err)
	}
	if unit, ok := units.GetByOrderLine("ord-pin", 1); !ok || unit.PackageID != "pkg-pin-2" {
		t.Fatalf("skipped messages must leave the copy untouched: %+v ok=%t", unit, ok)
	}
}
