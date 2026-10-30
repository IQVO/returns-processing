package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"github.com/claudioed/returns-processing/internal/adapters/outbound/memory"
	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

var now = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

// restProblemBase is the REST adapter's RFC 7807 namespace (problemBaseURI in
// internal/adapters/inbound/http/errors.go), spelled out here so a drift of
// the A2A namespace fails this suite.
const restProblemBase = "https://errors.returns-processing.warehouse-systems.dev/"

// fixture holds the in-memory repositories behind the agent and the WRITE use
// cases that seed them the way the REST adapter would. The agent itself has
// no write use case: only this test file does.
type fixture struct {
	rmas     *memory.RMAs
	policies *memory.Policies
	events   *memory.Events
	allocate func(orderID string, lineNo int32)
	authUC   *usecases.AuthorizeReturn
	declare  *usecases.DeclareDispositionPolicy
}

func newFixture() *fixture {
	f := &fixture{rmas: memory.NewRMAs(), policies: memory.NewPolicies(), events: &memory.Events{}}
	allocated, manifested := memory.NewAllocatedLines(), memory.NewManifestedUnits()
	clock := memory.Clock{T: now}
	f.authUC = &usecases.AuthorizeReturn{
		RMAs: f.rmas, Policies: f.policies, Allocated: allocated, Manifested: manifested,
		Events: f.events, IDs: memory.NewIDGen(), Clock: clock,
	}
	f.declare = &usecases.DeclareDispositionPolicy{Policies: f.policies, Clock: clock}
	f.allocate = func(orderID string, lineNo int32) {
		allocated.Put(orderID, lineNo, views.AllocatedLine{SKU: "sku-1"})
		manifested.PutByOrderLine(orderID, lineNo, views.ManifestedUnit{PackageID: "pkg-1", ManifestedAt: now})
	}
	return f
}

func (f *fixture) deps() Deps {
	return Deps{
		GetReturn:   &usecases.GetReturn{RMAs: f.rmas},
		ListByOrder: &usecases.ListReturnsByOrder{RMAs: f.rmas},
		GetPolicy:   &usecases.GetDispositionPolicy{Policies: f.policies},
	}
}

func (f *fixture) declarePolicy(t *testing.T) {
	t.Helper()
	_, err := f.declare.Execute(context.Background(), usecases.PolicyDeclaration{
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

// authorize seeds one AUTHORIZED RMA of a shipped order line and returns its id.
func (f *fixture) authorize(t *testing.T, orderID string, lineNo int32, qty int64) string {
	t.Helper()
	f.allocate(orderID, lineNo)
	rma, err := f.authUC.Execute(context.Background(), usecases.ReturnRequest{
		OrderID: orderID, LineNo: lineNo, Quantity: qty, SiteID: "SITE1", ReasonCode: "DAMAGED",
	})
	if err != nil {
		t.Fatalf("authorize %s/%d: %v", orderID, lineNo, err)
	}
	return rma.ID()
}

// ---- the wire: JSON-RPC over HTTP, driven by the SDK's own client ----------

// wireEnvelope is a TEST-LOCAL copy of the answer contract. If the adapter's
// envelope drifts, this suite fails instead of silently following it.
type wireEnvelope struct {
	Skill string `json:"skill"`
	Data  *struct {
		Return  *wireReturn `json:"return"`
		Returns *struct {
			OrderID string       `json:"orderId"`
			Items   []wireReturn `json:"items"`
		} `json:"returns"`
		Policy *struct {
			Version          int64  `json:"version"`
			ReturnWindowDays int32  `json:"returnWindowDays"`
			MaxUnitsPerLine  int64  `json:"maxUnitsPerLine"`
			VetoOutcome      string `json:"vetoOutcome"`
			DeclaredAt       string `json:"declaredAt"`
			Rules            []struct {
				Grade        string `json:"grade"`
				ProductClass string `json:"productClass"`
				Outcome      string `json:"outcome"`
			} `json:"rules"`
		} `json:"policy"`
	} `json:"data"`
	Problem *wireProblem `json:"problem"`
}

type wireProblem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Status int    `json:"status"`
}

type wireReturn struct {
	RMAID         string `json:"rmaId"`
	OrderID       string `json:"orderId"`
	LineNo        int32  `json:"lineNo"`
	WorkUnit      string `json:"workUnit"`
	SKU           string `json:"sku"`
	Quantity      int64  `json:"quantity"`
	SiteID        string `json:"siteId"`
	ReasonCode    string `json:"reasonCode"`
	RejectionCode string `json:"rejectionCode"`
	Status        string `json:"status"`
	WindowEndsAt  string `json:"windowEndsAt"`
	Version       int64  `json:"version"`
}

type wire struct {
	client *a2aclient.Client
}

func newWire(t *testing.T, deps Deps) *wire {
	t.Helper()
	srv := httptest.NewServer(Handler(NewExecutor(deps)))
	t.Cleanup(srv.Close)
	card := Card(Config{AgentURL: srv.URL + "/", Version: "test"})
	client, err := a2aclient.NewFromCard(context.Background(), card)
	if err != nil {
		t.Fatalf("NewFromCard: %v", err)
	}
	return &wire{client: client}
}

// call sends one message part and returns the decoded envelope and the
// one-line text summary of the agent's reply.
func (w *wire) call(t *testing.T, part *a2a.Part) (wireEnvelope, string) {
	t.Helper()
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, part)})
	if err != nil {
		t.Fatalf("SendMessage: %v (a domain or request problem must be answered in-band, never raised)", err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || len(msg.Parts) < 2 {
		t.Fatalf("reply = %T, want a message with a data and a text part", resp)
	}
	data, ok := msg.Parts[0].Content.(a2a.Data)
	if !ok {
		t.Fatalf("first reply part is %T, want data", msg.Parts[0].Content)
	}
	raw, _ := json.Marshal(data.Value)
	var env wireEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("reply is not the skill envelope: %v (%s)", err, raw)
	}
	text, _ := msg.Parts[1].Content.(a2a.Text)
	return env, string(text)
}

