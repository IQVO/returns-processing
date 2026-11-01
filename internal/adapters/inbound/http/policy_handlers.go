package http

import (
	"net/http"

	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// overrideDispositionRequest is the wire body of
// POST /dispositions/{dispositionId}/overrides (OverrideDispositionRequest).
type overrideDispositionRequest struct {
	Outcome string `json:"outcome"`
	Reason  string `json:"reason"`
}

// handleOverrideDisposition: POST /dispositions/{dispositionId}/overrides ->
// OverrideDisposition (200; the reason is mandatory, 400
// override-reason-required when blank).
func (s *Server) handleOverrideDisposition(w http.ResponseWriter, r *http.Request) {
	dispositionID, err := idParam(r, "dispositionId", errInvalidParam)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in overrideDispositionRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	over, err := s.OverrideDisposition.Execute(r.Context(), usecases.Override{
		DispositionID: dispositionID,
		Outcome:       disposition.Outcome(in.Outcome),
		Reason:        in.Reason,
	})
	if err != nil {
		writeIfNotFound(w, r, err, dispositionNotFound)
		return
	}
	writeJSON(w, http.StatusOK, dispositionDTOFrom(over))
}

// declarePolicyRequest is the wire body of PUT /disposition-policies/{version}
// (DeclareDispositionPolicyRequest).
type declarePolicyRequest struct {
	ReturnWindowDays int32               `json:"returnWindowDays"`
	MaxUnitsPerLine  int64               `json:"maxUnitsPerLine"`
	VetoOutcome      string              `json:"vetoOutcome,omitempty"`
	Rules            []policyRuleRequest `json:"rules"`
}

// policyRuleRequest is one row of the table (PolicyRule).
type policyRuleRequest struct {
	Grade        string `json:"grade"`
	ProductClass string `json:"productClass"`
	Outcome      string `json:"outcome"`
}

// handleDeclarePolicy: PUT /disposition-policies/{version} ->
// DeclareDispositionPolicy (201). vetoOutcome "" defaults to DISPOSE in the
// domain, exactly as the spec documents.
func (s *Server) handleDeclarePolicy(w http.ResponseWriter, r *http.Request) {
	version, err := versionParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in declarePolicyRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	rules := make([]disposition.Rule, 0, len(in.Rules))
	for _, rule := range in.Rules {
		rules = append(rules, disposition.Rule{
			Grade:        shared.Grade(rule.Grade),
			ProductClass: rule.ProductClass,
			Outcome:      disposition.Outcome(rule.Outcome),
		})
	}
	p, err := s.DeclareDispositionPolicy.Execute(r.Context(), usecases.PolicyDeclaration{
		Version:          version,
		ReturnWindowDays: in.ReturnWindowDays,
		MaxUnitsPerLine:  in.MaxUnitsPerLine,
		VetoOutcome:      disposition.Outcome(in.VetoOutcome),
		Rules:            rules,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, policyDTOFrom(usecases.PolicyView(p)))
}

// handleGetCurrentPolicy: GET /disposition-policies/current ->
// GetDispositionPolicy (404 policy-not-found when none was ever declared).
func (s *Server) handleGetCurrentPolicy(w http.ResponseWriter, r *http.Request) {
	p, err := s.GetDispositionPolicy.Execute(r.Context())
	if err != nil {
		writeIfNotFound(w, r, err, policyNotFound)
		return
	}
	writeJSON(w, http.StatusOK, policyDTOFrom(p))
}
