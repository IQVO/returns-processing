package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// errInvalidParam marks a path or query parameter outside its OpenAPI shape;
// it shares the 400 malformed-request-body problem (the spec's BadRequest
// response covers "an invalid path, query or header parameter").
var errInvalidParam = errMalformedRequest

// authorizeReturnRequest is the wire body of POST /returns
// (apis/openapi.yaml, AuthorizeReturnRequest). sku is REQUIRED by the spec
// but advisory to the service: the allocations local copy is the
// authoritative sku of the line (ADR 0003), so the value is accepted and not
// compared — a mismatch never refuses an otherwise valid return.
type authorizeReturnRequest struct {
	OrderID    string `json:"orderId"`
	LineNo     int32  `json:"lineNo"`
	SKU        string `json:"sku"`
	Quantity   int64  `json:"quantity"`
	SiteID     string `json:"siteId"`
	ReasonCode string `json:"reasonCode"`
}

// handleAuthorizeReturn: POST /returns -> AuthorizeReturn (201; 409
// return-not-authorizable with the rejection code in the detail).
func (s *Server) handleAuthorizeReturn(w http.ResponseWriter, r *http.Request) {
	var in authorizeReturnRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	rma, err := s.AuthorizeReturn.Execute(r.Context(), usecases.ReturnRequest{
		OrderID:    in.OrderID,
		LineNo:     in.LineNo,
		Quantity:   in.Quantity,
		SiteID:     in.SiteID,
		ReasonCode: in.ReasonCode,
	})
	if err != nil {
		// A miss here is the missing policy (the only port the use case
		// reads first): the line cannot be authorized against no policy.
		if errors.Is(err, ports.ErrNotFound) {
			writeProblem(w, r, notAuthorizable, "no disposition policy version is declared; PUT /disposition-policies/1 first")
			return
		}
		if p := problemFor(err); p == notAuthorizable {
			writeProblem(w, r, p, rejectionDetail(err))
			return
		}
		writeError(w, r, err)
		return
	}
	dto := returnDTOFrom(usecases.ReturnView(rma))
	w.Header().Set("Location", "/returns/"+dto.RMAID)
	writeJSON(w, http.StatusCreated, dto)
}

// handleGetReturn: GET /returns/{rmaId} -> GetReturn (404 rma-not-found on a
// miss).
func (s *Server) handleGetReturn(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "rmaId", errInvalidParam)
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.GetReturn.Execute(r.Context(), id)
	if err != nil {
		writeIfNotFound(w, r, err, rmaNotFound)
		return
	}
	writeJSON(w, http.StatusOK, returnDTOFrom(v))
}

// handleListReturnsByOrder: GET /returns?orderRef= -> ListReturnsByOrder
// (200 with items, [] for an unknown order, never 404).
func (s *Server) handleListReturnsByOrder(w http.ResponseWriter, r *http.Request) {
	orderRef, err := orderRefParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	list, err := s.ListByOrder.Execute(r.Context(), orderRef)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]returnDTO, 0, len(list)) // never nil: the wire answer is []
	for _, v := range list {
		items = append(items, returnDTOFrom(v))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// writeIfNotFound answers err with the given typed not-found problem on a
// ports.ErrNotFound miss, or its catalogue problem otherwise.
func writeIfNotFound(w http.ResponseWriter, r *http.Request, err error, notFound problem) {
	if errors.Is(err, ports.ErrNotFound) {
		writeProblem(w, r, notFound, notFound.detail)
		return
	}
	writeError(w, r, err)
}