func (w *wire) skill(t *testing.T, skill string, args map[string]any) (wireEnvelope, string) {
	t.Helper()
	return w.call(t, a2a.NewDataPart(map[string]any{"skill": skill, "arguments": args}))
}

func wantProblem(t *testing.T, env wireEnvelope, status int, slug string) {
	t.Helper()
	if env.Problem == nil {
		t.Fatalf("expected a %d %s problem, got data: %+v", status, slug, env.Data)
	}
	if env.Problem.Status != status || env.Problem.Type != restProblemBase+slug {
		t.Fatalf("problem = %+v, want %d %s%s", *env.Problem, status, restProblemBase, slug)
	}
	if env.Data != nil {
		t.Fatalf("a problem answer must carry no data: %+v", env.Data)
	}
}

// ---- the skills ------------------------------------------------------------

func TestReturnStatusByRMAID(t *testing.T) {
	f := newFixture()
	f.declarePolicy(t)
	id := f.authorize(t, "ord-7c9e", 3, 2)
	w := newWire(t, f.deps())

	env, text := w.skill(t, skillReturnStatus, map[string]any{"rmaId": id})
	if env.Problem != nil || env.Skill != skillReturnStatus || env.Data == nil || env.Data.Return == nil {
		t.Fatalf("envelope = %+v", env)
	}
	got := *env.Data.Return
	want := wireReturn{
		RMAID: id, OrderID: "ord-7c9e", LineNo: 3, WorkUnit: "ord-7c9e-line-3", SKU: "sku-1", Quantity: 2,
		SiteID: "SITE1", ReasonCode: "DAMAGED", Status: "AUTHORIZED", WindowEndsAt: "2026-11-20T09:00:00Z", Version: 1,
	}
	if got != want {
		t.Fatalf("return = %+v\nwant     %+v", got, want)
	}
	if !strings.Contains(text, id) || !strings.Contains(text, "AUTHORIZED") {
		t.Fatalf("summary = %q, want the RMA id and its status", text)
	}
}

func TestReturnStatusByOrderIsOrderedAndEmptyForAnUnknownOrder(t *testing.T) {
	f := newFixture()
	f.declarePolicy(t)
	f.authorize(t, "ord-1", 5, 1)
	f.authorize(t, "ord-1", 2, 1)
	f.authorize(t, "ord-2", 1, 1)
	w := newWire(t, f.deps())

	assertOrderListOrdered(t, w)
	assertUnknownOrderIsEmptyList(t, w)
}

