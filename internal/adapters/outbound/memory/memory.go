// Package memory holds the in-memory test doubles for the application
// ports (unit tests and BDD wiring only). The shipped binary never imports
// it: TestRuntimeBinaryHasNoInMemoryAdapters enforces that, and the runtime
// is real Postgres + real Kafka only.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/disposition"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/returnreceipt"
)

// The doubles return the PORT sentinel (ports.ErrNotFound) on a miss so
// use-case errors.Is checks hold against every adapter, memory and Postgres.

// RMAs is the ReturnAuthorizationRepo double. Like the Postgres adapter, it
// round-trips through clone: a saved RMA is stored as an independent copy
// with its pending events consumed, so a later read never replays
// already-published facts and caller mutations never corrupt stored state.
type RMAs struct {
	mu    sync.Mutex
	byID  map[string]*returnauthorization.RMA
	order []string
}

// NewRMAs returns an empty double.
func NewRMAs() *RMAs { return &RMAs{byID: map[string]*returnauthorization.RMA{}} }

// Save stores the latest copy of r. Like the Postgres adapter, Save is
// guarded by the aggregate version: an rma whose version is not exactly the
// stored version + 1 is a concurrent modification (a fresh insert has no
// stored version and always lands). The one-open-RMA rule (ADR 0002
// invariant 1) is enforced exactly like the partial unique index: a second
// open RMA for the same order line is ErrOpenRMAExists, whatever the id.
func (r *RMAs) Save(_ context.Context, rma *returnauthorization.RMA) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stored, ok := r.byID[rma.ID()]; ok && rma.Version() != stored.Version()+1 {
		return ports.ErrConcurrentModification
	}
	if err := r.refuseOpenSibling(rma); err != nil {
		return err
	}
	if _, ok := r.byID[rma.ID()]; !ok {
		r.order = append(r.order, rma.ID())
	}
	r.byID[rma.ID()] = clone(rma)
	return nil
}

// refuseOpenSibling is the in-memory stand-in of the
// uq_rmas_one_open_per_order_line partial index: at most ONE open RMA per
// (order, line), REQUESTED/AUTHORIZED/RECEIVED counting as open.
func (r *RMAs) refuseOpenSibling(rma *returnauthorization.RMA) error {
	if !rma.Open() {
		return nil
	}
	for _, stored := range r.byID {
		if stored.ID() == rma.ID() || !stored.Open() {
			continue
		}
		line := stored.Line()
		if line.OrderID().String() == rma.Line().OrderID().String() && line.LineNo() == rma.Line().LineNo() {
			return returnauthorization.ErrOpenRMAExists
		}
	}
	return nil
}

// FindByID returns the RMA or not-found.
func (r *RMAs) FindByID(_ context.Context, id string) (*returnauthorization.RMA, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return clone(stored), nil
}

// FindOpenByOrderLine returns the RMA still open for the order line, or
// not-found.
func (r *RMAs) FindOpenByOrderLine(_ context.Context, orderID string, lineNo int32) (*returnauthorization.RMA, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range r.order {
		rma := r.byID[id]
		if !rma.Open() {
			continue
		}
		line := rma.Line()
		if line.OrderID().String() == orderID && line.LineNo() == lineNo {
			return clone(rma), nil
		}
	}
	return nil, ports.ErrNotFound
}

