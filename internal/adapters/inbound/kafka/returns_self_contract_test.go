package kafka_test

import (
	"strings"
	"testing"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/kafka"
	cloudevents "github.com/claudioed/returns-processing/internal/adapters/kafka/cloudevents"
	outboundkafka "github.com/claudioed/returns-processing/internal/adapters/outbound/kafka"
	"github.com/claudioed/returns-processing/internal/application/outbox"
)

// Pinned-contract tests for the SELF-pin: this service's own
// apis/asyncapi.yaml, byte-copied to testdata and registered in
// contracts/pinned.json with this repo as its own producer. The pin keeps the
// published catalog honest against the encoder (the outbox relay's single
// wire form) and documents the events topic's .dlq companion.
const returnsSelfPinnedSpec = "testdata/returns-processing.asyncapi.yaml"

// selfCatalog is the outbox encoder's published catalogue (ADR 0004):
// aggregate segment -> the event names the segment publishes. Provenance:
// internal/adapters/outbound/kafka/encoder.go's `published` map (restated
// here, never imported — it is unexported), the same way the consumed types
// are restated from the producers' specs.
var selfCatalog = []struct {
	entity string
	event  string
}{
	{outbox.AggregRMA, "ReturnAuthorized"},
	{outbox.AggregRMA, "ReturnAuthorizationRejected"},
	{outbox.AggregReceipt, "ReturnReceiptOpened"},
	{outbox.AggregReceipt, "ReturnLineInspected"},
	{outbox.AggregReceipt, "UnexpectedReturnReceived"},
	{outbox.AggregDisposition, "DispositionDecided"},
	{outbox.AggregDisposition, "ReturnLineStaged"},
}

// E1 (self): every event the outbox encoder can publish is documented on the
// integration topic this repo owns (outboundkafka.TopicEvents) AND mirrored
// on its analytics twin (outboundkafka.TopicAnalytics), with the wire content
// types the fleet envelope mandates.
func TestOwnPinCoversTheEncoderCatalog(t *testing.T) {
	doc := loadPinnedSpec(t, returnsSelfPinnedSpec)
	requirePinnedDefaultContentType(t, doc)
	events := pinnedChannelTypes(t, doc, outboundkafka.TopicEvents)
	analytics := pinnedChannelTypes(t, doc, outboundkafka.TopicAnalytics)
	for _, entry := range selfCatalog {
		wantType := cloudevents.Type(entry.entity, entry.event)
		eventsKey, ok := events[wantType]
		if !ok {
			t.Errorf("own spec no longer documents %s on %s", wantType, outboundkafka.TopicEvents)
			continue
		}
		analyticsKey, ok := analytics[wantType]
		if !ok {
			t.Errorf("own spec no longer mirrors %s on %s", wantType, outboundkafka.TopicAnalytics)
			continue
		}
		requirePinnedMessageContentType(t, doc, eventsKey)
		requirePinnedMessageContentType(t, doc, analyticsKey)
	}
}

// E3 (self): the own spec's examples carry the fleet CloudEvents 1.0
// invariants with THIS service's envelope — source and dataschema exactly
// what internal/adapters/kafka/cloudevents builds — on both streams.
func TestOwnPinEnvelopeInvariants(t *testing.T) {
	doc := loadPinnedSpec(t, returnsSelfPinnedSpec)
	for _, channel := range []struct {
		topic  string
		stream string
	}{
		{outboundkafka.TopicEvents, cloudevents.StreamEvents},
		{outboundkafka.TopicAnalytics, cloudevents.StreamAnalytics},
	} {
		types := pinnedChannelTypes(t, doc, channel.topic)
		for _, entry := range selfCatalog {
			key, ok := types[cloudevents.Type(entry.entity, entry.event)]
			if !ok {
				continue // E1 (self) already reported the missing twin
			}
			assertSelfExampleInvariants(t, doc, key, channel.stream, entry.event)
		}
	}
}

// assertSelfExampleInvariants checks every example of one own message: the
// envelope invariants, this service's source, and the dataschema the encoder
// builds for the stream.
func assertSelfExampleInvariants(t *testing.T, doc *pinnedSpecDoc, key, stream, event string) {
	t.Helper()
	schema := pinnedMessageSchema(t, doc, key)
	wantSchema := cloudevents.DataSchema(stream, event, 1)
	for _, ex := range doc.Components.Messages[key].Examples {
		payload := ex.Payload
		requirePinnedEnvelope(t, payload)
		if src, _ := payload["source"].(string); src != cloudevents.Source {
			t.Errorf("%s/%s: source = %q, want %q", key, stream, src, cloudevents.Source)
		}
		if payload["dataschema"] != wantSchema {
			t.Errorf("%s: dataschema = %v, want %q", key, payload["dataschema"], wantSchema)
		}
		assertEnvelopeConstsMatch(t, schema, payload)
	}
}

// The events topic's .dlq companion (outboundkafka.TopicDLQ, where the relay
// dead-letters rows that can never be encoded) stays documented on the events
// channel — the pin keeps the operator contract visible.
func TestOwnPinDocumentsTheEventsDLQ(t *testing.T) {
	doc := loadPinnedSpec(t, returnsSelfPinnedSpec)
	ch, ok := doc.Channels[outboundkafka.TopicEvents]
	if !ok {
		t.Fatalf("own spec does not document %s", outboundkafka.TopicEvents)
	}
	if !strings.Contains(ch.Description, outboundkafka.TopicDLQ) {
		t.Errorf("the %s description no longer documents the %s companion", outboundkafka.TopicEvents, outboundkafka.TopicDLQ)
	}
	// The consumed restatements stay attributed: this service's own spec
	// restates the three producers' contracts it consumes (ADR 0003).
	for _, consumed := range []string{kafka.ProductMasterTopic, kafka.OrderManagementTopic, kafka.FulfillmentTopic} {
		if _, ok := doc.Channels[consumed]; !ok {
			t.Errorf("own spec no longer restates the consumed channel %s", consumed)
		}
	}
}
