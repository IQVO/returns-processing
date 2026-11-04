package mcp

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// triageReturnSOP is the operator standard-operating-procedure the
// triage_return prompt hands to the model (Agentic Standard v2, AS-3). Per
// the charter, prompts encode how to interpret results and what "done"
// means; this one names tools but NEVER calls them, and it cannot write:
// every tool it names is read-only (docs/docs/mcp/tools.md).
const triageReturnSOP = `You are triaging one return authorization (RMA {{rmaId}}) of the warehouse's reverse flow. Use only the MCP tools of returns-processing; never assume state — read it.

Procedure:
1. Call get_return(rma_id) for the RMA {{rmaId}}. Read its status: REQUESTED (asked, not yet decided), AUTHORIZED (goods may come back), REJECTED (refused; the rejection_code says why), RECEIVED (goods are in) or CLOSED (settled). The order_id and line_no name the original order line; window_ends_at is when the return window closes; version is the aggregate version.
2. Call list_returns_by_order(order_id) with that order_id to see every other RMA of the same original order — a customer often returns a whole order line by line.
3. Call get_disposition_policy() (no version = the CURRENT policy) to learn what happens to the goods once received: the return window in days, the per-line quantity cap, and the grade x product-class rule table that decides each outcome (RESTOCK, REFURBISH, LIQUIDATE, DISPOSE, RTV; the veto outcome is what a guardrail demotes to). The policy is append-only: cite the version you read.

Interpretation:
- A REJECTED return needs the rejection_code explained to the requester; it will not progress.
- An AUTHORIZED return whose window_ends_at has passed or is close is urgent: receiving after the window is an override, which is a REST operation, not yours.
- RECEIVED goods follow the policy table of the CURRENT version; a specific version can be cited for history, never to change an outcome.
- Nothing in this server changes data: authorizing or rejecting a return, receiving and inspecting goods, overriding a disposition and declaring the policy are REST operations of returns-processing.

Escalate to a human when: the RMA is AUTHORIZED but the window has ended, when a received line's inspection outcome would need an override, or when the caller asks to change a status or the policy.

Done means: you have reported the RMA's status (with the rejection code when refused), the other returns of the same order, and which disposition policy version applies to the goods, each in one line.`

// registerPrompts adds the workflow prompt (the operator SOP of this
// context). Read-only by construction: a prompt returns text.
func (d Deps) registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "triage_return",
		Description: "Standard operating procedure for triaging one return authorization: read where the RMA stands, the order's other returns, and which disposition policy will apply to the goods.",
		Arguments: []*mcp.PromptArgument{{
			Name:        "rmaId",
			Description: "the return authorization id to triage, e.g. rma-1b4e28ba",
			Required:    true,
		}},
	}, func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		id := strings.TrimSpace(req.Params.Arguments["rmaId"])
		if id == "" {
			id = "<rmaId>"
		}
		return &mcp.GetPromptResult{
			Description: "How to triage a return: read the RMA, the order's other returns and the disposition policy; escalate overrides.",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: strings.ReplaceAll(triageReturnSOP, "{{rmaId}}", id)},
			}},
		}, nil
	})
}