func assertOrderListOrdered(t *testing.T, w *wire) {
	t.Helper()
	env, text := w.skill(t, skillReturnStatus, map[string]any{"orderId": "ord-1"})
	if env.Problem != nil || env.Data == nil || env.Data.Returns == nil {
		t.Fatalf("envelope = %+v", env)
	}
	items := env.Data.Returns.Items
	if env.Data.Returns.OrderID != "ord-1" || len(items) != 2 || items[0].LineNo != 2 || items[1].LineNo != 5 {
		t.Fatalf("returns = %+v, want ord-1 lines 2 then 5 only", env.Data.Returns)
	}
	if items[0].WorkUnit != "ord-1-line-2" || items[0].Status != "AUTHORIZED" {
		t.Fatalf("item shape = %+v", items[0])
	}
	if !strings.Contains(text, "2 returns") {
		t.Fatalf("summary = %q", text)
	}
}

// The REST rule: an order with no returns, or an unknown one, is an EMPTY
// list, never a 404.
func assertUnknownOrderIsEmptyList(t *testing.T, w *wire) {
	t.Helper()
	empty, text := w.skill(t, skillReturnStatus, map[string]any{"orderId": "ord-9"})
	if empty.Problem != nil || empty.Data == nil || empty.Data.Returns == nil || empty.Data.Returns.Items == nil || len(empty.Data.Returns.Items) != 0 {
		t.Fatalf("unknown order = %+v, want an empty (non-null) list", empty)
	}
	if !strings.Contains(text, "no returns") {
		t.Fatalf("summary = %q", text)
	}
}

func TestDispositionPolicyCurrentAndByVersion(t *testing.T) {
	f := newFixture()
	f.declarePolicy(t)
	w := newWire(t, f.deps())

	for name, args := range map[string]map[string]any{"current": {}, "version 1": {"version": 1}} {
		env, text := w.skill(t, skillDispositionPolicy, args)
		if env.Problem != nil || env.Data == nil || env.Data.Policy == nil {
			t.Fatalf("%s: envelope = %+v", name, env)
		}
		p := env.Data.Policy
		if p.Version != 1 || p.ReturnWindowDays != 30 || p.MaxUnitsPerLine != 5 || p.VetoOutcome != "DISPOSE" || p.DeclaredAt != "2026-10-21T09:00:00Z" {
			t.Fatalf("%s: policy = %+v", name, p)
		}
		if len(p.Rules) != 4 || p.Rules[0].Grade != "A" || p.Rules[0].ProductClass != "*" || p.Rules[0].Outcome != "RESTOCK" {
			t.Fatalf("%s: rules = %+v", name, p.Rules)
		}
		if !strings.Contains(text, "v1") {
			t.Fatalf("%s: summary = %q", name, text)
		}
	}
}

// A skill request may also arrive as a Text part holding the same JSON.
func TestSkillRequestAsTextPart(t *testing.T) {
	f := newFixture()
	f.declarePolicy(t)
	w := newWire(t, f.deps())

	env, _ := w.call(t, a2a.NewTextPart(`{"skill":"disposition_policy","arguments":{}}`))
	if env.Problem != nil || env.Data == nil || env.Data.Policy == nil {
		t.Fatalf("envelope = %+v", env)
	}
}

// Reads never enqueue an outbox event: this agent has no write path at all
// (the only Events here belong to the seeding use cases).
func TestSkillsWriteNothing(t *testing.T) {
	f := newFixture()
	f.declarePolicy(t)
	id := f.authorize(t, "ord-1", 1, 1)
	before := len(f.events.RMAEventNames())
	w := newWire(t, f.deps())

	w.skill(t, skillReturnStatus, map[string]any{"rmaId": id})
	w.skill(t, skillReturnStatus, map[string]any{"orderId": "ord-1"})
	w.skill(t, skillDispositionPolicy, map[string]any{})

	if after := len(f.events.RMAEventNames()); after != before {
		t.Fatalf("skills added %d outbox events", after-before)
	}
}

// ---- argument validation and in-band problems -----------------------------

