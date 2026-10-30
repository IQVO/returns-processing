package main

import (
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	inbounda2a "github.com/claudioed/returns-processing/internal/adapters/inbound/a2a"
)

// newAgent composes the A2A surface over the wired read use cases: the Agent
// Card advertising agentURL and the HTTP router around the JSON-RPC handler.
// The adapter never writes, so no EventPublisher, UnitOfWork or Clock is
// wired, and it calls no other context.
func newAgent(deps inbounda2a.Deps, agentURL, version string) http.Handler {
	card := inbounda2a.Card(inbounda2a.Config{AgentURL: agentURL, Version: version})
	return newRouter(inbounda2a.Handler(inbounda2a.NewExecutor(deps)), card)
}

// newRouter wraps the A2A handler in the process's HTTP surface:
//
//   - GET /healthz  -> 200 {"status":"ok"} (open; liveness/readiness probes).
//   - GET /.well-known/agent-card.json -> the Agent Card (the discovery
//     contract, ADR 0008).
//   - /  and /a2a   -> the JSON-RPC endpoint. Both paths are served so the
//     canonical root and the fleet's <CTX>_A2A_ENDPOINT convention
//     (".../a2a", mirroring cmd/mcp's dual "/mcp" mount) both work.
//
// No auth middleware is ever added here (TestNoAuthMiddlewareReintroduced):
// A2A is unauthenticated by the same fleet-wide decision as REST and MCP.
func newRouter(a2aHandler http.Handler, card *a2a.AgentCard) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle("/a2a", a2aHandler)
	mux.Handle("/", a2aHandler)
	return mux
}
