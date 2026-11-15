package kafka_test

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Shared machinery for the per-producer pinned-AsyncAPI contract tests
// (archetypes E1-E4, warehouse-harness-template kit). Each producer this
// service consumes has its own contract_test file listing its pinned copy,
// its reader-config topic constant and the CloudEvents types its consumer
// handles; this file only knows how to read an AsyncAPI document and the
// fleet's CloudEvents envelope conventions. It follows the wave-1 helpers
// (inventory-storage) with one adaptation: this repo's Consumer.HandleMessage
// takes the raw message value, so fixtures marshal straight to []byte.
//
// Fleet reality encoded here: the pinned specs are channel-perspective (the
// producer's OWN topics sit under 'subscribe:', the topics it consumes under
// 'publish:'), but that is dominant, not uniform — channel message collection
// accepts every shape, and E1 is anchored on THIS repo's reader-config topic
// constants either way. The pinned spec is the message catalog.

const (
	pinnedCEContentType = "application/cloudevents+json"
	pinnedJSONBody      = "application/json"
	pinnedURNPrefix     = "urn:warehouse:"
	pinnedURNSuffix     = ":v1"
)

type pinnedSpecDoc struct {
	DefaultContentType string                   `yaml:"defaultContentType"`
	Channels           map[string]pinnedChannel `yaml:"channels"`
	Components         struct {
		Messages map[string]pinnedSpecMessage `yaml:"messages"`
		Schemas  map[string]any               `yaml:"schemas"`
	} `yaml:"components"`
}

type pinnedChannel struct {
	Description string               `yaml:"description"`
	Messages    map[string]pinnedRef `yaml:"messages"`
	Subscribe   *pinnedOp            `yaml:"subscribe"`
	Publish     *pinnedOp            `yaml:"publish"`
}

type pinnedOp struct {
	Message *pinnedOpMessage `yaml:"message"`
}

type pinnedOpMessage struct {
	OneOf []pinnedRef `yaml:"oneOf"`
	Ref   string      `yaml:"$ref"`
}

type pinnedRef struct {
	Ref string `yaml:"$ref"`
}

type pinnedSpecMessage struct {
	Name        string              `yaml:"name"`
	ContentType string              `yaml:"contentType"`
	Payload     any                 `yaml:"payload"`
	Examples    []pinnedSpecExample `yaml:"examples"`
}

type pinnedSpecExample struct {
	Name    string         `yaml:"name"`
	Payload map[string]any `yaml:"payload"`
}

func loadPinnedSpec(t *testing.T, file string) *pinnedSpecDoc {
	t.Helper()
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read pinned spec %s (copy it from the producer, never hand-write it): %v", file, err)
	}
	var doc pinnedSpecDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse pinned spec %s: %v", file, err)
	}
	return &doc
}

// pinnedMessageKeys lists the components.messages keys a channel declares,
// across the fleet's AsyncAPI shapes (channel-level messages map, or the 2.x
// subscribe/publish operation message oneOf / single $ref).
func pinnedMessageKeys(t *testing.T, doc *pinnedSpecDoc, channel string) []string {
	t.Helper()
	ch, ok := doc.Channels[channel]
	if !ok {
		t.Fatalf("pinned spec does not document channel %q", channel)
	}
	var keys []string
	for _, ref := range ch.Messages {
		keys = append(keys, pinnedRefName(ref.Ref))
	}
	for _, op := range []*pinnedOp{ch.Subscribe, ch.Publish} {
		if op == nil || op.Message == nil {
			continue
		}
		for _, ref := range op.Message.OneOf {
			keys = append(keys, pinnedRefName(ref.Ref))
		}
		if op.Message.Ref != "" {
			keys = append(keys, pinnedRefName(op.Message.Ref))
		}
	}
	if len(keys) == 0 {
		t.Fatalf("channel %q declares no message catalog", channel)
	}
	return keys
}

func pinnedRefName(ref string) string {
	return strings.TrimPrefix(ref, "#/components/messages/")
}

