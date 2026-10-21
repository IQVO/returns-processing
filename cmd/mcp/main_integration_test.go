//go:build integration

package main

import (
	"context"
	"os"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/returns-processing/internal/adapters/inbound/mcp"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/shared"
	"github.com/claudioed/returns-processing/internal/testing/pgtest"
)

func TestMain(m *testing.M) {
	code := m.Run()
	pgtest.Shutdown()
	os.Exit(code)
}

// The binary migrates a database itself (idempotently: pgtest's databases are
// already migrated, a second run must be a no-op), then reads through MCP
// tools exactly what the write use cases persisted, and its reads add no
// outbox row: the surface is read-only all the way down.
func TestMCPAgainstPostgresReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
	url := pgtest.URL(t) // a fresh, already migrated database
	ctx := context.Background()
	pool := pgtest.Open(t, url)

	// Seed through the write use cases, as cmd/api wires them: the local
	// copies a return is authorized against, the policy, then the RMA.
	allocated, manifested := postgres.NewAllocatedLines(pool), postgres.NewManifestedUnits(pool)
	rmas, policies := postgres.NewRMAs(pool), postgres.NewPolicies(pool)
	events, uow := postgres.NewOutbox(pool), postgres.NewUnitOfWork(pool)
	ids, clock := postgres.NewIDGen(), postgres.NewClock()

	steps := []func() error{
		func() error { return allocated.ApplyAllocatedLine(ctx, "ord-7c9e", 3, "sku-1") },
		func() error {
			return manifested.ApplyManifestedUnit(ctx, "ord-7c9e", 3, views.ManifestedUnit{
				PackageID: "pkg-1", ManifestedAt: clock.Now(),
			})
		},
	}
	for _, step := range steps {
		if err := step(); err != nil {
			t.Fatalf("seed local copy: %v", err)
		}
	}
	if _, err := (&usecases.DeclareDispositionPolicy{Policies: policies, Clock: clock}).Execute(ctx, usecases.PolicyDeclaration{
		Version: 1, ReturnWindowDays: 30, MaxUnitsPerLine: 5,
		Rules: []disposition.Rule{
			{Grade: shared.GradeA, ProductClass: "*", Outcome: disposition.OutcomeRestock},
			{Grade: shared.GradeB, ProductClass: "*", Outcome: disposition.OutcomeRefurbish},
			{Grade: shared.GradeC, ProductClass: "*", Outcome: disposition.OutcomeLiquidate},
			{Grade: shared.GradeScrap, ProductClass: "*", Outcome: disposition.OutcomeDispose},
		},
	}); err != nil {
		t.Fatalf("declare policy: %v", err)
	}
	rma, err := (&usecases.AuthorizeReturn{
		RMAs: rmas, Policies: policies, Allocated: allocated, Manifested: manifested,
		Events: events, IDs: ids, Clock: clock, UnitOfWork: uow,
	}).Execute(ctx, usecases.ReturnRequest{
		OrderID: "ord-7c9e", LineNo: 3, Quantity: 2, SiteID: "SITE1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	outboxBefore := pgtest.Count(t, pool, "outbox")
	if outboxBefore < 1 {
		t.Fatalf("seeding left %d outbox rows, want the declared and authorized facts", outboxBefore)
	}

	deps, closeFn, err := buildDeps(ctx, quietLogger(), url, url)
	if err != nil {
		t.Fatalf("buildDeps: %v", err)
	}
	t.Cleanup(closeFn)
	ct, st := sdk.NewInMemoryTransports()
	server, err := inboundmcp.NewServer(deps).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, err := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, err := session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || res.IsError {
			t.Fatalf("%s: err=%v res=%+v", name, err, res)
		}
		return res.StructuredContent.(map[string]any)
	}
	if r := call("get_return", map[string]any{"rma_id": rma.ID()}); r["status"] != "AUTHORIZED" ||
		r["order_id"] != "ord-7c9e" || r["work_unit"] != "ord-7c9e-line-3" || r["quantity"] != float64(2) {
		t.Fatalf("return = %v", r)
	}
	if list := call("list_returns_by_order", map[string]any{"order_id": "ord-7c9e"})["items"].([]any); len(list) != 1 {
		t.Fatalf("returns of the order = %v", list)
	}
	if p := call("get_disposition_policy", map[string]any{}); p["version"] != float64(1) ||
		p["max_units_per_line"] != float64(5) {
		t.Fatalf("policy = %v", p)
	}
	if v := call("get_disposition_policy", map[string]any{"version": 1}); v["veto_outcome"] != "DISPOSE" {
		t.Fatalf("policy v1 = %v", v)
	}

	if after := pgtest.Count(t, pool, "outbox"); after != outboxBefore {
		t.Fatalf("outbox rows %d -> %d: the MCP reads wrote", outboxBefore, after)
	}
}
