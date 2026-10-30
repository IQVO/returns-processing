// Package a2a is the inbound Agent2Agent adapter: it exposes this bounded
// context to the fleet's agent ecosystem as a third driving adapter over the
// same application-layer read use cases the REST and MCP adapters use. It is
// built on the official A2A Go SDK (a2aproject/a2a-go) and speaks JSON-RPC 2.0
// over HTTP, with the Agent Card served at the well-known path.
//
// Per ADR 0008, A2A is the DELEGATION channel between bounded contexts: an
// agent client sends a goal-shaped skill request as a structured message part
// and this agent answers from its own state through its own use cases. It is
// not a data-sync channel: facts between contexts still travel exclusively as
// CloudEvents on Kafka, published through the transactional outbox
// (ADR 0005).
//
// This agent is READ-ONLY, exactly like the MCP adapter (docs/docs/mcp/tools.md): both
// skills read the RMA and disposition-policy state. Authorizing or rejecting
// a return, opening a receipt, inspecting a line, overriding a disposition
// and declaring a policy stay on REST. This package never writes, holds no
// write use case, no unit of work and no event publisher, and contains no
// outbound A2A client.
//
// Like the MCP adapter, this package depends inward on the application layer
// (use cases) and the domain only, never on an outbound adapter. The
// composition root (cmd/a2a) wires the concrete repositories into the use
// cases. Domain structs never leak across the message boundary.
package a2a

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// Config carries the deployment-specific identity of this agent: the absolute
// URL clients use to reach its JSON-RPC endpoint (advertised on the Agent
// Card) and the card version. It is supplied by the composition root from
// A2A_AGENT_URL; the card MUST advertise a URL the fleet's agent clients can
// actually dial (in-cluster: http://<release>-a2a.<ns>.svc.cluster.local:8093/).
type Config struct {
	AgentURL string
	Version  string
}

// Deps is everything the A2A skills need, injected by the composition root.
// It carries the SAME read use cases the REST and MCP adapters use (GET
// /returns/{rmaId}, GET /returns?orderRef=, GET /disposition-policies/current
// and the MCP-only version read); the adapter never constructs an outbound
// adapter itself and holds no write use case at all (the architecture
// fitness tests pin the allowed use case set).
type Deps struct {
	GetReturn   *usecases.GetReturn
	ListByOrder *usecases.ListReturnsByOrder
	GetPolicy   *usecases.GetDispositionPolicy
}

// Card builds this agent's Agent Card (ADR 0008). The surface is two curated,
// goal-shaped READ skills derived from the three MCP tools (docs/docs/mcp/tools.md):
// return_status folds get_return and list_returns_by_order into the one
// question a delegating agent asks ("where is this return?"), and
// disposition_policy is get_disposition_policy. This context's MCP charter is
// entirely read-only, so the agent is read-only too and never widens what the
// context exposes. The card declares no security schemes on purpose: this
// surface is unauthenticated by the same fleet-wide decision as REST and MCP;
// schemes are added here, not in a redesign, if that decision is ever
// reversed.
func Card(cfg Config) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "returns-processing",
		Description: "Returns Processing agent for the Warehouse Execution System (reverse logistics): the read-only source of truth for return authorizations (RMAs) and the disposition policy. It answers where a return stands (status REQUESTED, AUTHORIZED, REJECTED, RECEIVED or CLOSED, with the rejection code of a refused one) by RMA id or for every RMA of an order, and which disposition policy applies (return window, per-line quantity cap, and the grade x product-class outcome table). It never authorizes, rejects, receives or inspects a return and never changes the policy: those stay on the REST API.",
		Version:     cfg.Version,
		Provider: &a2a.AgentProvider{
			Org: "IQVO",
			URL: "https://github.com/IQVO/returns-processing",
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cfg.AgentURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{
			{
				ID:          skillReturnStatus,
				Name:        "Where does a return stand",
				Description: "Read the state of returns, addressed by EXACTLY ONE of rmaId (one return authorization) or orderId (every RMA of one original order, ordered by line number then id; an order with no returns, or an unknown one, answers an empty list). Each return carries its orderId and lineNo, the work unit it shipped in, sku, quantity, site, reasonCode, status, the rejectionCode of a refused return, the instant the return window ends and the aggregate version. Answers a 404 problem for an unknown rmaId. Persists nothing.",
				Tags:        []string{"returns", "rma", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"return_status","arguments":{"rmaId":"rma-1b4e28ba"}}`,
					`{"skill":"return_status","arguments":{"orderId":"ord-7c9e"}}`,
				},
			},
			{
				ID:          skillDispositionPolicy,
				Name:        "Read the disposition policy",
				Description: "Read the disposition policy table: version, returnWindowDays, maxUnitsPerLine, the vetoOutcome a guardrail demotes to, declaredAt and the rules mapping each inspection grade (A, B, C, SCRAP) and product class to an outcome (RESTOCK, REFURBISH, LIQUIDATE, DISPOSE, RTV; \"*\" is a rule's default product class). Omitting version reads the CURRENT policy; a positive version reads that declared one (the policy is append-only). Answers a 404 problem when no such version exists. Persists nothing.",
				Tags:        []string{"returns", "disposition", "policy", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"disposition_policy","arguments":{}}`,
					`{"skill":"disposition_policy","arguments":{"version":1}}`,
				},
			},
		},
	}
}

// NewExecutor returns the AgentExecutor that maps an incoming message to a
// skill and answers with a single agent Message (a message-only agent in A2A
// terms): both curated skills are transactional request/response reads, so
// there is no long-running Task lifecycle to track in this first A2A surface.
// The response carries the caller's contextId (and taskId when the client
// referenced one) so a delegating saga can group its interactions.
func NewExecutor(deps Deps) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(answer(ctx, deps, ec), nil)
		}
	})
}

// Handler wires the executor into the transport-agnostic A2A request handler
// and wraps it in the JSON-RPC 2.0 HTTP handler, the only transport this
// agent ships (ADR 0008), mirroring the MCP server's "Streamable HTTP
// only" minimalism.
func Handler(executor a2asrv.AgentExecutor) http.Handler {
	return a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
}
