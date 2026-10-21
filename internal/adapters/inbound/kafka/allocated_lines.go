package kafka

import (
	"context"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// OrderManagementTopic is order-management's integration topic.
const OrderManagementTopic = "warehouse.order-management.events"

// The order-management types this service acts on, byte-identical to
// order-management's AsyncAPI. Every other type on the topic is ignored.
const (
	TypeOrderAllocated          = "com.warehouse.wes.order-management.order.OrderAllocated"
	TypeOrderPartiallyAllocated = "com.warehouse.wes.order-management.order.OrderPartiallyAllocated"
)

// allocationLine is one line of an allocation fact. The sources carry NO
// quantity (an allocated line is identified by its number and its sku);
// path_id, gift_wrap and fulfillment_class are ignored.
type allocationLine struct {
	LineNo int32  `json:"line_no"`
	SKU    string `json:"sku"`
}

// allocationData mirrors OrderAllocated's and OrderPartiallyAllocated's v1
// payloads (restated here, never imported): the order, its promise date and
// the lines the fact covers — for a partial allocation, only the allocated
// ones. promise_date is ignored (the return window starts at authorization).
type allocationData struct {
	OrderID     string           `json:"order_id"`
	PromiseDate string           `json:"promise_date"`
	Lines       []allocationLine `json:"lines"`
}

// AllocationHandlers maps the order-management types this service consumes
// to their use case. Both types feed the same copy: OrderPartiallyAllocated
// carries the same shape over only the allocated lines.
func AllocationHandlers(uc *usecases.ApplyAllocations, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	allocate := func(ctx context.Context, e ce.Event) error {
		d, err := decodeData[allocationData](e)
		if err != nil {
			return err
		}
		lines := make([]usecases.AllocatedLineIn, 0, len(d.Lines))
		for _, l := range d.Lines {
			lines = append(lines, usecases.AllocatedLineIn{LineNo: l.LineNo, SKU: l.SKU})
		}
		return apply(ctx, logger, e, func() (usecases.ConsumeOutcome, error) {
			return uc.Handle(ctx, e.ID(), d.OrderID, lines)
		})
	}
	return map[string]HandlerFunc{
		TypeOrderAllocated:          allocate,
		TypeOrderPartiallyAllocated: allocate,
	}
}

// NewAllocationsConsumer reads order-management's topic under groupID
// (ORDER_LINES_CONSUMER_GROUP, never a literal) and feeds the allocations
// local copy (ADR 0003).
func NewAllocationsConsumer(brokers []string, groupID string, uc *usecases.ApplyAllocations, logger *slog.Logger) *Consumer {
	return NewAllocationsConsumerForTopic(brokers, OrderManagementTopic, groupID, uc, logger)
}

// NewAllocationsConsumerForTopic is NewAllocationsConsumer on an explicit
// topic (the integration tests' unique per-test topics).
func NewAllocationsConsumerForTopic(brokers []string, topic, groupID string, uc *usecases.ApplyAllocations, logger *slog.Logger) *Consumer {
	return newConsumer("order lines consumer", brokers, topic, groupID, AllocationHandlers(uc, logger), logger)
}
