//go:build integration

package kafka_test

import (
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/testing/kafkatest"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

// The three read-model consumers, proven end to end on real Kafka and real
// Postgres (testcontainers): real CloudEvents of the producers' exact types
// are published on the consumed topics, the production consumers feed the
// production read models, and every fleet rule is asserted where it lives —
// the id dedupe (same CloudEvents id twice = one effect), the version gating
// of the classification copy, the order_ref reversal, the unknown-type skip,
// the invalid-envelope WARN-and-commit-past, and the bounded-retry-then-
// dead-letter path whose rolled-back claims leave no processed_events rows.

// TestProductClassesConsumer_RealKafkaAndPostgres proves the classification
// copy: apply, id dedupe, version gating, the unknown-type skip, the invalid
// envelope commit-past and the poison dead-letter, all in one stream.
func TestProductClassesConsumer_RealKafkaAndPostgres(t *testing.T) {
	topic, dlq := newTopic(t, kafka.ProductMasterTopic)
	group := groupFor("classifications", topic)
	pool := pgtest.NewPool(t)
	uc := &usecases.ApplyProductClassification{
		UnitOfWork: postgres.NewUnitOfWork(pool),
		Processed:  postgres.NewProcessedEvents(pool),
		Classes:    postgres.NewProductClasses(pool),
	}
	runConsumer(t, kafka.NewProductClassesConsumerForTopic(kafkatest.Brokers(t), topic, group, uc, itLogger(t)))

	classified := func(id, sku string, version int64, tags []string) kafkatest.Message {
		return kafkatest.Message{Key: sku, Value: envelope(t, kafka.TypeProductClassified,
			"/warehouse/product-master", sku, id,
			map[string]any{"sku": sku, "handling_tags": tags, "temperature_class": "Ambient",
				"dot_hazard_class": 3, "classification_source": "native", "version": version})}
	}
	renamed := envelope(t, "com.warehouse.wms.product-master.product.ProductRenamed",
		"/warehouse/product-master", "SKU-1", "evt-renamed", map[string]any{"sku": "SKU-1", "version": 3})

	kafkatest.Produce(t, topic,
		classified("evt-1", "SKU-1", 3, []string{"hazmat", "class:Standard"}), // applied
		// The SAME id again carrying v9: a duplicate — the id, not the
		// payload, is the dedupe key, so the copy must stay at v3.
		classified("evt-1", "SKU-1", 9, []string{"class:Premium"}),
		// A DIFFERENT id carrying an OLDER version: applied but store-gated
		// (the LOCAL COPY RULE) — the copy still holds v3.
		classified("evt-2", "SKU-1", 2, []string{"class:Standard"}),
		// An unknown product-master type: skipped, committed past.
		kafkatest.Message{Key: "SKU-1", Value: renamed},
		// The retired flat envelope: WARN, committed past.
		kafkatest.Message{Key: "flat", Value: legacyFlatEnvelope()},
		// A blank sku: deterministic poison — dead-lettered at once.
		classified("evt-poison", " ", 3, nil),
		classified("evt-3", "SKU-2", 1, nil), // applied
	)

	classes := postgres.NewProductClasses(pool)
	eventually(t, "both classifications to land", func() bool {
		return pgtest.Count(t, pool, "product_classes") == 2
	})
	// SKU-1 kept v3 (the duplicate and the older version changed nothing)
	// and carries the folded dot:3 tag.
	var temp string
	var version int64
	var tags string
	pgtest.Scan(t, pool, `SELECT temperature_class, version, tags FROM product_classes WHERE sku = $1`,
		[]any{"SKU-1"}, &temp, &version, &tags)
	if temp != "Ambient" || version != 3 {
		t.Fatalf("SKU-1 = %s v%d, want Ambient v3 (dedupe + version gate)", temp, version)
	}
	if pc, ok := classes.Get("SKU-1"); !ok || !containsTag(pc.Tags, "dot:3") {
		t.Fatalf("SKU-1 tags = %v, want the folded dot:3", pc.Tags)
	}

	// Exactly the three applied events claimed a row; the skipped and the
	// dead-lettered messages never did.
	if n := pgtest.Count(t, pool, "processed_events"); n != 3 {
		t.Fatalf("processed_events = %d, want 3 (evt-1, evt-2, evt-3)", n)
	}
	// The poison message landed on the DLQ, verbatim.
	dead := kafkatest.ReadN(t, dlq, 1, 60*time.Second)[0]
	if dead.Key != " " || dead.Headers["x-dlq-source-topic"] != topic {
		t.Fatalf("dead letter = %q from %v, want the blank-sku fact of %s", dead.Key, dead.Headers["x-dlq-source-topic"], topic)
	}
	// Seven messages in, seven offsets committed: everything moved past.
	assertCommittedPast(t, group, topic, 7)
}

func containsTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

// TestAllocationsConsumer_RealKafkaAndPostgres proves the allocations copy:
// every line of one event under ONE claim, both order-management types, the
// id dedupe holding the line's version at 1, and the invalid-envelope
// commit-past.
func TestAllocationsConsumer_RealKafkaAndPostgres(t *testing.T) {
	topic, _ := newTopic(t, kafka.OrderManagementTopic)
	group := groupFor("order-lines", topic)
	pool := pgtest.NewPool(t)
	uc := &usecases.ApplyAllocations{
		UnitOfWork: postgres.NewUnitOfWork(pool),
		Processed:  postgres.NewProcessedEvents(pool),
		Lines:      postgres.NewAllocatedLines(pool),
	}
	runConsumer(t, kafka.NewAllocationsConsumerForTopic(kafkatest.Brokers(t), topic, group, uc, itLogger(t)))

	allocated := func(typ, id, orderID string, lines ...map[string]any) kafkatest.Message {
		return kafkatest.Message{Key: orderID, Value: envelope(t, typ, "/warehouse/order-management",
			orderID, id, map[string]any{"order_id": orderID, "promise_date": "2026-09-09T10:01:00Z", "lines": lines})}
	}

	kafkatest.Produce(t, topic,
		allocated(kafka.TypeOrderAllocated, "evt-1", "ord-7c9e",
			map[string]any{"line_no": 1, "sku": "SKU-BOOK-0001", "path_id": "pick", "gift_wrap": false, "fulfillment_class": "SAME_SKU_MULTI"},
			map[string]any{"line_no": 2, "sku": "SKU-2", "path_id": "multis"}),
		// The SAME id redelivered: one effect — the local version counter
		// stays at 1, proving the claim and the writes committed once.
		allocated(kafka.TypeOrderAllocated, "evt-1", "ord-7c9e",
			map[string]any{"line_no": 1, "sku": "SKU-BOOK-0001", "path_id": "pick"}),
		allocated(kafka.TypeOrderPartiallyAllocated, "evt-2", "ord-7c9e",
			map[string]any{"line_no": 3, "sku": "SKU-3", "path_id": "pick"}),
		kafkatest.Message{Key: "garbage", Value: []byte("{not a cloud event")},
	)

	lines := postgres.NewAllocatedLines(pool)
	eventually(t, "the three allocated lines to land", func() bool {
		return pgtest.Count(t, pool, "allocated_lines") == 3
	})
	if line, ok := lines.Get("ord-7c9e", 1); !ok || line.SKU != "SKU-BOOK-0001" {
		t.Fatalf("ord-7c9e line 1 = %+v ok=%t", line, ok)
	}
	if line, ok := lines.Get("ord-7c9e", 3); !ok || line.SKU != "SKU-3" {
		t.Fatalf("ord-7c9e line 3 = %+v ok=%t (OrderPartiallyAllocated)", line, ok)
	}
	for _, lineNo := range []int32{1, 2, 3} {
		var version int64
		pgtest.Scan(t, pool, `SELECT version FROM allocated_lines WHERE order_id = $1 AND line_no = $2`,
			[]any{"ord-7c9e", lineNo}, &version)
		if version != 1 {
			t.Fatalf("line %d version = %d, want 1 (the redelivered id applied once)", lineNo, version)
		}
	}
	if n := pgtest.Count(t, pool, "processed_events"); n != 2 {
		t.Fatalf("processed_events = %d, want 2 (evt-1, evt-2)", n)
	}
	assertCommittedPast(t, group, topic, 4)
}

// TestManifestsConsumer_RealKafkaAndPostgres proves the package-manifest
// copy: the order_ref reversal, the id dedupe, and the bounded-retry-then-
// dead-letter path — a manifest whose package id collides with another work
// unit's fails deterministically at the store, its claim ROLLS BACK with
// every attempt (no processed_events row), it lands on the DLQ after the
// retry cap, and the messages behind it still apply.
func TestManifestsConsumer_RealKafkaAndPostgres(t *testing.T) {
	topic, dlq := newTopic(t, kafka.FulfillmentTopic)
	group := groupFor("manifests", topic)
	pool := pgtest.NewPool(t)
	uc := &usecases.ApplyManifest{
		UnitOfWork: postgres.NewUnitOfWork(pool),
		Processed:  postgres.NewProcessedEvents(pool),
		Units:      postgres.NewManifestedUnits(pool),
	}
	runConsumer(t, kafka.NewManifestsConsumerForTopic(kafkatest.Brokers(t), topic, group, uc, itLogger(t)))

	manifested := func(id, packageID, orderRef string) kafkatest.Message {
		return kafkatest.Message{Key: orderRef, Value: envelope(t, kafka.TypePackageManifested,
			"/warehouse/fulfillment-execution", orderRef, id,
			map[string]any{"package_id": packageID, "order_ref": orderRef})}
	}

	kafkatest.Produce(t, topic,
		manifested("m1", "pkg-1029", "ord-7c9e-line-3"), // the pinned contract's example
		manifested("m1", "pkg-1029", "ord-7c9e-line-3"), // the SAME id: a duplicate
		// pkg-1029 belongs to ord-7c9e's line 3: another work unit claiming
		// it violates the package unique index every attempt — after the
		// retry cap it is dead-lettered and committed past.
		manifested("m2", "pkg-1029", "ord-other-line-1"),
		manifested("m3", "pkg-0055", "ord-7c9e-line-4"), // still applies behind the poison
	)

	units := postgres.NewManifestedUnits(pool)
	eventually(t, "the two good manifests to land", func() bool {
		return pgtest.Count(t, pool, "manifested_units") == 2
	})
	// The reversal: order_ref ord-7c9e-line-3 keyed as (ord-7c9e, 3).
	unit, ok := units.GetByOrderLine("ord-7c9e", 3)
	if !ok || unit.PackageID != "pkg-1029" {
		t.Fatalf("by order line = %+v ok=%t, want pkg-1029", unit, ok)
	}
	// manifested_at is the CloudEvent time attribute (helpers stamp
	// 2026-10-21T09:30:00Z on every envelope).
	if got := unit.ManifestedAt.UTC().Format(time.RFC3339); got != "2026-10-21T09:30:00Z" {
		t.Fatalf("manifested_at = %s, want the envelope time", got)
	}
	if unit, ok := units.GetByPackage("pkg-0055"); !ok || unit.PackageID != "pkg-0055" {
		t.Fatalf("by package = %+v ok=%t", unit, ok)
	}
	var workUnit, orderID string
	var lineNo int32
	pgtest.Scan(t, pool, `SELECT work_unit, order_id, line_no FROM manifested_units WHERE package_id = $1`,
		[]any{"pkg-1029"}, &workUnit, &orderID, &lineNo)
	if workUnit != "ord-7c9e-line-3" || orderID != "ord-7c9e" || lineNo != 3 {
		t.Fatalf("reversed key = (%s, %s, %d), want (ord-7c9e-line-3, ord-7c9e, 3)", workUnit, orderID, lineNo)
	}
	var version int64
	pgtest.Scan(t, pool, `SELECT version FROM manifested_units WHERE work_unit = $1`,
		[]any{"ord-7c9e-line-3"}, &version)
	if version != 1 {
		t.Fatalf("version = %d, want 1 (the redelivered id applied once)", version)
	}

	// Only the two good manifests claimed; m2's claim rolled back with every
	// failed attempt (the dedupe and the effect share ONE transaction).
	eventually(t, "the poison manifest to be dead-lettered", func() bool {
		return pgtest.Count(t, pool, "processed_events") == 2
	})
	dead := kafkatest.ReadN(t, dlq, 1, 60*time.Second)[0]
	if dead.Key != "ord-other-line-1" || dead.Headers["x-dlq-source-topic"] != topic {
		t.Fatalf("dead letter = %q from %v", dead.Key, dead.Headers["x-dlq-source-topic"])
	}
	assertCommittedPast(t, group, topic, 4)
}