// pinnedMessageSchema resolves a message's payload: either inline or a $ref
// into components.schemas.
func pinnedMessageSchema(t *testing.T, doc *pinnedSpecDoc, key string) map[string]any {
	t.Helper()
	msg, ok := doc.Components.Messages[key]
	if !ok {
		t.Fatalf("message %q is not in components.messages", key)
	}
	if payload, ok := msg.Payload.(map[string]any); ok {
		if ref, ok := payload["$ref"].(string); ok {
			name := strings.TrimPrefix(ref, "#/components/schemas/")
			schema, ok := doc.Components.Schemas[name].(map[string]any)
			if !ok {
				t.Fatalf("payload $ref %q does not resolve", ref)
			}
			return schema
		}
		return payload
	}
	t.Fatalf("message %q has no object payload", key)
	return nil
}

// pinnedSchemaConst reads properties.<attr>.const out of a schema's allOf
// branches (the fleet's CloudEvents envelope narrowing shape).
func pinnedSchemaConst(schema map[string]any, attr string) (string, bool) {
	branches, _ := schema["allOf"].([]any)
	branches = append(branches, schema)
	for _, b := range branches {
		branch, ok := b.(map[string]any)
		if !ok {
			continue
		}
		props, _ := branch["properties"].(map[string]any)
		node, _ := props[attr].(map[string]any)
		if c, ok := node["const"].(string); ok {
			return c, true
		}
	}
	return "", false
}

// pinnedMessageType returns the full CloudEvents `type` a channel message
// carries, from the schema const (falling back to its first example).
func pinnedMessageType(t *testing.T, doc *pinnedSpecDoc, key string) string {
	t.Helper()
	if c, ok := pinnedSchemaConst(pinnedMessageSchema(t, doc, key), "type"); ok {
		return c
	}
	for _, ex := range doc.Components.Messages[key].Examples {
		if ty, _ := ex.Payload["type"].(string); ty != "" {
			return ty
		}
	}
	t.Fatalf("message %q declares no CloudEvents type const or example type", key)
	return ""
}

// pinnedChannelTypes maps every CloudEvents type on a channel to its message
// key: the E1 view of "what the pinned catalog offers".
func pinnedChannelTypes(t *testing.T, doc *pinnedSpecDoc, channel string) map[string]string {
	t.Helper()
	types := make(map[string]string)
	for _, key := range pinnedMessageKeys(t, doc, channel) {
		types[pinnedMessageType(t, doc, key)] = key
	}
	return types
}

// pinnedExamples returns every example payload of the given type on the
// channel (spec examples are the first-choice fixtures, per the kit).
func pinnedExamples(t *testing.T, doc *pinnedSpecDoc, channel, wantType string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, key := range pinnedMessageKeys(t, doc, channel) {
		if pinnedMessageType(t, doc, key) != wantType {
			continue
		}
		for _, ex := range doc.Components.Messages[key].Examples {
			if ex.Payload["type"] == wantType {
				out = append(out, ex.Payload)
			}
		}
	}
	return out
}

// requirePinnedEnvelope (E3) asserts the CloudEvents 1.0 invariants every
// fleet envelope carries, on a concrete payload.
func requirePinnedEnvelope(t *testing.T, payload map[string]any) {
	t.Helper()
	str := func(key string) string { v, _ := payload[key].(string); return v }
	if str("specversion") != "1.0" {
		t.Errorf("specversion = %q, want 1.0", str("specversion"))
	}
	for _, attr := range []string{"id", "source", "type", "subject", "time"} {
		if str(attr) == "" {
			t.Errorf("envelope attribute %q is missing/empty", attr)
		}
	}
	if str("datacontenttype") != pinnedJSONBody {
		t.Errorf("datacontenttype = %q, want %s", str("datacontenttype"), pinnedJSONBody)
	}
	if ds := str("dataschema"); !strings.HasPrefix(ds, pinnedURNPrefix) || !strings.HasSuffix(ds, pinnedURNSuffix) {
		t.Errorf("dataschema = %q, want a %s...%s urn", ds, pinnedURNPrefix, pinnedURNSuffix)
	}
	if _, ok := payload["data"].(map[string]any); !ok {
		t.Errorf("data is not an object: %T", payload["data"])
	}
}

