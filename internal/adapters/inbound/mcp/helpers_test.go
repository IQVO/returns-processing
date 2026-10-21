package mcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/returns-processing/internal/adapters/inbound/mcp"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var now = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// harness is a connected in-memory MCP client over the real server wired to
// the in-memory repositories, exactly as cmd/mcp wires them (minus Postgres).
// The write use cases exist ONLY here, to seed data the way the REST adapter
// would: the MCP surface itself has none.
type harness struct {
	session *sdk.ClientSession
	events  *memory.Events
	rmas    *memory.RMAs

	authorizeUC *usecases.AuthorizeReturn
	declareUC   *usecases.DeclareDispositionPolicy
	seedLine    func(orderID string, lineNo int32, sku, pkg string)
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	rmas, policies := memory.NewRMAs(), memory.NewPolicies()
	events := &memory.Events{}
	allocated, manifested := memory.NewAllocatedLines(), memory.NewManifestedUnits()
	ids, clock := memory.NewIDGen(), memory.Clock{T: now}
	deps := inboundmcp.Deps{
		GetReturn:   &usecases.GetReturn{RMAs: rmas},
		ListByOrder: &usecases.ListReturnsByOrder{RMAs: rmas},
		GetPolicy:   &usecases.GetDispositionPolicy{Policies: policies},
	}
	return &harness{
		session: connectSession(t, deps), events: events, rmas: rmas,
		authorizeUC: &usecases.AuthorizeReturn{
			RMAs: rmas, Policies: policies, Allocated: allocated, Manifested: manifested,
			Events: events, IDs: ids, Clock: clock,
		},
		declareUC: &usecases.DeclareDispositionPolicy{Policies: policies, Clock: clock},
		seedLine: func(orderID string, lineNo int32, sku, pkg string) {
			allocated.Put(orderID, lineNo, views.AllocatedLine{SKU: sku})
			manifested.PutByOrderLine(orderID, lineNo, views.ManifestedUnit{PackageID: pkg, ManifestedAt: now})
		},
	}
}

func connectSession(t *testing.T, deps inboundmcp.Deps) *sdk.ClientSession {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := inboundmcp.NewServer(deps).Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := sdk.NewClient(&sdk.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// declare installs the standard total policy v1: window 30 days, cap 5,
// A/B restock-or-refurbish, C liquidates, SCRAP disposes, veto DISPOSE.
func (h *harness) declare(t *testing.T) {
	t.Helper()
	_, err := h.declareUC.Execute(context.Background(), usecases.PolicyDeclaration{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
	})
	if err != nil {
		t.Fatalf("declare policy: %v", err)
	}
}

// authorize seeds one AUTHORIZED RMA of a shipped order line, as POST /returns
// would: an allocated + manifested line, a policy, a request within the cap.
func (h *harness) authorize(t *testing.T, orderID string, lineNo int32, qty int64) string {
	t.Helper()
	h.declare(t)
	h.seedLine(orderID, lineNo, "sku-1", "pkg-1")
	rma, err := h.authorizeUC.Execute(context.Background(), usecases.ReturnRequest{
		OrderID: orderID, LineNo: lineNo, Quantity: qty, SiteID: "SITE1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return rma.ID()
}

// call invokes a tool and fails the test on a TRANSPORT/protocol error;
// tool-level failures come back as res.IsError.
func call(t *testing.T, session *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	res, err := session.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: transport/protocol error (want a tool result): %v", name, err)
	}
	return res
}

// ok calls a tool, requires success, and returns its structured content.
func (h *harness) ok(t *testing.T, name string, args map[string]any) map[string]any {
	t.Helper()
	res := call(t, h.session, name, args)
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, text(res))
	}
	sc, isMap := res.StructuredContent.(map[string]any)
	if !isMap {
		t.Fatalf("%s: structured content = %#v, want an object", name, res.StructuredContent)
	}
	return sc
}

// failWith calls a tool and requires an isError result whose text starts with
// the slug want.
func failWith(t *testing.T, session *sdk.ClientSession, name string, args map[string]any, want string) {
	t.Helper()
	res := call(t, session, name, args)
	if !res.IsError {
		t.Fatalf("%s: expected an isError tool result, got %#v", name, res.StructuredContent)
	}
	if got := text(res); !strings.HasPrefix(got, want+": ") {
		t.Fatalf("%s: error text %q does not start with %q", name, got, want+": ")
	}
}

func text(res *sdk.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, isText := c.(*sdk.TextContent); isText {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}
