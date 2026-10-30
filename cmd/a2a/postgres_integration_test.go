//go:build integration

package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	inbounda2a "github.com/claudioed/returns-processing/internal/adapters/inbound/a2a"
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
// already migrated, a second run must be a no-op), then reads through A2A
// skills, over the real router and the real JSON-RPC wire, exactly what the
// write use cases persisted, and its reads add no outbox row: the surface is
// read-only all the way down.
func TestA2AAgainstPostgresReadsWhatTheAPIWroteAndNeverWrites(t *testing.T) {
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
	srv := httptest.NewServer(newAgent(deps, testAgentURL, "test"))
	t.Cleanup(srv.Close)
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: srv.URL + "/a2a", Version: "test"})
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		t.Fatal(err)
	}

	// call sends one skill request and returns the envelope's data (or fails
	// on a problem / transport error).
	call := func(skill string, args map[string]any) map[string]any {
		t.Helper()
		resp, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser,
			a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))})
		if err != nil {
			t.Fatalf("%s: %v", skill, err)
		}
		msg, ok := resp.(*a2a.Message)
		if !ok || len(msg.Parts) == 0 {
			t.Fatalf("%s: reply = %#v", skill, resp)
		}
		data, ok := msg.Parts[0].Content.(a2a.Data)
		if !ok {
			t.Fatalf("%s: first part = %T", skill, msg.Parts[0].Content)
		}
		raw, _ := json.Marshal(data.Value)
		var env struct {
			Data    map[string]any `json:"data"`
			Problem map[string]any `json:"problem"`
		}
		if err := json.Unmarshal(raw, &env); err != nil || env.Problem != nil {
			t.Fatalf("%s: envelope %s (%v)", skill, raw, err)
		}
		return env.Data
	}
	r := call("return_status", map[string]any{"rmaId": rma.ID()})["return"].(map[string]any)
	if r["status"] != "AUTHORIZED" || r["orderId"] != "ord-7c9e" || r["workUnit"] != "ord-7c9e-line-3" || r["quantity"] != float64(2) {
		t.Fatalf("return = %v", r)
	}
	if list := call("return_status", map[string]any{"orderId": "ord-7c9e"})["returns"].(map[string]any)["items"].([]any); len(list) != 1 {
		t.Fatalf("returns of the order = %v", list)
	}
	for _, args := range []map[string]any{{}, {"version": 1}} {
		if p := call("disposition_policy", args)["policy"].(map[string]any); p["version"] != float64(1) ||
			p["maxUnitsPerLine"] != float64(5) || p["vetoOutcome"] != "DISPOSE" {
			t.Fatalf("policy %v = %v", args, p)
		}
	}

	// A miss is an in-band 404 problem, not a transport error.
	resp, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser,
		a2a.NewDataPart(map[string]any{"skill": "return_status", "arguments": map[string]any{"rmaId": "rma-unknown"}}))})
	if err != nil {
		t.Fatalf("unknown rma: %v", err)
	}
	if text, _ := resp.(*a2a.Message).Parts[1].Content.(a2a.Text); !strings.Contains(string(text), "(404)") {
		t.Fatalf("unknown rma summary = %q, want a 404 problem", text)
	}

	if after := pgtest.Count(t, pool, "outbox"); after != outboxBefore {
		t.Fatalf("outbox rows %d -> %d: the A2A reads wrote", outboxBefore, after)
	}
}
