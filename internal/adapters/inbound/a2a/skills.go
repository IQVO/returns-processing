package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/claudioed/returns-processing/internal/application/ports"
)

// tracerName is the OTel instrumentation scope for A2A skill spans, mirroring
// the MCP adapter's mcp.tool spans.
const tracerName = "github.com/claudioed/returns-processing/internal/adapters/inbound/a2a"

// Skill ids advertised on the Agent Card (see Card).
const (
	skillReturnStatus      = "return_status"
	skillDispositionPolicy = "disposition_policy"
)

// maxIDLen mirrors the REST adapter's id shape (internal/adapters/inbound/http
// /params.go): ids are 1..64 characters.
const maxIDLen = 64

// skillRequest is the wire contract of every skill call: the first message
// part carries (as a Data part, or a Text part holding the same JSON) the
// skill id and its arguments. Strict on purpose: an agent client is
// machine-to-machine, so unknown fields are rejected, never ignored, and a
// misspelled name like "versio" must not silently fall back to the CURRENT
// policy. (Go's encoding/json matches field names case-insensitively, so
// "RMAID" still reaches rmaId; only genuinely unknown names are refused.)
type skillRequest struct {
	Skill     string          `json:"skill"`
	Arguments json.RawMessage `json:"arguments"`
}

// returnStatusArgs: EXACTLY ONE of the two is set. Pointers tell an absent
// argument from a blank one.
type returnStatusArgs struct {
	RMAID   *string `json:"rmaId"`
	OrderID *string `json:"orderId"`
}

// dispositionPolicyArgs: an absent version reads the CURRENT policy.
type dispositionPolicyArgs struct {
	Version *int64 `json:"version"`
}

// answer maps one incoming message to a skill and returns the agent's reply
// Message. It is the whole executor body: this is a message-only agent, so a
// skill run is exactly one request mapped to one answer (ADR 0008).
func answer(ctx context.Context, deps Deps, ec *a2asrv.ExecutorContext) a2a.Event {
	req, problem := parseSkillRequest(ec)
	if problem != nil {
		return reply(ec, envelope{Skill: "", Problem: problem}, problem.summary())
	}

	ctx, span := otel.Tracer(tracerName).Start(ctx, "a2a.skill "+req.Skill)
	defer span.End()
	span.SetAttributes(attribute.String("a2a.skill", req.Skill))

	dto, problem := run(ctx, deps, req)
	if problem != nil {
		span.SetStatus(codes.Error, problem.Detail)
		return reply(ec, envelope{Skill: req.Skill, Problem: problem}, problem.summary())
	}
	return reply(ec, envelope{Skill: req.Skill, Data: &dto}, dto.Summary())
}

// run dispatches one validated request to its skill.
func run(ctx context.Context, deps Deps, req skillRequest) (skillDTO, *problem) {
	switch req.Skill {
	case skillReturnStatus:
		return deps.returnStatus(ctx, req.Arguments)
	case skillDispositionPolicy:
		return deps.dispositionPolicy(ctx, req.Arguments)
	default:
		return skillDTO{}, problemUnknownSkill(req.Skill)
	}
}

// reply builds the agent Message for one skill run: a Data part carrying the
// structured envelope and a Text part carrying the one-line summary, bound to
// the caller's context (and task, when the client referenced one).
func reply(ec *a2asrv.ExecutorContext, env envelope, summary string) a2a.Event {
	return a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewDataPart(env), a2a.NewTextPart(summary))
}

// parseSkillRequest extracts the skillRequest from the message's first part.
// A Data part (or a Text part holding the same JSON object) is accepted;
// anything else is a request-shape problem, answered, not raised, because a
// malformed delegation is a business answer for the calling agent, not a
// transport failure.
func parseSkillRequest(ec *a2asrv.ExecutorContext) (skillRequest, *problem) {
	msg := ec.Message
	if msg == nil || len(msg.Parts) == 0 {
		return skillRequest{}, problemMalformedRequest("the message carries no parts")
	}
	raw, err := partJSON(msg.Parts[0])
	if err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the first message part is not a skill request: %v", err))
	}
	var req skillRequest
	if err := decodeStrict(raw, &req); err != nil {
		return skillRequest{}, problemMalformedRequest(fmt.Sprintf("the skill request is not valid: %v", err))
	}
	if req.Skill == "" {
		return skillRequest{}, problemMalformedRequest(`the skill request carries no "skill" field`)
	}
	return req, nil
}

// partJSON renders one part's content as the JSON bytes of a skill request.
func partJSON(part *a2a.Part) ([]byte, error) {
	switch content := part.Content.(type) {
	case a2a.Data:
		return json.Marshal(content.Value)
	case a2a.Text:
		return []byte(string(content)), nil
	default:
		return nil, errors.New("expected a data part (or a text part holding the skill JSON)")
	}
}

