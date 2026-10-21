package mcp_test

import (
	"context"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/usecases"
)

func TestGetReturn(t *testing.T) {
	h := newHarness(t)
	id := h.authorize(t, "ord-7c9e", 3, 2)

	got := h.ok(t, "get_return", map[string]any{"rma_id": id})
	if got["rma_id"] != id || got["order_id"] != "ord-7c9e" || got["line_no"] != float64(3) {
		t.Fatalf("identity wrong: %v", got)
	}
	if got["work_unit"] != "ord-7c9e-line-3" || got["sku"] != "sku-1" || got["quantity"] != float64(2) {
		t.Fatalf("line facts wrong: %v", got)
	}
	if got["site_id"] != "SITE1" || got["reason_code"] != "DAMAGED" || got["status"] != "AUTHORIZED" {
		t.Fatalf("request facts wrong: %v", got)
	}
	if got["window_ends_at"] != "2026-11-20T09:00:00Z" || got["version"] != float64(1) {
		t.Fatalf("window/version wrong: %v", got)
	}
	if _, has := got["rejection_code"]; has {
		t.Fatalf("rejection_code on an authorized return: %v", got)
	}
}

func TestListReturnsByOrder(t *testing.T) {
	h := newHarness(t)
	h.declare(t)
	h.seedLine("ord-1", 5, "sku-1", "pkg-1")
	h.seedLine("ord-1", 2, "sku-1", "pkg-1")
	h.seedLine("ord-2", 1, "sku-1", "pkg-1")
	for _, line := range []int32{5, 2} {
		if _, err := h.authorizeUC.Execute(context.Background(), usecases.ReturnRequest{
			OrderID: "ord-1", LineNo: line, Quantity: 1, SiteID: "SITE1", ReasonCode: "DAMAGED",
		}); err != nil {
			t.Fatalf("authorize line %d: %v", line, err)
		}
	}

	page := h.ok(t, "list_returns_by_order", map[string]any{"order_id": "ord-1"})
	items := page["items"].([]any)
	if len(items) != 2 ||
		items[0].(map[string]any)["line_no"] != float64(2) ||
		items[1].(map[string]any)["line_no"] != float64(5) {
		t.Fatalf("items not ordered by line 2 then 5: %v", page)
	}
	first := page["items"].([]any)[0].(map[string]any)
	if first["status"] != "AUTHORIZED" || first["work_unit"] != "ord-1-line-2" {
		t.Fatalf("item shape: %v", first)
	}

	// An unknown order answers an EMPTY list, never an error (the REST rule).
	empty := h.ok(t, "list_returns_by_order", map[string]any{"order_id": "ord-9"})
	if items, _ := empty["items"].([]any); len(items) != 0 {
		t.Fatalf("unknown order = %v, want an empty list", empty)
	}
}

func TestGetDispositionPolicyCurrentAndVersion(t *testing.T) {
	h := newHarness(t)
	h.declare(t) // v1: window 30, cap 5

	cur := h.ok(t, "get_disposition_policy", map[string]any{})
	if cur["version"] != float64(1) || cur["return_window_days"] != float64(30) ||
		cur["max_units_per_line"] != float64(5) || cur["veto_outcome"] != "DISPOSE" {
		t.Fatalf("current policy = %v", cur)
	}
	if cur["declared_at"] != "2026-10-21T09:00:00Z" {
		t.Fatalf("declared_at = %v", cur["declared_at"])
	}
	rules := cur["rules"].([]any)
	if len(rules) != 4 {
		t.Fatalf("rules = %v", rules)
	}
	first := rules[0].(map[string]any)
	if first["grade"] != "A" || first["product_class"] != "*" || first["outcome"] != "RESTOCK" {
		t.Fatalf("first rule = %v", first)
	}

	// The optional version argument reads that declared version.
	v1 := h.ok(t, "get_disposition_policy", map[string]any{"version": 1})
	if v1["version"] != float64(1) {
		t.Fatalf("version(1) = %v", v1)
	}
}

func TestToolErrorsCarryTheRESTSlug(t *testing.T) {
	h := newHarness(t)
	h.authorize(t, "ord-1", 1, 1)
	cases := []struct {
		tool, slug string
		args       map[string]any
	}{
		{"get_return", "rma-not-found", map[string]any{"rma_id": "rma-unknown"}},
		{"get_return", "invalid-rma-id", map[string]any{"rma_id": ""}},
		{"get_return", "invalid-rma-id", map[string]any{"rma_id": "has space"}},
		{"list_returns_by_order", "invalid-order-id", map[string]any{"order_id": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.tool+"/"+tc.slug, func(t *testing.T) { failWith(t, h.session, tc.tool, tc.args, tc.slug) })
	}

	// A database with NO policy declared: the current read and a version read
	// are both policy-not-found.
	bare := newHarness(t)
	failWith(t, bare.session, "get_disposition_policy", map[string]any{}, "policy-not-found")
	failWith(t, bare.session, "get_disposition_policy", map[string]any{"version": 9}, "policy-not-found")
}

// Reads never enqueue an outbox event: the MCP surface has no write path at
// all (the only Events here belong to the seeding use cases).
func TestReadsWriteNothing(t *testing.T) {
	h := newHarness(t)
	id := h.authorize(t, "ord-1", 1, 1)
	before := len(h.events.RMAEventNames())

	h.ok(t, "get_return", map[string]any{"rma_id": id})
	h.ok(t, "list_returns_by_order", map[string]any{"order_id": "ord-1"})
	h.ok(t, "get_disposition_policy", map[string]any{})

	if after := len(h.events.RMAEventNames()); after != before {
		t.Fatalf("reads added %d outbox events", after-before)
	}
}
