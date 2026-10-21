package kafka

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// ProductMasterTopic is product-master's integration topic.
const ProductMasterTopic = "warehouse.product-master.events"

// TypeProductClassified is the only product-master type this service acts on,
// byte-identical to product-master's AsyncAPI. Every other type on the topic
// is ignored.
const TypeProductClassified = "com.warehouse.wms.product-master.product.ProductClassified"

// productClassifiedData mirrors ProductClassified's v1 payload (restated
// here, never imported from the producer). Its extra fields
// (classification_source) are ignored; dot_hazard_class is folded into the
// tag grammar below.
type productClassifiedData struct {
	SKU               string   `json:"sku"`
	HandlingTags      []string `json:"handling_tags"`
	TemperatureClass  string   `json:"temperature_class"`
	DOTHazardClass    int      `json:"dot_hazard_class"`
	ClassificationSrc string   `json:"classification_source"`
	Version           int64    `json:"version"`
}

// ProductClassesHandlers maps the product-master types this service consumes
// to their use case.
func ProductClassesHandlers(uc *usecases.ApplyProductClassification, logger *slog.Logger) map[string]HandlerFunc {
	logger = defaultLogger(logger)
	return map[string]HandlerFunc{
		TypeProductClassified: func(ctx context.Context, e ce.Event) error {
			d, err := decodeData[productClassifiedData](e)
			if err != nil {
				return err
			}
			return apply(ctx, logger, e, func() (usecases.ConsumeOutcome, error) {
				return uc.Handle(ctx, e.ID(), d.SKU, views.ProductClass{
					Tags:             WithDotTag(d.HandlingTags, d.DOTHazardClass),
					TemperatureClass: d.TemperatureClass,
					Version:          d.Version,
				})
			})
		},
	}
}

// NewProductClassesConsumer reads product-master's topic under groupID
// (PRODUCT_CLASSIFICATION_CONSUMER_GROUP, never a literal) and feeds the
// classification local copy (ADR 0003), applying only newer versions.
func NewProductClassesConsumer(brokers []string, groupID string, uc *usecases.ApplyProductClassification, logger *slog.Logger) *Consumer {
	return NewProductClassesConsumerForTopic(brokers, ProductMasterTopic, groupID, uc, logger)
}

// NewProductClassesConsumerForTopic is NewProductClassesConsumer on an
// explicit topic (the integration tests' unique per-test topics).
func NewProductClassesConsumerForTopic(brokers []string, topic, groupID string, uc *usecases.ApplyProductClassification, logger *slog.Logger) *Consumer {
	return newConsumer("product classifications consumer", brokers, topic, groupID, ProductClassesHandlers(uc, logger), logger)
}

// WithDotTag folds the payload's dot_hazard_class rating into the tag
// grammar the disposition guardrails read (views.ProductClass): a `dot:<n>`
// tag, unless the producer already sent one. A rating of 0 means none.
func WithDotTag(tags []string, dot int) []string {
	if dot <= 0 {
		return tags
	}
	for _, t := range tags {
		if strings.HasPrefix(t, "dot:") {
			return tags
		}
	}
	out := make([]string, 0, len(tags)+1)
	out = append(out, tags...)
	return append(out, "dot:"+strconv.Itoa(dot))
}
