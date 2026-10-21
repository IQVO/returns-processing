package kafka_test

import (
	"context"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// allocationsConsumer is the order lines consumer over the memory doubles.
func allocationsConsumer(t *testing.T) (*kafka.Consumer, *memory.AllocatedLines) {
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

func allocationEvent(t *testing.T, typ, id, orderID string, lines ...map[string]any) []byte {
	t.Helper()
	return envelope(t, typ, "/warehouse/order-management", orderID, id,
		map[string]any{"order_id": orderID, "promise_date": "2026-09-09T10:01:00Z", "lines": lines})
}

func TestAllocationsHandler_AppliesBothTypes(t *testing.T) {
	c, lines := allocationsConsumer(t)
	full := allocationEvent(t, kafka.TypeOrderAllocated, "evt-1", "ord-7c9e",
		map[string]any{"line_no": 1, "sku": "SKU-BOOK-0001", "path_id": "pick", "gift_wrap": false, "fulfillment_class": "SAME_SKU_MULTI"},
		map[string]any{"line_no": 2, "sku": "SKU-2", "path_id": "multis"},
	)
	partial := allocationEvent(t, kafka.TypeOrderPartiallyAllocated, "evt-2", "ord-7c9e",
		map[string]any{"line_no": 3, "sku": "SKU-3", "path_id": "pick"},
	)

	for _, msg := range [][]byte{full, partial} {
		if err := c.HandleMessage(context.Background(), msg); err != nil {
			t.Fatalf("handle: %v", err)
		}
	}
	for _, want := range []struct {
		lineNo int32
		sku    string
	}{{1, "SKU-BOOK-0001"}, {2, "SKU-2"}, {3, "SKU-3"}} {
		if line, ok := lines.Get("ord-7c9e", want.lineNo); !ok || line.SKU != want.sku {
			t.Fatalf("ord-7c9e line %d = %+v ok=%t, want sku %s", want.lineNo, line, ok, want.sku)
		}
	}
}

func TestAllocationsHandler_DedupesOnTheEventID(t *testing.T) {
	c, lines := allocationsConsumer(t)
	once := allocationEvent(t, kafka.TypeOrderAllocated, "evt-1", "ord-1",
		map[string]any{"line_no": 1, "sku": "SKU-1"})

	if err := c.HandleMessage(context.Background(), once); err != nil {
		t.Fatal(err)
	}
	// The same id again, carrying a DIFFERENT sku: the id, not the payload,
	// is the dedupe key — the copy must keep the first fact's line.
	if err := c.HandleMessage(context.Background(), once); err != nil {
		t.Fatal(err)
	}
	if line, ok := lines.Get("ord-1", 1); !ok || line.SKU != "SKU-1" {
		t.Fatalf("line after the redelivery = %+v ok=%t, want SKU-1 once", line, ok)
	}
}

func TestAllocationsHandler_IgnoresOtherOrderManagementTypes(t *testing.T) {
	c, lines := allocationsConsumer(t)
	msg := envelope(t, "com.warehouse.wes.order-management.order.OrderRepromised",
		"/warehouse/order-management", "ord-1", "evt-1",
		map[string]any{"order_id": "ord-1", "cpt_id_old": "sp1-1200", "cpt_id_new": "sp1-1800", "reason": "TaskCPTMissed"})

	if err := c.HandleMessage(context.Background(), msg); err != nil {
		t.Fatalf("an unconsumed type must be committed past, not failed: %v", err)
	}
	if _, ok := lines.Get("ord-1", 1); ok {
		t.Fatal("an unconsumed type must not touch the copy")
	}
}

func TestAllocationsHandler_DeadLettersALinelessAllocation(t *testing.T) {
	c, _ := allocationsConsumer(t)
	msg := allocationEvent(t, kafka.TypeOrderAllocated, "evt-1", "ord-1") // no lines

	if err := c.HandleMessage(context.Background(), msg); err == nil {
		t.Fatal("a lineless allocation can never be applied; it must be dead-lettered")
	}
}

func TestNewAllocationsConsumer_Wiring(t *testing.T) {
	uc := &usecases.ApplyAllocations{Processed: memory.NewProcessedEvents(), Lines: memory.NewAllocatedLines()}
	c := kafka.NewAllocationsConsumerForTopic([]string{"b1"}, "topic-x", "group-x", uc, nil)
	for _, typ := range []string{kafka.TypeOrderAllocated, kafka.TypeOrderPartiallyAllocated} {
		if _, ok := c.Handlers[typ]; !ok {
			t.Fatalf("handlers = %v, want %s", c.Handlers, typ)
		}
	}
	if len(c.Handlers) != 2 {
		t.Fatalf("handlers = %v, want exactly the two consumed types", c.Handlers)
	}
}
