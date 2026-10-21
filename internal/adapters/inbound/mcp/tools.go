package mcp

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
)

// Deps is everything the MCP tools need, injected by the composition root.
// It carries the SAME read use cases the REST adapter uses (GET /returns/{rmaId},
// GET /returns?orderRef=, GET /disposition-policies/current); the adapter
// never constructs an outbound adapter itself, and it holds no write use case
// at all (governance_test.go pins the tool set read-only).
type Deps struct {
	GetReturn   *usecases.GetReturn
	ListByOrder *usecases.ListReturnsByOrder
	GetPolicy   *usecases.GetDispositionPolicy
}

// --- inputs -------------------------------------------------------------------

type getReturnInput struct {
	RMAID string `json:"rma_id" jsonschema:"the return authorization id, e.g. rma-1b4e28ba"`
}

type listReturnsByOrderInput struct {
	OrderID string `json:"order_id" jsonschema:"the original order id the returns were authorized against, e.g. ord-7c9e"`
}

type getDispositionPolicyInput struct {
	// Version is OPTIONAL: omitted or 0 means the CURRENT version; a positive
	// number reads that declared version.
	Version int64 `json:"version,omitempty" jsonschema:"optional declared version to read; omitted means the CURRENT policy version"`
}

// --- outputs (snake_case renderings of the application's read views) ----------

type returnView struct {
	RMAID         string `json:"rma_id"`
	OrderID       string `json:"order_id"`
	LineNo        int32  `json:"line_no"`
	WorkUnit      string `json:"work_unit,omitempty"`
	SKU           string `json:"sku"`
	Quantity      int64  `json:"quantity"`
	SiteID        string `json:"site_id"`
	ReasonCode    string `json:"reason_code,omitempty"`
	RejectionCode string `json:"rejection_code,omitempty"`
	Status        string `json:"status"`
	WindowEndsAt  string `json:"window_ends_at,omitempty"`
	Version       int64  `json:"version"`
}

type returnsByOrderView struct {
	Items []returnView `json:"items"`
}

type policyRuleView struct {
	Grade        string `json:"grade"`
	ProductClass string `json:"product_class"`
	Outcome      string `json:"outcome"`
}

type policyView struct {
	Version          int64            `json:"version"`
	ReturnWindowDays int32            `json:"return_window_days"`
	MaxUnitsPerLine  int64            `json:"max_units_per_line"`
	VetoOutcome      string           `json:"veto_outcome"`
	Rules            []policyRuleView `json:"rules"`
	DeclaredAt       string           `json:"declared_at"`
}

// --- mapping ------------------------------------------------------------------

