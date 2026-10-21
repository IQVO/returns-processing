package kafka_test

import (
	"context"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// manifestsConsumer is the manifests consumer over the memory doubles.
func manifestsConsumer(t *testing.T) (*kafka.Consumer, *memory.ManifestedUnits) {
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

func manifestedEvent(t *testing.T, id, packageID, orderRef string) []byte {
	t.Helper()
	return envelope(t, kafka.TypePackageManifested, "/warehouse/fulfillment-execution",
		orderRef, id, map[string]any{"package_id": packageID, "order_ref": orderRef})
}

func TestManifestsHandler_ReversesTheOrderRef(t *testing.T) {
	c, units := manifestsConsumer(t)
	// The pinned contract's example: pkg-1029 / wu-8a1f stand for the work
	// unit ord-7c9e's line 3.
	msg := manifestedEvent(t, "evt-1", "pkg-1029", "ord-7c9e-line-3")

	if err := c.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("handle: %v", err)
	}
	unit, ok := units.GetByOrderLine("ord-7c9e", 3)
	if !ok || unit.PackageID != "pkg-1029" {
		t.Fatalf("by order line = %+v ok=%t, want pkg-1029", unit, ok)
	}
	if unit, ok := units.GetByPackage("pkg-1029"); !ok || unit.PackageID != "pkg-1029" {
		t.Fatalf("by package = %+v ok=%t", unit, ok)
	}
}

func TestManifestsHandler_TimeComesFromTheEnvelope(t *testing.T) {
	c, units := manifestsConsumer(t)
	if err := c.HandleMessage(context.Background(), manifestedEvent(t, "evt-1", "pkg-1", "ord-1-line-1")); err != nil {
		t.Fatal(err)
	}
	unit, ok := units.GetByOrderLine("ord-1", 1)
	if !ok {
		t.Fatal("not in the copy")
	}
	// helpers_test.go stamps 2026-10-21T09:30:00Z on every envelope.
	if unit.ManifestedAt.UTC().Format("2006-01-02T15:04:05Z07:00") != "2026-10-21T09:30:00Z" {
		t.Fatalf("manifested_at = %s, want the CloudEvent time attribute", unit.ManifestedAt)
	}
}

func TestManifestsHandler_DedupesOnTheEventID(t *testing.T) {
	c, units := manifestsConsumer(t)
	once := manifestedEvent(t, "evt-1", "pkg-1", "ord-1-line-1")
	if err := c.HandleMessage(context.Background(), once); err != nil {
		t.Fatal(err)
	}
	if err := c.HandleMessage(context.Background(), once); err != nil {
		t.Fatal(err)
	}
	// The redelivered id changed nothing: the package still resolves to the
	// one unit, and a re-manifest would have replaced it (the double's
	// overwrite rule) — it did not fire.
	if unit, ok := units.GetByOrderLine("ord-1", 1); !ok || unit.PackageID != "pkg-1" {
		t.Fatalf("by order line = %+v ok=%t, want the single applied fact", unit, ok)
	}
}

func TestManifestsHandler_IgnoresOtherFulfillmentTypes(t *testing.T) {
	c, units := manifestsConsumer(t)
	for _, typ := range []string{
		"com.warehouse.wes.fulfillment-execution.task.TaskCPTMissed",
		"com.warehouse.wes.fulfillment-execution.package.PackageDiverted",
		"com.warehouse.wes.fulfillment-execution.package.WeightDiscrepancyDetected",
	} {
		msg := envelope(t, typ, "/warehouse/fulfillment-execution", "task-8a1f", "evt-1",
			map[string]any{"task_id": "task-8a1f", "order_ref": "ord-1-line-1"})
		if err := c.HandleMessage(context.Background(), msg); err != nil {
			t.Fatalf("%s: an unconsumed type must be committed past, not failed: %v", typ, err)
		}
	}
	if _, ok := units.GetByOrderLine("ord-1", 1); ok {
		t.Fatal("an unconsumed type must not touch the copy")
	}
}

func TestManifestsHandler_DeadLettersAnUnreversibleOrderRef(t *testing.T) {
	c, _ := manifestsConsumer(t)
	msg := manifestedEvent(t, "evt-1", "pkg-1", "not-the-formula")

	if err := c.HandleMessage(context.Background(), msg); err == nil {
		t.Fatal("an order_ref that is not {order_id}-line-{line_no} can never be applied; it must be dead-lettered")
	}
}

func TestNewManifestsConsumer_Wiring(t *testing.T) {
	uc := &usecases.ApplyManifest{Processed: memory.NewProcessedEvents(), Units: memory.NewManifestedUnits()}
	c := kafka.NewManifestsConsumerForTopic([]string{"b1"}, "topic-x", "group-x", uc, nil)
	if _, ok := c.Handlers[kafka.TypePackageManifested]; !ok {
		t.Fatalf("handlers = %v, want %s", c.Handlers, kafka.TypePackageManifested)
	}
	if len(c.Handlers) != 1 {
		t.Fatalf("handlers = %v, want exactly the consumed type", c.Handlers)
	}
}
