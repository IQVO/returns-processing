package http

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/claudioed/returns-processing/internal/adapters/inbound/http/page"
	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
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

// handleListReturnsByOrder: GET /returns?orderRef=&limit=&cursor= ->
// ListReturnsByOrder.Page (200 {items,nextCursor}, no items for an unknown
// order, never 404; 400 invalid-limit / invalid-cursor). Keyset order is
// (lineNo, rmaId).
func (s *Server) handleListReturnsByOrder(w http.ResponseWriter, r *http.Request) {
	orderRef, err := orderRefParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	limit, err := page.Limit(r)
	if err != nil {
		writeProblem(w, r, invalidLimit, invalidLimit.detail)
		return
	}
	after, ok := returnCursor(w, r)
	if !ok {
		return
	}
	list, err := s.ListByOrder.Page(r.Context(), orderRef, after, page.FetchSize(limit))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]returnDTO, 0, len(list))
	for _, v := range list {
		items = append(items, returnDTOFrom(v))
	}
	writeJSON(w, http.StatusOK, page.Of(items, limit, func(d returnDTO) page.Position {
		return page.Position{Sort: strconv.Itoa(int(d.LineNo)), ID: d.RMAID}
	}))
}

// returnCursor decodes the cursor query parameter into the repository's keyset
// position (nil on the first page). An undecodable cursor is answered 400
// invalid-cursor and reported as not ok.
func returnCursor(w http.ResponseWriter, r *http.Request) (*views.ReturnKey, bool) {
	pos, err := page.After(r)
	if err == nil && pos == nil {
		return nil, true
	}
	if err == nil {
		var lineNo int64
		if lineNo, err = strconv.ParseInt(pos.Sort, 10, 32); err == nil {
			return &views.ReturnKey{LineNo: int32(lineNo), ID: pos.ID}, true
		}
	}
	writeProblem(w, r, invalidCursor, invalidCursor.detail)
	return nil, false
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
