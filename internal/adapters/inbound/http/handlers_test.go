package http

import (
	"net/http"
	"strings"
	"testing"
)

// POST /returns: the happy path answers 201 with the RMA, a Location header,
// and the policy window computed from the fixed clock.
func TestAuthorizeReturnRoute(t *testing.T) {
	f := newFixture(t)
	body := `{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`
	status, raw, header := f.call(http.MethodPost, "/returns", body, "k1")
	if status != http.StatusCreated {
		t.Fatalf("authorize = %d %s", status, raw)
	}
	m := decodeBody(t, raw)
	for field, want := range map[string]any{
		"rmaId": "rma-0001", "orderId": "ord-7c9e", "sku": "SKU-1", "status": "AUTHORIZED",
		"reasonCode": "DAMAGED", "windowEndsAt": "2026-11-20T09:00:00Z", "version": float64(1),
	} {
		if got := m[field]; got != want {
			t.Fatalf("%s = %v, want %v (%s)", field, got, want, raw)
		}
	}
	if got := header.Get("Location"); got != "/returns/rma-0001" {
		t.Fatalf("Location = %q", got)
	}
	// The path rma appears on GET straight away.
	f.mustCall(http.MethodGet, "/returns/rma-0001", "", "", http.StatusOK)
}

// POST /returns refusals: every typed authorization error is 409 with its
// rejection code in the detail; the sku field is advisory (a wrong value
// never refuses: the allocations copy is the truth).
func TestAuthorizeReturnRefusals(t *testing.T) {
	f := newFixture(t)
	const path = "/returns"
	cases := []struct {
		name   string
		body   string
		slug   string
		detail string
	}{
		{"unmanifested line", `{"orderId":"ord-alloc","lineNo":1,"sku":"SKU-2","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`,
			"return-not-authorizable", "LINE_NOT_MANIFESTED"},
		{"unallocated line", `{"orderId":"ord-noalloc","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`,
			"return-not-authorizable", "ORDER_LINE_NOT_ALLOCATED"},
		{"quantity over the cap", `{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":9,"siteId":"WH1","reasonCode":"DAMAGED"}`,
			"return-not-authorizable", "QUANTITY_EXCEEDS_POLICY"},
		// A blank order id never reaches the domain's own blank rule: the
		// allocations copy is consulted first and cannot hold a blank line,
		// so the refusal is the fail-closed 409 (spec drift, reported).
		{"blank order id", `{"orderId":"","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`,
			"return-not-authorizable", "ORDER_LINE_NOT_ALLOCATED"},
		{"quantity below one", `{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":0,"siteId":"WH1","reasonCode":"DAMAGED"}`,
			"invalid-quantity", ""},
		{"unknown field", `{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED","warehouse":"X"}`,
			"malformed-request-body", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, raw, _ := f.call(http.MethodPost, path, tc.body, "k-"+tc.name)
			wantProblem(t, status, raw, tc.slug, tc.detail)
			if status != http.StatusConflict && status != http.StatusBadRequest {
				t.Fatalf("status = %d", status)
			}
		})
	}
	// One open RMA per line: the second authorize of the same line refuses.
	f.authorize("ord-7c9e")
	status, raw, _ := f.call(http.MethodPost, path,
		`{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-9","quantity":1,"siteId":"WH1","reasonCode":"DAMAGED"}`, "k-second")
	wantProblem(t, status, raw, "open-rma-exists", "")
	if status != http.StatusConflict {
		t.Fatalf("status = %d", status)
	}
}

// POST /returns with no policy declared: covered by
// TestCurrentPolicyNotDeclared (the fixture there declares none).

// GET /returns/{rmaId} and GET /returns?orderRef=.
func TestReturnReads(t *testing.T) {
	f := newFixture(t)
	status, raw, _ := f.call(http.MethodGet, "/returns/rma-none", "", "")
	wantProblem(t, status, raw, "rma-not-found", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d", status)
	}

	f.authorize("ord-7c9e")
	raw = f.mustCall(http.MethodGet, "/returns/rma-0001", "", "", http.StatusOK)
	if m := decodeBody(t, raw); m["status"] != "AUTHORIZED" {
		t.Fatalf("get = %s", raw)
	}

	// Lists answer 200 with [] on an unknown order, never 404.
	raw = f.mustCall(http.MethodGet, "/returns?orderRef=ord-none", "", "", http.StatusOK)
	if !strings.Contains(raw, `"items":[]`) {
		t.Fatalf("unknown order = %s, want items []", raw)
	}
	raw = f.mustCall(http.MethodGet, "/returns?orderRef=ord-7c9e", "", "", http.StatusOK)
	if !strings.Contains(raw, `"rmaId":"rma-0001"`) {
		t.Fatalf("list = %s", raw)
	}
	status, raw, _ = f.call(http.MethodGet, "/returns", "", "")
	wantProblem(t, status, raw, "malformed-request-body", "orderRef")
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d", status)
	}
}

