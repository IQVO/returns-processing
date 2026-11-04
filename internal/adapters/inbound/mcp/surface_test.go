// The MCP surface beyond tools (Agentic Standard v2, AS-3): ONE resource —
// the scoped return read model — and ONE prompt — this context's operator
// SOP. Pinned through the same in-memory client the tool tests use.
package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The resource template is advertised with the fleet URI shape.
func TestSurface_ResourceTemplateListed(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()

	listed, err := h.session.ListResourceTemplates(ctx, nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	found := false
	for _, rt := range listed.ResourceTemplates {
		if rt.URITemplate == "warehouse://returns-processing/returns/{rmaId}" {
			found = true
			if rt.MIMEType != "application/json" {
				t.Errorf("template MIMEType = %q, want application/json", rt.MIMEType)
			}
		}
	}
	if !found {
		t.Fatalf("return resource template not advertised: %+v", listed.ResourceTemplates)
	}
}

// Reading the resource returns the same read model as the get_return tool.
func TestSurface_ResourceTemplateRead(t *testing.T) {
	h := newHarness(t)
	id := h.authorize(t, "ord-7c9e", 3, 2)

	res, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{
		URI: "warehouse://returns-processing/returns/" + id,
	})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(res.Contents) != 1 {
		t.Fatalf("contents = %d entries, want 1", len(res.Contents))
	}
	c := res.Contents[0]
	if c.URI != "warehouse://returns-processing/returns/"+id {
		t.Errorf("content URI = %q", c.URI)
	}
	var v struct {
		RMAID      string `json:"rma_id"`
		OrderID    string `json:"order_id"`
		LineNo     int32  `json:"line_no"`
		SKU        string `json:"sku"`
		Status     string `json:"status"`
		SiteID     string `json:"site_id"`
		ReasonCode string `json:"reason_code"`
		Version    int64  `json:"version"`
	}
	if err := json.Unmarshal([]byte(c.Text), &v); err != nil {
		t.Fatalf("resource text is not the return JSON: %v (%s)", err, c.Text)
	}
	if v.RMAID != id || v.OrderID != "ord-7c9e" || v.LineNo != 3 || v.SKU != "sku-1" ||
		v.Status != "AUTHORIZED" || v.SiteID != "SITE1" || v.ReasonCode != "DAMAGED" || v.Version != 1 {
		t.Errorf("return resource = %+v, want rma %s of ord-7c9e line 3 AUTHORIZED", v, id)
	}
}

// A malformed URI and an unknown rma id are errors, never silent defaults.
func TestSurface_ResourceRejectsMalformedAndUnknown(t *testing.T) {
	h := newHarness(t)
	id := h.authorize(t, "ord-1", 1, 1)
	for _, uri := range []string{
		"staffing://returns-processing/returns/" + id,             // wrong scheme
		"warehouse://returns-processing/policies/" + id,           // wrong model
		"warehouse://returns-processing/returns/",                 // empty rmaId
		"warehouse://returns-processing/returns/" + id + "/extra", // trailing segment
		"warehouse://returns-processing/returns/rma unknown",      // white space in the id
		"warehouse://returns-processing/returns/rma-404",          // unknown id
	} {
		if _, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("read resource %q: expected an error", uri)
		}
	}
}

// The prompt is advertised with its typed argument and returns the SOP text.
func TestSurface_PromptListedAndGet(t *testing.T) {
	h := newHarness(t)
	id := h.authorize(t, "ord-1", 1, 1)
	ctx := t.Context()

	listed, err := h.session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(listed.Prompts) != 1 || listed.Prompts[0].Name != "triage_return" {
		t.Fatalf("prompts = %+v, want exactly triage_return", listed.Prompts)
	}
	if len(listed.Prompts[0].Arguments) != 1 || listed.Prompts[0].Arguments[0].Name != "rmaId" || !listed.Prompts[0].Arguments[0].Required {
		t.Fatalf("prompt arguments = %+v, want one required rmaId", listed.Prompts[0].Arguments)
	}

	res, err := h.session.GetPrompt(ctx, &sdk.GetPromptParams{
		Name:      "triage_return",
		Arguments: map[string]string{"rmaId": id},
	})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(res.Messages))
	}
	text := res.Messages[0].Content.(*sdk.TextContent).Text
	for _, needle := range []string{"get_return", "list_returns_by_order", "get_disposition_policy", id} {
		if !strings.Contains(text, needle) {
			t.Errorf("SOP text does not mention %q", needle)
		}
	}
	if strings.Contains(text, "{{rmaId}}") {
		t.Errorf("SOP text still carries the unsubstituted placeholder")
	}

	// A missing argument degrades to the placeholder, never to a hidden call.
	noArg, err := h.session.GetPrompt(ctx, &sdk.GetPromptParams{Name: "triage_return"})
	if err != nil {
		t.Fatalf("get prompt without arguments: %v", err)
	}
	if plain := noArg.Messages[0].Content.(*sdk.TextContent).Text; !strings.Contains(plain, "<rmaId>") {
		t.Errorf("SOP without an argument should carry the <rmaId> placeholder")
	}
}
