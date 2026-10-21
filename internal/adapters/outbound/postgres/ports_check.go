package postgres

import (
	"github.com/claudioed/returns-processing/internal/application/ports"
)

// Compile-time proof that every outbound port has a Postgres implementation.
var (
	_ ports.ReturnAuthorizationRepo = (*RMAs)(nil)
	_ ports.ReturnReceiptRepo       = (*Receipts)(nil)
	_ ports.DispositionRepo         = (*Dispositions)(nil)
	_ ports.DispositionPolicyRepo   = (*Policies)(nil)
	_ ports.EventPublisher          = (*Outbox)(nil)
	_ ports.UnitOfWork              = (*UnitOfWork)(nil)
	_ ports.AllocatedLines          = (*AllocatedLines)(nil)
	_ ports.ManifestedUnits         = (*ManifestedUnits)(nil)
	_ ports.ProductClasses          = (*ProductClasses)(nil)
	_ ports.IDGen                   = (*IDGen)(nil)
	_ ports.Clock                   = (*Clock)(nil)
	_ ports.IdempotencyStore        = (*IdempotencyStore)(nil)
)