// requirePinnedMessageContentType (E3) asserts the wire content type the
// producer documents for every handled message: the message's own
// contentType, or — for producers that style their integration messages with
// the document default only (order-management) — an empty per-message value
// backed by defaultContentType = application/cloudevents+json.
func requirePinnedMessageContentType(t *testing.T, doc *pinnedSpecDoc, key string) {
	t.Helper()
	ct := doc.Components.Messages[key].ContentType
	if ct == "" && doc.DefaultContentType == pinnedCEContentType {
		return
	}
	if ct != pinnedCEContentType {
		t.Errorf("message %q contentType = %q, want %s", key, ct, pinnedCEContentType)
	}
}

func requirePinnedDefaultContentType(t *testing.T, doc *pinnedSpecDoc) {
	t.Helper()
	if doc.DefaultContentType != pinnedCEContentType {
		t.Errorf("defaultContentType = %q, want %s", doc.DefaultContentType, pinnedCEContentType)
	}
}

// pinnedWithUnknownDataKey (E4) deep-ish-copies a payload and injects a data
// field no pinned schema declares: additive producer evolution the decoders
// must tolerate (encoding/json ignores unknown keys).
func pinnedWithUnknownDataKey(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	const unknownKey = "x_future_contract_field"
	data, _ := payload["data"].(map[string]any)
	if data == nil {
		t.Fatal("payload has no data object to perturb")
	}
	clone := pinnedCloneEnvelope(payload)
	dataClone := make(map[string]any, len(data)+1)
	for k, v := range data {
		dataClone[k] = v
	}
	dataClone[unknownKey] = "added-after-pinning"
	clone["data"] = dataClone
	return clone
}

// pinnedCloneEnvelope shallow-copies the envelope attributes of a payload.
func pinnedCloneEnvelope(payload map[string]any) map[string]any {
	clone := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		clone[k] = v
	}
	return clone
}

// pinnedEventValue marshals a payload into the bytes a consumer sees.
func pinnedEventValue(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	value, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal pinned payload: %v", err)
	}
	return value
}

// assertEnvelopeConstsMatch (E3) asserts every envelope const the pinned
// schema narrows (type, source, dataschema, ...) is what the example carries.
func assertEnvelopeConstsMatch(t *testing.T, schema map[string]any, payload map[string]any) {
	t.Helper()
	for _, attr := range []string{"type", "source", "dataschema", "datacontenttype", "specversion"} {
		if c, ok := pinnedSchemaConst(schema, attr); ok && payload[attr] != c {
			t.Errorf("example %v: %s = %v, want pinned const %q", payload["id"], attr, payload[attr], c)
		}
	}
}

// pinnedNum coerces a yaml.v3 / json number (int | int64 | float64) to int64
// so assertions on decoded DTO fields never panic on the producer's own
// examples.
func pinnedNum(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return -1
	}
}

// pinnedItoa formats a coerced number for the tag grammar assertions.
func pinnedItoa(n int64) string { return strconv.FormatInt(n, 10) }

// pinnedSkipFixture builds a minimal VALID CloudEvents 1.0 envelope for a
// documented type this consumer does NOT handle (provenance: synthesized
// envelope, not a spec example — it exists to prove the skip policy, not the
// producer's payload). Routing is on the full `type`; source/dataschema are
// the producer's own forms.
func pinnedSkipFixture(t *testing.T, wantType, source string) map[string]any {
	t.Helper()
	entity := wantType[strings.LastIndex(wantType, ".")+1:]
	return map[string]any{
		"specversion":     "1.0",
		"id":              "skip-" + entity + "-0001",
		"source":          source,
		"type":            wantType,
		"subject":         "skip-" + entity,
		"time":            "2026-11-01T00:00:00Z",
		"datacontenttype": pinnedJSONBody,
		"dataschema":      pinnedURNPrefix + source[strings.LastIndex(source, "/")+1:] + ":events:" + entity + pinnedURNSuffix,
		"data":            map[string]any{"synthesized": true},
	}
}