func TestArgumentValidation(t *testing.T) {
	f := newFixture()
	f.declarePolicy(t)
	w := newWire(t, f.deps())
	long := strings.Repeat("x", maxIDLen+1)

	cases := []struct {
		name  string
		skill string
		args  map[string]any
		want  string // a fragment of the problem detail
	}{
		{"neither id", skillReturnStatus, map[string]any{}, "exactly one of rmaId or orderId"},
		{"both ids", skillReturnStatus, map[string]any{"rmaId": "rma-1", "orderId": "ord-1"}, "not both"},
		{"blank rmaId", skillReturnStatus, map[string]any{"rmaId": ""}, "rmaId must be"},
		{"rmaId with a space", skillReturnStatus, map[string]any{"rmaId": "rma 1"}, "rmaId must be"},
		{"rmaId with a tab", skillReturnStatus, map[string]any{"rmaId": "rma\t1"}, "rmaId must be"},
		{"rmaId with a slash", skillReturnStatus, map[string]any{"rmaId": "rma/1"}, "rmaId must be"},
		{"rmaId with a NUL", skillReturnStatus, map[string]any{"rmaId": "rma\x001"}, "rmaId must be"},
		{"rmaId too long", skillReturnStatus, map[string]any{"rmaId": long}, "rmaId must be"},
		{"blank orderId", skillReturnStatus, map[string]any{"orderId": ""}, "orderId must be"},
		{"orderId with a slash", skillReturnStatus, map[string]any{"orderId": "a/b"}, "orderId must be"},
		{"orderId too long", skillReturnStatus, map[string]any{"orderId": long}, "orderId must be"},
		{"rmaId of the wrong type", skillReturnStatus, map[string]any{"rmaId": 7}, "arguments:"},
		{"null rmaId counts as absent", skillReturnStatus, map[string]any{"rmaId": nil}, "exactly one of rmaId or orderId"},
		{"unknown argument", skillReturnStatus, map[string]any{"rmaId": "rma-1", "extra": true}, "unknown field"},
		{"misspelled argument", skillReturnStatus, map[string]any{"orderIds": "ord-1"}, "unknown field"},
		{"the MCP snake_case key is not offered", skillReturnStatus, map[string]any{"rma_id": "rma-1"}, "unknown field"},
		{"version zero", skillDispositionPolicy, map[string]any{"version": 0}, "at least 1"},
		{"negative version", skillDispositionPolicy, map[string]any{"version": -3}, "at least 1"},
		{"fractional version", skillDispositionPolicy, map[string]any{"version": 1.5}, "arguments:"},
		{"version as a string", skillDispositionPolicy, map[string]any{"version": "1"}, "arguments:"},
		{"misspelled version", skillDispositionPolicy, map[string]any{"versio": 1}, "unknown field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, text := w.skill(t, tc.skill, tc.args)
			wantProblem(t, env, 400, "invalid-argument")
			if !strings.Contains(env.Problem.Detail, tc.want) {
				t.Fatalf("detail = %q, want it to contain %q", env.Problem.Detail, tc.want)
			}
			if !strings.Contains(text, "(400)") {
				t.Fatalf("summary = %q, want the problem summary", text)
			}
		})
	}
}

// Request-shape failures are answered in-band as 400 malformed-skill-request
// or unknown-skill problems, never as JSON-RPC errors.
func TestRequestShapeProblems(t *testing.T) {
	w := newWire(t, newFixture().deps())

	cases := []struct {
		name string
		part *a2a.Part
		slug string
	}{
		{"unknown skill", a2a.NewDataPart(map[string]any{"skill": "authorize_return", "arguments": map[string]any{}}), "unknown-skill"},
		{"no skill field", a2a.NewDataPart(map[string]any{"arguments": map[string]any{}}), "malformed-skill-request"},
		{"unknown envelope field", a2a.NewDataPart(map[string]any{"skill": skillReturnStatus, "args": map[string]any{}}), "malformed-skill-request"},
		{"text that is not JSON", a2a.NewTextPart("where is my return?"), "malformed-skill-request"},
		{"trailing data after the JSON", a2a.NewTextPart(`{"skill":"disposition_policy","arguments":{}} {"skill":"x"}`), "malformed-skill-request"},
		{"a part that is not data or text", a2a.NewRawPart([]byte("x")), "malformed-skill-request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := w.call(t, tc.part)
			wantProblem(t, env, 400, tc.slug)
		})
	}
}

// Trailing data INSIDE the arguments document is refused too.
func TestStrictDecodeRejectsTrailingData(t *testing.T) {
	var args returnStatusArgs
	if err := decodeStrict([]byte(`{"rmaId":"a"} {"rmaId":"b"}`), &args); err == nil {
		t.Fatal("trailing data after the document must be refused")
	}
	if err := decodeStrict([]byte(`{"rmaId":"a","bogus":1}`), &args); err == nil {
		t.Fatal("an unknown field must be refused")
	}
	var absent returnStatusArgs
	if err := decodeStrict(nil, &absent); err != nil || absent.RMAID != nil || absent.OrderID != nil {
		t.Fatalf("absent arguments decode as the empty object, got %v / %+v", err, absent)
	}
}