// instant formats an instant as RFC 3339 UTC; the zero time is "" (omitted).
func instant(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func toReturnView(v views.ReturnView) returnView {
	return returnView{
		RMAID:         v.ID,
		OrderID:       v.OrderID,
		LineNo:        v.LineNo,
		WorkUnit:      v.WorkUnit,
		SKU:           v.SKU,
		Quantity:      v.Quantity,
		SiteID:        v.SiteID,
		ReasonCode:    v.ReasonCode,
		RejectionCode: v.RejectionCode,
		Status:        v.Status,
		WindowEndsAt:  instant(v.WindowEndsAt),
		Version:       v.Version,
	}
}

func toPolicyView(p views.PolicyView) policyView {
	out := policyView{
		Version:          p.Version,
		ReturnWindowDays: p.ReturnWindowDays,
		MaxUnitsPerLine:  p.MaxUnitsPerLine,
		VetoOutcome:      p.VetoOutcome,
		Rules:            make([]policyRuleView, 0, len(p.Rules)),
		DeclaredAt:       instant(p.DeclaredAt),
	}
	for _, r := range p.Rules {
		out.Rules = append(out.Rules, policyRuleView{
			Grade:        r.Grade.String(),
			ProductClass: r.ProductClass,
			Outcome:      string(r.Outcome),
		})
	}
	return out
}

// --- handlers -----------------------------------------------------------------

func (d Deps) getReturn(ctx context.Context, in getReturnInput) (returnView, error) {
	id := strings.TrimSpace(in.RMAID)
	if id == "" || len(id) > maxIDLen || strings.ContainsAny(id, " \t/\x00") {
		return returnView{}, toolError("invalid-rma-id", "rma_id must be 1..64 characters without white space, a slash or a NUL byte")
	}
	v, err := d.GetReturn.Execute(ctx, id)
	if err != nil {
		return returnView{}, mapError(err, "rma-not-found", "return authorization not found")
	}
	return toReturnView(v), nil
}

func (d Deps) listReturnsByOrder(ctx context.Context, in listReturnsByOrderInput) (returnsByOrderView, error) {
	orderID := strings.TrimSpace(in.OrderID)
	if orderID == "" || len(orderID) > maxIDLen || strings.ContainsAny(orderID, " \t/\x00") {
		return returnsByOrderView{}, toolError("invalid-order-id", "order_id must be 1..64 characters without white space, a slash or a NUL byte")
	}
	list, err := d.ListByOrder.Execute(ctx, orderID)
	if err != nil {
		return returnsByOrderView{}, mapError(err, "internal-error", "")
	}
	out := returnsByOrderView{Items: make([]returnView, 0, len(list))}
	for _, v := range list {
		out.Items = append(out.Items, toReturnView(v))
	}
	return out, nil
}

func (d Deps) getDispositionPolicy(ctx context.Context, in getDispositionPolicyInput) (policyView, error) {
	var (
		p   views.PolicyView
		err error
	)
	if in.Version > 0 {
		p, err = d.GetPolicy.Version(ctx, in.Version)
		if err != nil {
			return policyView{}, mapError(err, "policy-not-found", "no disposition policy version "+strconv.FormatInt(in.Version, 10)+" exists")
		}
	} else {
		p, err = d.GetPolicy.Execute(ctx)
		if err != nil {
			return policyView{}, mapError(err, "policy-not-found", "no disposition policy version exists yet")
		}
	}
	return toPolicyView(p), nil
}

// --- registry -----------------------------------------------------------------

// maxIDLen mirrors the REST adapter's id shape (internal/adapters/inbound/http
// /params.go): ids are 1..64 characters.
const maxIDLen = 64

// registerTools adds the three read-only tools. Every tool is annotated
// read-only, idempotent and closed-world; TestToolSurface pins the set and
// fails the build on any write-verb name.
func (d Deps) registerTools(server *mcp.Server) {
	closedWorld := false
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &closedWorld}

	addTool(server, &mcp.Tool{
		Name: "get_return",
		Description: "Read one return authorization (RMA) by id: the original order id and line number it was " +
			"authorized against, the work unit ({order_id}-line-{line_no}) the goods shipped in, the sku, quantity " +
			"and site, the reason code, the status (REQUESTED, AUTHORIZED, REJECTED, RECEIVED or CLOSED), the " +
			"rejection code on a refused return, the instant the return window ends and the aggregate version. " +
			"Read-only; fails with rma-not-found for an unknown id and invalid-rma-id for a malformed one.",
		Annotations: readOnly,
	}, d.getReturn)

	addTool(server, &mcp.Tool{
		Name: "list_returns_by_order",
		Description: "List every return authorization (RMA) of one original order, ordered by line number then id. " +
			"An order with no returns -- or an unknown order -- answers an empty list, never an error. Each item " +
			"carries the same shape as get_return. Read-only; fails with invalid-order-id for a malformed order id.",
		Annotations: readOnly,
	}, d.listReturnsByOrder)

	addTool(server, &mcp.Tool{
		Name: "get_disposition_policy",
		Description: "Read the disposition policy table: the version, the return window in days, the maximum units " +
			"per return line, the veto outcome a guardrail demotes to (default DISPOSE), the declared instant and " +
			"the rule table mapping each inspection grade (A, B, C, SCRAP) and product class to an outcome (RESTOCK, " +
			"REFURBISH, LIQUIDATE, DISPOSE, RTV); \"*\" is the default product class of a rule. Omitting version " +
			"reads the CURRENT version; passing a version number reads that declared one (the policy is append-only: " +
			"a change is a new version, never a rewrite). Read-only; fails with policy-not-found when the version " +
			"does not exist.",
		Annotations: readOnly,
	}, d.getDispositionPolicy)
}

// addTool registers one tool. A handler error is returned to the SDK as the
// handler's error, which the SDK turns into an isError tool result (never a
// transport/protocol failure), carrying the error text.
func addTool[In, Out any](
	server *mcp.Server,
	tool *mcp.Tool,
	handle func(context.Context, In) (Out, error),
) {
	mcp.AddTool(server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		out, err := handle(ctx, in)
		if err != nil {
			var zero Out
			return nil, zero, err
		}
		return nil, out, nil
	})
}
