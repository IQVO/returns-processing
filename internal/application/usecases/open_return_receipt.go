package usecases

import (
	"context"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// ReceiptLine is one line of the arriving parcel on the wire: a matched line
// carries the rma it returns (RMAID != ""), an unexpected one carries none —
// the presence of the rma id is the discriminator, so the refused third
// state cannot be expressed.
type ReceiptLine struct {
	LineNo   int32
	SKU      string
	Quantity int64
	RMAID    string
}

// ReturnReceiptInput is the receiving input: the rma the parcel was opened
// against ("" when the whole parcel is unexpected), the site it arrived at,
// and its lines.
type ReturnReceiptInput struct {
	RMAID  string
	SiteID string
	Lines  []ReceiptLine
}

// OpenReturnReceipt records the physical arrival of returned goods (ADR
// 0002): one parcel, one site, every line matched to an RMA or explicitly
// unexpected. ReturnReceiptOpened — and one UnexpectedReturnReceived per
// unexpected line, which exception-management opens a case on — publishes in
// the same unit of work as the save.
type OpenReturnReceipt struct {
	Receipts   ports.ReturnReceiptRepo
	Events     ports.EventPublisher
	IDs        ports.IDGen
	Clock      ports.Clock
	UnitOfWork ports.UnitOfWork // nil = unwrapped
}

// Execute opens the receipt and returns it (version 1).
func (uc *OpenReturnReceipt) Execute(ctx context.Context, in ReturnReceiptInput) (*returnreceipt.Receipt, error) {
	lines := make([]returnreceipt.Line, 0, len(in.Lines))
	for _, l := range in.Lines {
		var (
			line returnreceipt.Line
			err  error
		)
		if l.RMAID != "" {
			line, err = returnreceipt.MatchedLine(l.LineNo, l.SKU, l.Quantity, l.RMAID)
		} else {
			line, err = returnreceipt.UnexpectedLine(l.LineNo, l.SKU, l.Quantity)
		}
		if err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	r, err := returnreceipt.Open(returnreceipt.OpenInput{
		// "rcpt-": the id prefix of apis/openapi.yaml's ReceiptId pattern
		// (the spec is the contract; a service-minted id must match it).
		ReceiptID: uc.IDs.New("rcpt"),
		RMAID:     in.RMAID,
		SiteID:    in.SiteID,
		Lines:     lines,
		At:        uc.Clock.Now(),
	})
	if err != nil {
		return nil, err
	}
	if err := atomically(ctx, uc.UnitOfWork, func(ctx context.Context) error {
		if err := uc.Receipts.Save(ctx, r); err != nil {
			return err
		}
		return uc.Events.PublishReceiptEvents(ctx, r.DomainEvents()...)
	}); err != nil {
		return nil, err
	}
	return r, nil
}