// Domain misses carry the REST adapter's status, slug, title and detail.
func TestNotFoundProblemsMatchREST(t *testing.T) {
	w := newWire(t, newFixture().deps()) // no RMA, no policy

	env, text := w.skill(t, skillReturnStatus, map[string]any{"rmaId": "rma-unknown"})
	wantProblem(t, env, 404, "rma-not-found")
	if env.Problem.Title != "Return authorization not found" || env.Problem.Detail != "return authorization not found" {
		t.Fatalf("rma problem = %+v", *env.Problem)
	}
	if !strings.Contains(text, "(404)") {
		t.Fatalf("summary = %q", text)
	}

	env, _ = w.skill(t, skillDispositionPolicy, map[string]any{})
	wantProblem(t, env, 404, "policy-not-found")
	if env.Problem.Title != "No disposition policy declared" || env.Problem.Detail != "no disposition policy version exists yet" {
		t.Fatalf("policy problem = %+v", *env.Problem)
	}

	env, _ = w.skill(t, skillDispositionPolicy, map[string]any{"version": 9})
	wantProblem(t, env, 404, "policy-not-found")
	if env.Problem.Detail != "no disposition policy version 9 exists" {
		t.Fatalf("version problem = %+v", *env.Problem)
	}
}

// failingRMAs and failingPolicies make every read fail with an infrastructure
// error that must never reach the caller.
type failingRMAs struct{ ports.ReturnAuthorizationRepo }

var errBoom = errors.New("pq: password authentication failed for user returns@10.1.2.3")

func (failingRMAs) FindByID(context.Context, string) (*returnauthorization.RMA, error) {
	return nil, errBoom
}
func (failingRMAs) ListByOrder(context.Context, string) ([]*returnauthorization.RMA, error) {
	return nil, errBoom
}

type failingPolicies struct{ ports.DispositionPolicyRepo }

func (failingPolicies) Current(context.Context) (*disposition.DispositionPolicy, error) {
	return nil, errBoom
}

func (failingPolicies) FindByVersion(context.Context, int64) (*disposition.DispositionPolicy, error) {
	return nil, errBoom
}

// An unrecognised failure is a 500 internal-error problem, answered in-band,
// with adapter-owned detail text: the infrastructure message stays in the log.
func TestUnexpectedFailureIsAnInBand500WithoutLeakingTheCause(t *testing.T) {
	deps := Deps{
		GetReturn:   &usecases.GetReturn{RMAs: failingRMAs{}},
		ListByOrder: &usecases.ListReturnsByOrder{RMAs: failingRMAs{}},
		GetPolicy:   &usecases.GetDispositionPolicy{Policies: failingPolicies{}},
	}
	w := newWire(t, deps)
	for name, req := range map[string]struct {
		skill string
		args  map[string]any
	}{
		"get return":     {skillReturnStatus, map[string]any{"rmaId": "rma-1"}},
		"list by order":  {skillReturnStatus, map[string]any{"orderId": "ord-1"}},
		"current policy": {skillDispositionPolicy, map[string]any{}},
		"policy version": {skillDispositionPolicy, map[string]any{"version": 2}},
	} {
		env, text := w.skill(t, req.skill, req.args)
		wantProblem(t, env, 500, "internal-error")
		for _, part := range []string{env.Problem.Detail, env.Problem.Title, text} {
			if strings.Contains(part, "password") || strings.Contains(part, "10.1.2.3") {
				t.Fatalf("%s: the infrastructure error leaked into the answer: %q", name, part)
			}
		}
	}
}

// The answer is bound to the caller's conversation (contextId), so a
// delegating saga can group its interactions.
func TestReplyCarriesTheCallersContext(t *testing.T) {
	f := newFixture()
	f.declarePolicy(t)
	w := newWire(t, f.deps())

	req := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewDataPart(map[string]any{"skill": skillDispositionPolicy, "arguments": map[string]any{}}))
	req.ContextID = "saga-42"
	resp, err := w.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: req})
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := resp.(*a2a.Message)
	if !ok || msg.ContextID != "saga-42" || msg.Role != a2a.MessageRoleAgent {
		t.Fatalf("reply = %#v, want an agent message bound to context saga-42", resp)
	}
}
