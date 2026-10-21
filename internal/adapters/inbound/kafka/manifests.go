package kafka

import (
	"context"
	"log/slog"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// FulfillmentTopic is fulfillment-execution's integration topic.
const FulfillmentTopic = "warehouse.fulfillment.events"

// TypePackageManifested is the only fulfillment-execution type this service
// acts on, byte-identical to fulfillment-execution's AsyncAPI. Every other
// type on the topic (TaskCPTMissed, PackageDiverted,
// WeightDiscrepancyDetected, ...) is ignored — exception-management
// consumes those, not returns-processing.
const TypePackageManifested = "com.warehouse.wes.fulfillment-execution.package.PackageManifested"

// packageManifestedData mirrors PackageManifested's v1 payload (restated
// here, never imported): the physical package and order_ref, the WES work
// unit id `{order_id}-line-{line_no}` the use case reverses.
type packageManifestedData struct {
	PackageID string `json:"package_id"`
	OrderRef  string `json:"order_ref"`
}

// ManifestHandlers maps the fulfillment-execution types this service
// consumes to their use case.
func ManifestHandlers(uc *usecases.ApplyManifest, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypePackageManifested: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[packageManifestedData](e)
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.ConsumeOutcome, error) {
				// The payload carries no timestamp: the fact's occurred-at
				// is the CloudEvent `time` attribute.
				return uc.Handle(ctx, e.ID(), d.OrderRef, d.PackageID, e.Time())
			})
		},
	}
}

// NewManifestsConsumer reads fulfillment-execution's topic under groupID
// (MANIFEST_CONSUMER_GROUP, never a literal) and feeds the package-manifest
// local copy (ADR 0003), reversing order_ref's
// `{order_id}-line-{line_no}` formula into the (order, line) key.
func NewManifestsConsumer(brokers []string, groupID string, uc *usecases.ApplyManifest, logger *slog.Logger) *Consumer {
	return NewManifestsConsumerForTopic(brokers, FulfillmentTopic, groupID, uc, logger)
}

// NewManifestsConsumerForTopic is NewManifestsConsumer on an explicit topic
// (the integration tests' unique per-test topics).
func NewManifestsConsumerForTopic(brokers []string, topic, groupID string, uc *usecases.ApplyManifest, logger *slog.Logger) *Consumer {
	return newConsumer("manifests consumer", brokers, topic, groupID, ManifestHandlers(uc, logger), logger)
}
