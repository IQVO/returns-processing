package http

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// The wire kinds of a received line (apis/openapi.yaml, LineKind).
const (
	lineKindMatched    = "MATCHED"
	lineKindUnexpected = "UNEXPECTED"
)

// openReceiptLineRequest is one line of POST /returns/{rmaId}/receipts
// (OpenReceiptLine): kind is the discriminator, rmaId rides a MATCHED line
// and must be absent on an UNEXPECTED one.
type openReceiptLineRequest struct {
	LineNo   int32   `json:"lineNo"`
	SKU      string  `json:"sku"`
	Quantity int64   `json:"quantity"`
	Kind     string  `json:"kind"`
	RMAID    *string `json:"rmaId"`
}

// openReceiptRequest is the wire body of POST /returns/{rmaId}/receipts
// (OpenReturnReceiptRequest).
type openReceiptRequest struct {
	SiteID string                   `json:"siteId"`
	Lines  []openReceiptLineRequest `json:"lines"`
}

// handleOpenReturnReceipt: POST /returns/{rmaId}/receipts ->
// OpenReturnReceipt (201). The path's rmaId is the RMA the parcel was opened
// against; a line's own rmaId (which may differ, several RMAs in one parcel)
// rides the line.
func (s *Server) handleOpenReturnReceipt(w http.ResponseWriter, r *http.Request) {
	rmaID, err := idParam(r, "rmaId", errInvalidParam)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in openReceiptRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	lines := make([]usecases.ReceiptLine, 0, len(in.Lines))
	for _, l := range in.Lines {
		line, err := receiptLineFromWire(l)
		if err != nil {
			writeError(w, r, err)
			return
		}
		lines = append(lines, line)
	}
	receipt, err := s.OpenReturnReceipt.Execute(r.Context(), usecases.ReturnReceiptInput{
		RMAID:  rmaID,
		SiteID: in.SiteID,
		Lines:  lines,
	})
	if err != nil {
		writeIfNotFound(w, r, err, rmaNotFound)
		return
	}
	dto := receiptDTOFrom(receipt)
	w.Header().Set("Location", "/receipts/"+dto.ReceiptID)
	writeJSON(w, http.StatusCreated, dto)
}

// receiptLineFromWire maps one wire line to the use case input: the presence
// of the rma id is the discriminator there, kind on the wire. Everything the
// spec calls the refused third state (a MATCHED line without its rmaId, an
// UNEXPECTED one with one, any other kind) is 400 invalid-line HERE, before
// the use case runs — raised as the domain's own line sentinels so the
// problem catalogue maps them.
func receiptLineFromWire(l openReceiptLineRequest) (usecases.ReceiptLine, error) {
	matched := ""
	switch l.Kind {
	case lineKindMatched:
		if l.RMAID == nil || *l.RMAID == "" {
			return usecases.ReceiptLine{}, fmt.Errorf("%w: a MATCHED line carries its rmaId", returnreceipt.ErrMatchedLineWithoutRMA)
		}
		matched = *l.RMAID
	case lineKindUnexpected:
		if l.RMAID != nil && *l.RMAID != "" {
			return usecases.ReceiptLine{}, fmt.Errorf("%w: an UNEXPECTED line carries none", returnreceipt.ErrUnexpectedLineWithRMA)
		}
	default:
		return usecases.ReceiptLine{}, fmt.Errorf("%w: kind %q is neither MATCHED nor UNEXPECTED", returnreceipt.ErrLineKindMissing, l.Kind)
	}
	return usecases.ReceiptLine{LineNo: l.LineNo, SKU: l.SKU, Quantity: l.Quantity, RMAID: matched}, nil
}

// inspectReturnLineRequest is the wire body of
// POST /receipts/{receiptId}/lines/{lineNo}/inspections
// (InspectReturnLineRequest). quantity is required in practice: the spec's
// "defaults to the line's full received quantity" is NOT implemented (the
// handler cannot read the line; see the drift note in the step report), so
// an omitted quantity is answered 400 invalid-quantity.
type inspectReturnLineRequest struct {
	Grade       string   `json:"grade"`
	Quantity    int64    `json:"quantity"`
	ReasonCodes []string `json:"reasonCodes"`
}

// handleInspectReturnLine: POST /receipts/{receiptId}/lines/{lineNo}/inspections
// -> InspectReturnLine (200; the disposition is decided in the same unit of
// work).
func (s *Server) handleInspectReturnLine(w http.ResponseWriter, r *http.Request) {
	receiptID, err := idParam(r, "receiptId", errInvalidParam)
	if err != nil {
		writeError(w, r, err)
		return
	}
	lineNo, err := lineNoParam(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var in inspectReturnLineRequest
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, r, err)
		return
	}
	d, err := s.InspectReturnLine.Execute(r.Context(), usecases.Inspection{
		ReceiptID:   receiptID,
		LineNo:      lineNo,
		Grade:       shared.Grade(in.Grade),
		Quantity:    in.Quantity,
		ReasonCodes: in.ReasonCodes,
	})
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			writeProblem(w, r, receiptNotFound, "return receipt not found")
			return
		}
		if errors.Is(err, returnreceipt.ErrLineNotFound) {
			// The receipt exists but has no such line: the addressed
			// resource (receipt, line) is a miss.
			writeProblem(w, r, receiptNotFound, "the receipt has no line "+strconv.FormatInt(int64(lineNo), 10))
			return
		}
		writeError(w, r, err)
		return
	}
	disposition := dispositionDTOFrom(d)
	writeJSON(w, http.StatusOK, inspectionResultDTO{
		ReceiptID:   disposition.ReceiptID,
		LineNo:      disposition.LineNo,
		SKU:         disposition.SKU,
		Quantity:    disposition.Quantity,
		Grade:       disposition.Grade,
		ReasonCodes: in.ReasonCodes,
		// The use case stamps the inspection and the decision with the same
		// now, so the disposition's decidedAt IS the inspected-at instant.
		InspectedAt: disposition.DecidedAt,
		Disposition: disposition,
	})
}