// ListByOrder returns at most limit RMAs of the order, by line number then id,
// strictly after the keyset position after (the first page when nil) — the
// same (line_no, rma_id) keyset the Postgres adapter queries.
func (r *RMAs) ListByOrder(_ context.Context, orderID string, after *views.ReturnKey, limit int) ([]*returnauthorization.RMA, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*returnauthorization.RMA
	for _, id := range r.order {
		rma := r.byID[id]
		if rma.Line().OrderID().String() == orderID && afterKey(rma, after) {
			out = append(out, clone(rma))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Line().LineNo() != out[j].Line().LineNo() {
			return out[i].Line().LineNo() < out[j].Line().LineNo()
		}
		return out[i].ID() < out[j].ID()
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// afterKey reports whether rma sorts strictly after the keyset position (every
// RMA does when after is nil).
func afterKey(rma *returnauthorization.RMA, after *views.ReturnKey) bool {
	if after == nil {
		return true
	}
	if line := rma.Line().LineNo(); line != after.LineNo {
		return line > after.LineNo
	}
	return rma.ID() > after.ID
}

// Receipts is the ReturnReceiptRepo double (clone round-trip, like RMAs).
type Receipts struct {
	mu   sync.Mutex
	byID map[string]*returnreceipt.Receipt
}

// NewReceipts returns an empty double.
func NewReceipts() *Receipts { return &Receipts{byID: map[string]*returnreceipt.Receipt{}} }

// Save stores the latest copy of r, guarded by the aggregate version like
// the Postgres adapter (a fresh insert always lands).
func (r *Receipts) Save(_ context.Context, rc *returnreceipt.Receipt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if stored, ok := r.byID[rc.ID()]; ok && rc.Version() != stored.Version()+1 {
		return ports.ErrConcurrentModification
	}
	r.byID[rc.ID()] = clone(rc)
	return nil
}

// FindByID returns the receipt or not-found.
func (r *Receipts) FindByID(_ context.Context, id string) (*returnreceipt.Receipt, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return clone(stored), nil
}

// Dispositions is the DispositionRepo double. FindByReceiptLine returns
// the decision saved LAST for the line — the superseding override, never
// the disposition it replaced.
type Dispositions struct {
	mu     sync.Mutex
	byID   map[string]*disposition.Disposition
	byLine map[string]string // "{receipt}\x00{line}" -> disposition id
}

// NewDispositions returns an empty double.
func NewDispositions() *Dispositions {
	return &Dispositions{
		byID:   map[string]*disposition.Disposition{},
		byLine: map[string]string{},
	}
}

// Save stores the decision and marks it the line's latest. A disposition is
// immutable at version 1, so re-saving an existing id is a concurrent
// modification, exactly like the Postgres primary-key conflict; and the
// one-policy-decision-per-line rule is enforced exactly like the partial
// unique index: a second NON-superseding decision for a line that already
// has one is a concurrent modification (an override carries supersedes and
// is exempt).
func (r *Dispositions) Save(_ context.Context, d *disposition.Disposition) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[d.ID()]; ok {
		return ports.ErrConcurrentModification
	}
	key := fmt.Sprintf("%s\x00%d", d.ReceiptID(), d.LineNo())
	if d.Supersedes() == "" {
		if prevID, ok := r.byLine[key]; ok {
			if prev := r.byID[prevID]; prev != nil && prev.Supersedes() == "" {
				return ports.ErrConcurrentModification
			}
		}
	}
	r.byID[d.ID()] = clone(d)
	r.byLine[key] = d.ID()
	return nil
}

// FindByID returns the disposition or not-found.
func (r *Dispositions) FindByID(_ context.Context, id string) (*disposition.Disposition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[id]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return clone(stored), nil
}

// FindByReceiptLine returns the line's latest decision, or not-found.
func (r *Dispositions) FindByReceiptLine(_ context.Context, receiptID string, lineNo int32) (*disposition.Disposition, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byLine[fmt.Sprintf("%s\x00%d", receiptID, lineNo)]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return clone(r.byID[id]), nil
}

// Policies is the DispositionPolicyRepo double: append-only versions, the
// latest one current.
type Policies struct {
	mu      sync.Mutex
	byVer   map[int64]*disposition.DispositionPolicy
	current *disposition.DispositionPolicy
}

// NewPolicies returns an empty double.
func NewPolicies() *Policies { return &Policies{byVer: map[int64]*disposition.DispositionPolicy{}} }

// Save stores the version and marks it current. A NEW version appends; a
// re-declared version replaces its row (the use cases re-declare v1 when a
// fixture resets the table), which the Postgres adapter mirrors with an
// upsert on the version primary key.
func (r *Policies) Save(_ context.Context, p *disposition.DispositionPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byVer[p.Version()] = clone(p)
	r.current = clone(p)
	return nil
}

// Current returns the latest declared version, or not-found.
func (r *Policies) Current(_ context.Context) (*disposition.DispositionPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current == nil {
		return nil, ports.ErrNotFound
	}
	return clone(r.current), nil
}

// FindByVersion returns the declared version, or not-found.
func (r *Policies) FindByVersion(_ context.Context, version int64) (*disposition.DispositionPolicy, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byVer[version]
	if !ok {
		return nil, ports.ErrNotFound
	}
	return clone(stored), nil
}

// Events collects everything published, for assertions.
type Events struct {
	mu          sync.Mutex
	RMA         []returnauthorization.Event
	Receipt     []returnreceipt.Event
	Disposition []disposition.Event
}

// PublishRMAEvents records RMA facts.
func (e *Events) PublishRMAEvents(_ context.Context, events ...returnauthorization.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.RMA = append(e.RMA, events...)
	return nil
}

// PublishReceiptEvents records receipt facts.
func (e *Events) PublishReceiptEvents(_ context.Context, events ...returnreceipt.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Receipt = append(e.Receipt, events...)
	return nil
}

// PublishDispositionEvents records disposition facts.
func (e *Events) PublishDispositionEvents(_ context.Context, events ...disposition.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Disposition = append(e.Disposition, events...)
	return nil
}

// RMAEventNames returns the recorded RMA fact names in order.
func (e *Events) RMAEventNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return names(len(e.RMA), func(i int) string { return e.RMA[i].EventName() })
}

// ReceiptEventNames returns the recorded receipt fact names in order.
func (e *Events) ReceiptEventNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return names(len(e.Receipt), func(i int) string { return e.Receipt[i].EventName() })
}

// DispositionEventNames returns the recorded disposition fact names in order.
func (e *Events) DispositionEventNames() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return names(len(e.Disposition), func(i int) string { return e.Disposition[i].EventName() })
}

func names(n int, name func(int) string) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, name(i))
	}
	return out
}

// IDGen mints sequential prefixed ids.
type IDGen struct {
	mu sync.Mutex
	n  map[string]int
}

// NewIDGen returns a generator counting from 1 per prefix.
func NewIDGen() *IDGen { return &IDGen{n: map[string]int{}} }

// New returns {prefix}-{0001...}.
func (g *IDGen) New(prefix string) string {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n[prefix]++
	return fmt.Sprintf("%s-%04d", prefix, g.n[prefix])
}

// Clock is a fixed clock.
type Clock struct{ T time.Time }

// Now returns the fixed instant.
func (c Clock) Now() time.Time { return c.T }
