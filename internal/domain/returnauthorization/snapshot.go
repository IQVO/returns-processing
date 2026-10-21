package returnauthorization

import (
	"time"

	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// Snapshot is the persisted form of an RMA: exactly the columns of the
// return_authorizations table, scalars only. Restore validates it.
type Snapshot struct {
	ID            string
	OrderID       string
	LineNo        int32
	SKU           string
	Quantity      int64
	SiteID        string
	ReasonCode    string
	Status        Status
	RejectionCode RejectionCode
	WindowEndsAt  time.Time
	DecidedAt     time.Time
	ReceivedAt    time.Time
	ClosedAt      time.Time
	Version       int64
}

// Snapshot returns the persisted form of the RMA (no events: a restored RMA
// has none pending).
func (r *RMA) Snapshot() Snapshot {
	return Snapshot{
		ID:            r.id.String(),
		OrderID:       r.line.OrderID().String(),
		LineNo:        r.line.LineNo(),
		SKU:           r.sku.String(),
		Quantity:      r.quantity.Int64(),
		SiteID:        r.site.String(),
		ReasonCode:    r.reasonCode,
		Status:        r.status,
		RejectionCode: r.rejectionCode,
		WindowEndsAt:  r.windowEndsAt,
		DecidedAt:     r.decidedAt,
		ReceivedAt:    r.receivedAt,
		ClosedAt:      r.closedAt,
		Version:       r.version,
	}
}

// Restore rebuilds a persisted RMA, validating every invariant the snapshot
// can show (ids and quantity through the shared value objects, the status
// machine's pairing rules), so a corrupt row can never become a live
// aggregate. The restored RMA has no pending events.
func Restore(s Snapshot) (*RMA, error) {
	id, err := shared.NewRMAID(s.ID)
	if err != nil {
		return nil, err
	}
	orderID, err := shared.NewOrderID(s.OrderID)
	if err != nil {
		return nil, err
	}
	line, err := shared.NewOrderLineRef(orderID, s.LineNo)
	if err != nil {
		return nil, err
	}
	sku, err := shared.NewSKU(s.SKU)
	if err != nil {
		return nil, err
	}
	quantity, err := shared.NewQuantity(s.Quantity)
	if err != nil {
		return nil, err
	}
	site, err := shared.NewSiteID(s.SiteID)
	if err != nil {
		return nil, err
	}
	if shared.IsBlank(s.ReasonCode) {
		return nil, ErrBlankReasonCode
	}
	if err := validateSnapshotState(s); err != nil {
		return nil, err
	}
	return &RMA{
		id:            id,
		line:          line,
		sku:           sku,
		quantity:      quantity,
		site:          site,
		reasonCode:    s.ReasonCode,
		status:        s.Status,
		rejectionCode: s.RejectionCode,
		windowEndsAt:  s.WindowEndsAt.UTC(),
		decidedAt:     s.DecidedAt.UTC(),
		receivedAt:    s.ReceivedAt.UTC(),
		closedAt:      s.ClosedAt.UTC(),
		version:       s.Version,
	}, nil
}

// validateSnapshotState holds the state-machine pairings a snapshot must
// satisfy: a known status; a rejection code exactly when REJECTED; version 0
// only while REQUESTED, and at least 1 once decided.
func validateSnapshotState(s Snapshot) error {
	if !s.Status.Valid() {
		return ErrInvalidStatus
	}
	if (s.Status == StatusRejected) != (s.RejectionCode != "") {
		return ErrInvalidSnapshot
	}
	if s.RejectionCode != "" && !s.RejectionCode.Valid() {
		return ErrInvalidRejectionCode
	}
	if s.Status == StatusRequested && s.Version != 0 {
		return ErrInvalidSnapshot
	}
	if s.Status != StatusRequested && s.Version < 1 {
		return ErrInvalidSnapshot
	}
	return nil
}
