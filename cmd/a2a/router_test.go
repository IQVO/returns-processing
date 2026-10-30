package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	inbounda2a "github.com/claudioed/returns-processing/internal/adapters/inbound/a2a"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// newTestAgent builds the router exactly as run() does (newAgent), over
// in-memory repositories holding one policy and one AUTHORIZED RMA. The memory
// adapter is a test double: imported by this _test file only, never by the
// binary (TestRuntimeBinaryHasNoInMemoryAdapters). It returns the RMA id.
func newTestAgent(t *testing.T) (http.Handler, string) {
	t.Helper()
	ctx := context.Background()
	rmas, policies := memory.NewRMAs(), memory.NewPolicies()
	allocated, manifested := memory.NewAllocatedLines(), memory.NewManifestedUnits()
	clock := memory.Clock{T: testNow}
	if _, err := (&usecases.DeclareDispositionPolicy{Policies: policies, Clock: clock}).Execute(ctx, usecases.PolicyDeclaration{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
	}); err != nil {
		t.Fatal(err)
	}
	allocated.Put("ord-1", 1, views.AllocatedLine{SKU: "sku-1"})
	manifested.PutByOrderLine("ord-1", 1, views.ManifestedUnit{PackageID: "pkg-1", ManifestedAt: testNow})
	rma, err := (&usecases.AuthorizeReturn{
		RMAs: rmas, Policies: policies, Allocated: allocated, Manifested: manifested,
		Events: &memory.Events{}, IDs: memory.NewIDGen(), Clock: clock,
	}).Execute(ctx, usecases.ReturnRequest{OrderID: "ord-1", LineNo: 1, Quantity: 1, SiteID: "SITE1", ReasonCode: "DAMAGED"})
	if err != nil {
		t.Fatal(err)
	}
	deps := inbounda2a.Deps{
		GetReturn:   &usecases.GetReturn{RMAs: rmas},
		ListByOrder: &usecases.ListReturnsByOrder{RMAs: rmas},
		GetPolicy:   &usecases.GetDispositionPolicy{Policies: policies},
	}
	return newAgent(deps, testAgentURL, "test"), rma.ID()
}

func TestRouter_HealthzServesOK(t *testing.T) {
	h, _ := newTestAgent(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz: got %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != `{"status":"ok"}` {
		t.Fatalf("GET /healthz body = %q, want {\"status\":\"ok\"}", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("GET /healthz Content-Type = %q, want application/json", ct)
	}
}

func TestRouter_ServesTheAgentCardAtTheWellKnownPath(t *testing.T) {
	h, _ := newTestAgent(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET agent card: got %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{`"name":"returns-processing"`, testAgentURL, `"return_status"`, `"disposition_policy"`} {
		if !strings.Contains(body, want) {
			t.Errorf("agent card lacks %s: %s", want, body)
		}
	}
}

// TestRouter_BothMountsReachTheJSONRPCHandler pins the fleet's
// <CTX>_A2A_ENDPOINT convention: the endpoint answers at "/" and at "/a2a",
// and the use cases behind it are the wired ones (the seeded RMA is read).
func TestRouter_BothMountsReachTheJSONRPCHandler(t *testing.T) {
	h, rmaID := newTestAgent(t)
	for _, path := range []string{"/", "/a2a"} {
		t.Run(path, func(t *testing.T) {
			body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"data":{"skill":"return_status","arguments":{"rmaId":"` + rmaID + `"}}}]}}}`
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("A2A-Version", "1.0")
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			raw, _ := io.ReadAll(rec.Body)
			if rec.Code == http.StatusNotFound || !strings.Contains(string(raw), `"jsonrpc"`) {
				t.Fatalf("POST %s: %d %q does not look like a JSON-RPC reply", path, rec.Code, raw)
			}
			if !strings.Contains(string(raw), `"status":"AUTHORIZED"`) {
				t.Fatalf("POST %s: reply %q lacks the seeded RMA read through the wired use case", path, raw)
			}
		})
	}
}

// No credentials are asked for, whatever the caller sends.
func TestRouter_NoAuthIsRequired(t *testing.T) {
	h, _ := newTestAgent(t)
	for _, auth := range []string{"", "Bearer nonsense"} {
		req := httptest.NewRequest(http.MethodGet, "/.well-known/agent-card.json", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("card with Authorization=%q = %d, want 200", auth, rec.Code)
		}
	}
}

// TestRouter_ACardFetchedFromTheBinaryIsDialable runs the discovery flow a
// delegating agent runs against the real router: fetch the card, build a
// client from it, call a skill.
func TestRouter_ACardFetchedFromTheBinaryIsDialable(t *testing.T) {
	h, rmaID := newTestAgent(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	res, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath)
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("card fetch = %v / %v", res, err)
	}
	defer func() { _ = res.Body.Close() }()
	var card a2a.AgentCard
	if err := json.NewDecoder(res.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	// The card advertises the configured URL, not the test server's: point the
	// client at the test server the way a gateway-rewritten route would.
	card.SupportedInterfaces[0].URL = srv.URL + "/a2a"
	client, err := a2aclient.NewFromCard(context.Background(), &card)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": "return_status", "arguments": map[string]any{"orderId": "ord-1"}}))})
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T", resp)
	}
	if text, _ := msg.Parts[1].Content.(a2a.Text); !strings.Contains(string(text), rmaID) {
		t.Fatalf("summary = %q, want the seeded RMA listed", text)
	}
}