// POST /returns/{rmaId}/receipts: matched and unexpected lines, the wire
// kind/rmaId mapping, and the refused third state.
func TestOpenReturnReceiptRoute(t *testing.T) {
	f := newFixture(t)
	rma := f.authorize("ord-7c9e")
	body := `{"siteId":"WH1","lines":[` +
		`{"lineNo":1,"sku":"SKU-1","quantity":2,"kind":"MATCHED","rmaId":"` + rma + `"},` +
		`{"lineNo":2,"sku":"SKU-9","quantity":1,"kind":"UNEXPECTED"}]}`
	raw := f.mustCall(http.MethodPost, "/returns/"+rma+"/receipts", body, "k-r1", http.StatusCreated)
	m := decodeBody(t, raw)
	if m["receiptId"] != "rcpt-0001" || m["rmaId"] != rma || m["openedAt"] != "2026-10-21T09:00:00Z" {
		t.Fatalf("receipt = %s", raw)
	}
	lines, _ := m["lines"].([]any)
	if len(lines) != 2 {
		t.Fatalf("lines = %s", raw)
	}
	second, _ := lines[1].(map[string]any)
	if second["kind"] != "UNEXPECTED" {
		t.Fatalf("unexpected line = %v", second)
	}
	if _, has := second["rmaId"]; has {
		t.Fatalf("unexpected line carries an rmaId: %v", second)
	}
	if second["inspected"] != false {
		t.Fatalf("inspected = %v", second)
	}

	cases := []struct {
		name string
		body string
	}{
		{"MATCHED without rmaId", `{"siteId":"WH1","lines":[{"lineNo":1,"sku":"SKU-1","quantity":2,"kind":"MATCHED"}]}`},
		{"UNEXPECTED with rmaId", `{"siteId":"WH1","lines":[{"lineNo":1,"sku":"SKU-1","quantity":2,"kind":"UNEXPECTED","rmaId":"rma-0001"}]}`},
		{"unknown kind", `{"siteId":"WH1","lines":[{"lineNo":1,"sku":"SKU-1","quantity":2,"kind":"MYSTERY"}]}`},
		{"no lines", `{"siteId":"WH1","lines":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, raw, _ := f.call(http.MethodPost, "/returns/"+rma+"/receipts", tc.body, "k-"+tc.name)
			wantProblem(t, status, raw, "invalid-line", "")
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d (%s)", status, raw)
			}
		})
	}
}

// POST /receipts/{receiptId}/lines/{lineNo}/inspect: the disposition is
// decided in the same unit and the InspectionResult carries it.
func TestInspectReturnLineRoute(t *testing.T) {
	f := newFixture(t)
	rma := f.authorize("ord-7c9e")
	receipt := f.openReceipt(rma)
	const body = `{"grade":"B","quantity":2,"reasonCodes":["CUSTOMER_DAMAGE"]}`
	raw := f.mustCall(http.MethodPost, "/receipts/"+receipt+"/lines/1/inspect", body, "k-i1", http.StatusOK)
	m := decodeBody(t, raw)
	if m["receiptId"] != receipt || m["lineNo"] != float64(1) || m["sku"] != "SKU-1" ||
		m["grade"] != "B" || m["inspectedAt"] != "2026-10-21T09:00:00Z" {
		t.Fatalf("inspection result = %s", raw)
	}
	if codes, _ := m["reasonCodes"].([]any); len(codes) != 1 || codes[0] != "CUSTOMER_DAMAGE" {
		t.Fatalf("reasonCodes = %s", raw)
	}
	d, _ := m["disposition"].(map[string]any)
	if d == nil || d["outcome"] != "RESTOCK" || d["staged"] != true || d["policyVersion"] != float64(1) || d["version"] != float64(1) {
		t.Fatalf("disposition = %v", d)
	}
	if d["decidedAt"] != m["inspectedAt"] {
		t.Fatalf("inspectedAt %v != decidedAt %v", m["inspectedAt"], d["decidedAt"])
	}

	// A line is inspected exactly once.
	status, raw2, _ := f.call(http.MethodPost, "/receipts/"+receipt+"/lines/1/inspect", body, "k-i2")
	wantProblem(t, status, raw2, "line-already-inspected", "")
	// An unknown receipt is 404 receipt-not-found; a bad line number is 400;
	// a line the receipt does not hold is 404 naming it.
	status, raw2, _ = f.call(http.MethodPost, "/receipts/rcpt-none/lines/1/inspect", body, "k-i3")
	wantProblem(t, status, raw2, "receipt-not-found", "")
	status, raw2, _ = f.call(http.MethodPost, "/receipts/"+receipt+"/lines/x/inspect", body, "k-i4")
	wantProblem(t, status, raw2, "malformed-request-body", "lineNo")
	status, raw2, _ = f.call(http.MethodPost, "/receipts/"+receipt+"/lines/9/inspect", body, "k-i8")
	wantProblem(t, status, raw2, "receipt-not-found", "no line 9")
	// A second receipt holds an UNEXPECTED line to inspect with fresh state.
	second := f.mustCall(http.MethodPost, "/returns/"+rma+"/receipts",
		`{"siteId":"WH1","lines":[{"lineNo":1,"sku":"SKU-9","quantity":3,"kind":"UNEXPECTED"}]}`, "k-second-receipt", http.StatusCreated)
	secondID := stringField(t, second, "receiptId")
	// An unknown grade and an omitted quantity are 400.
	status, raw2, _ = f.call(http.MethodPost, "/receipts/"+secondID+"/lines/1/inspect", `{"grade":"Z","quantity":1}`, "k-i5")
	wantProblem(t, status, raw2, "invalid-grade", "")
	status, raw2, _ = f.call(http.MethodPost, "/receipts/"+secondID+"/lines/1/inspect", `{"grade":"A"}`, "k-i6")
	wantProblem(t, status, raw2, "invalid-quantity", "")
	// Inspecting more than received is 409.
	status, raw2, _ = f.call(http.MethodPost, "/receipts/"+secondID+"/lines/1/inspect", `{"grade":"A","quantity":9}`, "k-i7")
	wantProblem(t, status, raw2, "inspected-exceeds-received", "")
}

// POST /dispositions/{dispositionId}/override: a new disposition carrying
// supersedes and the reason.
func TestOverrideDispositionRoute(t *testing.T) {
	f := newFixture(t)
	rma := f.authorize("ord-7c9e")
	receipt := f.openReceipt(rma)
	f.mustCall(http.MethodPost, "/receipts/"+receipt+"/lines/1/inspect", `{"grade":"B","quantity":2}`, "k-inspect", http.StatusOK)

	raw := f.mustCall(http.MethodPost, "/dispositions/disp-0001/override",
		`{"outcome":"RTV","reason":"vendor accepts open-box returns this quarter"}`, "k-ov1", http.StatusOK)
	m := decodeBody(t, raw)
	if m["dispositionId"] != "disp-0002" || m["supersedes"] != "disp-0001" ||
		m["reason"] != "vendor accepts open-box returns this quarter" || m["outcome"] != "RTV" {
		t.Fatalf("override = %s", raw)
	}

	status, body, _ := f.call(http.MethodPost, "/dispositions/disp-none/override", `{"outcome":"RTV","reason":"x"}`, "k-ov2")
	wantProblem(t, status, body, "disposition-not-found", "")
	status, body, _ = f.call(http.MethodPost, "/dispositions/disp-0001/override", `{"outcome":"RTV","reason":"  "}`, "k-ov3")
	wantProblem(t, status, body, "override-reason-required", "")
}

// PUT /disposition-policies/{version} and GET /disposition-policies/current.
func TestPolicyRoutes(t *testing.T) {
	f := newFixture(t)
	const table = `{"returnWindowDays":30,"maxUnitsPerLine":10,"vetoOutcome":"DISPOSE","rules":[` +
		`{"grade":"A","productClass":"*","outcome":"RESTOCK"},{"grade":"B","productClass":"*","outcome":"REFURBISH"},` +
		`{"grade":"C","productClass":"*","outcome":"LIQUIDATE"},{"grade":"SCRAP","productClass":"*","outcome":"DISPOSE"}]}`
	raw := f.mustCall(http.MethodPut, "/disposition-policies/2", table, "k-p2", http.StatusCreated)
	m := decodeBody(t, raw)
	if m["version"] != float64(2) || m["vetoOutcome"] != "DISPOSE" {
		t.Fatalf("declare = %s", raw)
	}
	rules, _ := m["rules"].([]any)
	if len(rules) != 4 {
		t.Fatalf("rules = %s", raw)
	}

	raw = f.mustCall(http.MethodGet, "/disposition-policies/current", "", "", http.StatusOK)
	if m := decodeBody(t, raw); m["version"] != float64(2) {
		t.Fatalf("current = %s", raw)
	}

	// A non-total table is 400 invalid-policy; a bad version is 400.
	status, body, _ := f.call(http.MethodPut, "/disposition-policies/3",
		`{"returnWindowDays":30,"maxUnitsPerLine":10,"rules":[{"grade":"A","productClass":"*","outcome":"RESTOCK"}]}`, "k-p3")
	wantProblem(t, status, body, "invalid-policy", "")
	status, body, _ = f.call(http.MethodPut, "/disposition-policies/x", table, "k-p4")
	wantProblem(t, status, body, "malformed-request-body", "version")
}

// GET /disposition-policies/current before any declaration: 404
// policy-not-found.
func TestCurrentPolicyNotDeclared(t *testing.T) {
	f := newFixtureWithoutPolicy(t)
	status, raw, _ := f.call(http.MethodGet, "/disposition-policies/current", "", "")
	wantProblem(t, status, raw, "policy-not-found", "")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d", status)
	}
	// And POST /returns then answers 409 naming the missing policy.
	status, raw, _ = f.call(http.MethodPost, "/returns",
		`{"orderId":"ord-7c9e","lineNo":1,"sku":"SKU-1","quantity":2,"siteId":"WH1","reasonCode":"DAMAGED"}`, "k-noconf")
	wantProblem(t, status, raw, "return-not-authorizable", "no disposition policy version is declared")
}
