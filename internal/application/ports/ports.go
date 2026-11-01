// Package ports declares the application's OUT ports: the repository,
// local-copy read and driven interfaces the use cases rely on and the
// outbound adapters (Postgres, the transactional outbox, the in-memory
// doubles) implement. The package holds interfaces and the ErrNotFound
// sentinel only — the architecture fitness test enforces interfaces-only
// here — and every read DTO the inbound adapters consume lives one package
// over, in internal/application/views.
package ports

import (
	"context"
	"errors"
	"time"

	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// ErrNotFound is the typed miss every Find*/Current/Get port returns: the
// requested aggregate or policy version does not exist. The HTTP adapter
// maps it to 404; use-case errors.Is checks hold against every adapter,
// memory and Postgres alike.
var ErrNotFound = errors.New("ports: not found")

// ErrConcurrentModification is the typed optimistic-concurrency conflict
// every repo Save can return: the stored row's version is not the version
// the caller loaded (another writer got there first, or the caller is saving
// stale state). The Postgres adapter raises it when the version-guarded
// upsert affects 0 rows; the memory double mirrors the same rule so the
// repocontract proves both adapters behave alike. The HTTP adapter maps it
// to 409.
var ErrConcurrentModification = errors.New("ports: concurrent modification")

// ReturnAuthorizationRepo persists RMA aggregates by id and carries the
// one-open-RMA-per-order-line invariant (ADR 0002). Inside a UnitOfWork it
// joins the transaction carried in ctx. The Postgres adapter guards every
// Save by the aggregate version the caller loaded (optimistic concurrency)
// and closes the one-open-RMA race with a partial unique index.
type ReturnAuthorizationRepo interface {
	// Save persists r. The in-memory double consumes r's pending domain
	// events (a later read returns them empty), exactly like the Postgres
	// adapter whose outbox write and aggregate write commit together.
	Save(ctx context.Context, r *returnauthorization.RMA) error
	// FindByID returns the RMA, or ErrNotFound.
	FindByID(ctx context.Context, id string) (*returnauthorization.RMA, error)
	// FindOpenByOrderLine returns the RMA that is still open (REQUESTED,
	// AUTHORIZED or RECEIVED) for orderID's lineNo, backing the
	// one-open-RMA rule, or ErrNotFound when none is.
	FindOpenByOrderLine(ctx context.Context, orderID string, lineNo int32) (*returnauthorization.RMA, error)
	// ListByOrder returns at most limit RMAs of the order, by line number
	// then id (byte order), strictly after the keyset position after (the
	// first page when after is nil). limit must be at least 1.
	ListByOrder(ctx context.Context, orderID string, after *views.ReturnKey, limit int) ([]*returnauthorization.RMA, error)
}

// ReturnReceiptRepo persists ReturnReceipt aggregates by id. Inside a
// UnitOfWork it joins the transaction carried in ctx.
type ReturnReceiptRepo interface {
	// Save persists r, consuming its pending domain events like the outbox
	// write would.
	Save(ctx context.Context, r *returnreceipt.Receipt) error
	// FindByID returns the receipt, or ErrNotFound.
	FindByID(ctx context.Context, id string) (*returnreceipt.Receipt, error)
}

// DispositionRepo persists the immutable Disposition aggregates by id, with
// the receipt-line lookup the override and read paths need. Inside a
// UnitOfWork it joins the transaction carried in ctx.
type DispositionRepo interface {
	// Save persists d, consuming its pending domain events like the outbox
	// write would.
	Save(ctx context.Context, d *disposition.Disposition) error
	// FindByID returns the disposition, or ErrNotFound.
	FindByID(ctx context.Context, id string) (*disposition.Disposition, error)
	// FindByReceiptLine returns the disposition decided for receiptID's
	// lineNo, or ErrNotFound when the line has none (an override target must
	// exist; the latest decision for a line is the one not superseded).
	FindByReceiptLine(ctx context.Context, receiptID string, lineNo int32) (*disposition.Disposition, error)
}

// DispositionPolicyRepo persists the append-only DispositionPolicy versions
// (ADR 0002: a change is a new version, never a rewrite). Inside a
// UnitOfWork it joins the transaction carried in ctx.
type DispositionPolicyRepo interface {
	// Save persists one policy version and marks it current.
	Save(ctx context.Context, p *disposition.DispositionPolicy) error
	// Current returns the latest declared version, or ErrNotFound when no
	// policy was ever declared.
	Current(ctx context.Context) (*disposition.DispositionPolicy, error)
	// FindByVersion returns the declared version, or ErrNotFound.
	FindByVersion(ctx context.Context, version int64) (*disposition.DispositionPolicy, error)
}

// EventPublisher is the write side of the transactional outbox, in domain
// terms: it is called in the same UnitOfWork as the aggregate's Save, so a
// state change and its facts commit or roll back together. The domain hands
// each aggregate its own Event interface, so the port speaks those three —
// one method per aggregate — and the outbound adapter maps every fact to
// CloudEvents on the wire (ADR 0004).
type EventPublisher interface {
	// PublishRMAEvents enqueues the facts an RMA raised.
	PublishRMAEvents(ctx context.Context, events ...returnauthorization.Event) error
	// PublishReceiptEvents enqueues the facts a ReturnReceipt raised.
	PublishReceiptEvents(ctx context.Context, events ...returnreceipt.Event) error
	// PublishDispositionEvents enqueues the facts a Disposition raised.
	PublishDispositionEvents(ctx context.Context, events ...disposition.Event) error
}

// AllocatedLines is the event-fed local copy of order-management's
// allocations (ADR 0003): which order lines exist and the SKU each carries.
// The consumer step feeds it; the application only reads it.
type AllocatedLines interface {
	// Get returns the allocated order line, or ok=false when the copy holds
	// no such line (fail-closed: a line the copy does not know cannot be
	// returned).
	Get(orderID string, lineNo int32) (views.AllocatedLine, bool)
}

// ManifestedUnits is the event-fed local copy of fulfillment-execution's
// package manifests (ADR 0003): which order lines actually shipped, keyed by
// the WES work unit `{order_id}-line-{line_no}` and by package id.
type ManifestedUnits interface {
	// GetByOrderLine returns the manifested work unit of orderID's lineNo,
	// or ok=false when the line never shipped.
	GetByOrderLine(orderID string, lineNo int32) (views.ManifestedUnit, bool)
	// GetByPackage returns the manifested work unit a package id stands for,
	// or ok=false.
	GetByPackage(packageID string) (views.ManifestedUnit, bool)
}

// ProductClasses is the event-fed local copy of product-master
// classifications (ADR 0003). Its tags carry the classification facts the
// disposition guardrails need — see views.ProductClass for the tag grammar.
type ProductClasses interface {
	// Get returns the SKU's classification copy, or ok=false when the copy
	// holds nothing (fail-open for grading: the "*" default rule catches an
	// unclassified SKU, the hazmat guardrail applies once known).
	Get(sku string) (views.ProductClass, bool)
}

// ProcessedEvents is the inbound consumers' idempotency guard, the fleet's
// dedupe rule: a consumed CloudEvent's id is claimed in the SAME unit of
// work as its effect, so the two commit or roll back together and a
// redelivery after a rollback is processed, not skipped.
type ProcessedEvents interface {
	// Claim records eventID as processed and reports whether THIS call
	// claimed it first (false = a duplicate redelivery). Inside a
	// UnitOfWork it joins the transaction carried in ctx.
	Claim(ctx context.Context, eventID string) (bool, error)
}

// ProductClassFeed is the write side of the classification local copy
// (ADR 0003): the product-master consumer applies one classification per
// ProductClassified fact. It is the same Postgres object the Get port
// reads; the application states them apart so the consumer and the use
// cases depend on exactly the half each needs.
type ProductClassFeed interface {
	// ApplyProductClass upserts the SKU's classification. Only a NEWER
	// version takes effect — the LOCAL COPY RULE of the contracts
	// (ProductClassified carries a version; a replayed old version must
	// not overwrite a newer copy).
	ApplyProductClass(ctx context.Context, sku string, pc views.ProductClass) error
}

// AllocatedLineFeed is the write side of the allocations local copy
// (ADR 0003): the order-management consumer applies every allocated line of
// one OrderAllocated / OrderPartiallyAllocated fact.
type AllocatedLineFeed interface {
	// ApplyAllocatedLine upserts one allocated order line. The sources
	// carry no version, so a newer fact is last-write-wins and the local
	// version counter just bumps.
	ApplyAllocatedLine(ctx context.Context, orderID string, lineNo int32, sku string) error
}

// ManifestedUnitFeed is the write side of the package-manifest local copy
// (ADR 0003): the fulfillment-execution consumer applies one manifested work
// unit per PackageManifested fact.
type ManifestedUnitFeed interface {
	// ApplyManifestedUnit upserts the manifested work unit of orderID's
	// lineNo, keyed by the WES work-unit id `{order_id}-line-{line_no}`.
	ApplyManifestedUnit(ctx context.Context, orderID string, lineNo int32, unit views.ManifestedUnit) error
}

// IDGen mints aggregate ids. The real generator's shape (prefixed, sortable)
// is an adapter concern; use cases only need a fresh unique string per
// aggregate kind.
type IDGen interface {
	// New returns a fresh id for the aggregate kind named by prefix.
	New(prefix string) string
}

// Clock is the service's notion of "now". Use cases stamp every decision,
// transition and fact with it; the domain never reads a clock.
type Clock interface {
	Now() time.Time
}

// IdempotencyStore keeps the answers of the POSTs/PUTs made with an
// Idempotency-Key, so a retried request replays the stored response instead
// of running its effects twice. Only 2xx and 4xx answers are ever stored
// (the HTTP middleware's rule); the first writer of a key wins.
type IdempotencyStore interface {
	// Lookup returns the stored answer of key, or ok=false when the key has
	// none.
	Lookup(ctx context.Context, key string) (views.IdempotencyRecord, bool, error)
	// Save stores rec. A key that already has an answer keeps it: the first
	// writer wins and a later Save changes nothing.
	Save(ctx context.Context, rec views.IdempotencyRecord) error
}

// UnitOfWork runs fn as ONE atomic unit: every port call fn makes with the
// ctx it is handed commits together when fn returns nil, or rolls back
// together when fn returns an error (or panics). Use cases tolerate a NIL
// UnitOfWork — nil means the calls run unwrapped (in-memory wiring, tests).
//
// Contract:
//   - fn MUST use the ctx passed to it (not the outer ctx) for every port
//     call that should participate; the unit of work travels in that ctx.
//   - Do is re-entrant: if ctx already carries an open unit of work, Do JOINS
//     it and the outermost Do owns commit/rollback.
//   - The error fn returns is returned from Do unchanged (errors.Is keeps
//     working); a commit failure is returned wrapped.
type UnitOfWork interface {
	Do(ctx context.Context, fn func(ctx context.Context) error) error
}