// decodeStrict decodes one JSON document into dst, rejecting unknown fields
// and trailing data. Absent arguments decode as the empty object so each
// skill's own required-argument checks produce the specific problem.
func decodeStrict(raw []byte, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// checkID applies the REST adapter's id-parameter shape (idParam): 1..64
// characters without a space, a tab, a slash or a NUL byte. The resource
// prefix is NOT enforced: an unknown id of the right shape is the use case's
// 404, not the adapter's 400.
func checkID(name, id string) *problem {
	if id == "" || len(id) > maxIDLen || strings.ContainsAny(id, " \t/\x00") {
		return problemInvalidArgument(name + " must be 1..64 characters without white space, a slash or a NUL byte")
	}
	return nil
}

// returnStatus is the return-state read skill, through the same GetReturn
// (rmaId) or ListReturnsByOrder (orderId) use case the REST and MCP adapters
// use. Exactly one of the two arguments is required.
func (d Deps) returnStatus(ctx context.Context, rawArgs json.RawMessage) (skillDTO, *problem) {
	var args returnStatusArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return skillDTO{}, problemInvalidArgument(fmt.Sprintf("arguments: %v", err))
	}
	switch {
	case args.RMAID != nil && args.OrderID != nil:
		return skillDTO{}, problemInvalidArgument("pass exactly one of rmaId or orderId, not both")
	case args.RMAID != nil:
		return d.returnByID(ctx, *args.RMAID)
	case args.OrderID != nil:
		return d.returnsByOrder(ctx, *args.OrderID)
	default:
		return skillDTO{}, problemInvalidArgument("pass exactly one of rmaId or orderId")
	}
}

func (d Deps) returnByID(ctx context.Context, rmaID string) (skillDTO, *problem) {
	if p := checkID("rmaId", rmaID); p != nil {
		return skillDTO{}, p
	}
	v, err := d.GetReturn.Execute(ctx, rmaID)
	if err != nil {
		return skillDTO{}, problemFromUseCase(err, problemRMANotFound())
	}
	dto := toReturnDTO(v)
	return skillDTO{Return: &dto, summary: summarizeReturn(dto)}, nil
}

func (d Deps) returnsByOrder(ctx context.Context, orderID string) (skillDTO, *problem) {
	if p := checkID("orderId", orderID); p != nil {
		return skillDTO{}, p
	}
	list, err := d.ListByOrder.Execute(ctx, orderID)
	if err != nil {
		return skillDTO{}, problemFromUseCase(err, nil)
	}
	out := returnsDTO{OrderID: orderID, Items: make([]returnDTO, 0, len(list))}
	for _, v := range list {
		out.Items = append(out.Items, toReturnDTO(v))
	}
	return skillDTO{Returns: &out, summary: summarizeReturns(out)}, nil
}

// dispositionPolicy is the policy-table read skill, through the same
// GetDispositionPolicy use case the REST (current) and MCP (current or by
// version) adapters use. An absent version reads the CURRENT policy; a given
// one must be a positive integer (REST's versionParam rule).
func (d Deps) dispositionPolicy(ctx context.Context, rawArgs json.RawMessage) (skillDTO, *problem) {
	var args dispositionPolicyArgs
	if err := decodeStrict(rawArgs, &args); err != nil {
		return skillDTO{}, problemInvalidArgument(fmt.Sprintf("arguments: %v", err))
	}
	if args.Version != nil && *args.Version < 1 {
		return skillDTO{}, problemInvalidArgument("version must be an integer of at least 1 (omit it to read the current policy)")
	}
	var (
		v        = d.currentPolicy
		notFound = "no disposition policy version exists yet"
	)
	if args.Version != nil {
		version := *args.Version
		v = func(ctx context.Context) (policyDTO, error) { return d.policyVersion(ctx, version) }
		notFound = "no disposition policy version " + strconv.FormatInt(version, 10) + " exists"
	}
	dto, err := v(ctx)
	if err != nil {
		return skillDTO{}, problemFromUseCase(err, problemPolicyNotFound(notFound))
	}
	return skillDTO{Policy: &dto, summary: summarizePolicy(dto)}, nil
}

func (d Deps) currentPolicy(ctx context.Context) (policyDTO, error) {
	p, err := d.GetPolicy.Execute(ctx)
	if err != nil {
		return policyDTO{}, err
	}
	return toPolicyDTO(p), nil
}

func (d Deps) policyVersion(ctx context.Context, version int64) (policyDTO, error) {
	p, err := d.GetPolicy.Version(ctx, version)
	if err != nil {
		return policyDTO{}, err
	}
	return toPolicyDTO(p), nil
}

// problemFromUseCase maps a use case error to a problem answer, using the
// SAME classification as the REST adapter (internal/adapters/inbound/http
// problemFor / writeIfNotFound) for the errors a read can raise: a miss of
// the shared ports.ErrNotFound sentinel is the 404 of the ADDRESSED resource
// (notFound; the caller names which one), and anything unrecognised is 500
// internal-error. (REST's 400 shape rules are the adapter's own checks above;
// its 409 class belongs to writes this read-only agent does not offer, and its
// 503 is /readyz's dependency check, not a request outcome.) The detail is
// adapter-owned text, so a shared error's "usecases:" prefix or a SQL message
// never reaches an agent client; the real error is logged.
func problemFromUseCase(err error, notFound *problem) *problem {
	if notFound != nil && errors.Is(err, ports.ErrNotFound) {
		return notFound
	}
	slog.Error("a2a skill failed", "error", err)
	return problemInternal()
}
