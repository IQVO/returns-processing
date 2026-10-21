package usecases_test

import (
	"context"
	"errors"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/application/views"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
)

func (f fixture) authorizeUC() *usecases.AuthorizeReturn {
	return &usecases.AuthorizeReturn{
		RMAs: f.rmas, Policies: f.policies, Allocated: f.allocated,
		Manifested: f.manifested, Events: f.events, IDs: f.ids, Clock: f.clock(),
	}
}

func TestAuthorizeReturn_Happy(t *testing.T) {
	f := newFixture(t)
	rma := f.authorize(t, "ord-1", 2, 3)
	if rma.Status() != returnauthorization.StatusAuthorized || rma.Version() != 1 {
		t.Fatalf("rma status=%s version=%d", rma.Status(), rma.Version())
	}
	if rma.SKU() != "sku-1" || rma.Quantity() != 3 || rma.SiteID() != "SITE1" || rma.ReasonCode() != "DAMAGED" {
		t.Fatalf("rma facts wrong: sku=%s qty=%d site=%s reason=%s", rma.SKU(), rma.Quantity(), rma.SiteID(), rma.ReasonCode())
	}
	if !rma.WindowEndsAt().Equal(f.now.AddDate(0, 0, 30)) {
		t.Fatalf("window = %v, want now+30d", rma.WindowEndsAt())
	}
	if names := f.events.RMAEventNames(); len(names) != 1 || names[0] != "ReturnAuthorized" {
		t.Fatalf("events = %v", names)
	}
	// the id came from IDGen and the decision is persisted
	if rma.ID() != "rma-0001" {
		t.Fatalf("id = %s, want the generator's first rma id", rma.ID())
	}
	open, err := f.rmas.FindOpenByOrderLine(context.Background(), "ord-1", 2)
	if err != nil || open.ID() != rma.ID() {
		t.Fatalf("not persisted as the open rma: err=%v", err)
	}
}

// TestAuthorizeReturn_Refusals covers every typed refusal: each row names
// the domain (or application) error the caller must see, and proves a
// refusal persists nothing and publishes nothing.
func TestAuthorizeReturn_Refusals(t *testing.T) {
	cases := []struct {
		name string
		want error
		// persists marks rows whose setup legitimately stored an RMA (the
		// open-sibling row authorizes one first).
		persists bool
		prep     func(f fixture) usecases.ReturnRequest
	}{
		{
			name: "unallocated line fails closed",
			want: usecases.ErrOrderLineNotAllocated,
			prep: func(f fixture) usecases.ReturnRequest {
				f.declare(t)
				f.manifested.PutByOrderLine("ord-1", 2, views.ManifestedUnit{PackageID: "pkg-1", ManifestedAt: f.now})
				return request("ord-1", 2, 1)
			},
		},
		{
			name: "unmanifested line is refused",
			want: returnauthorization.ErrLineNotManifested,
			prep: func(f fixture) usecases.ReturnRequest {
				f.declare(t)
				f.allocated.Put("ord-1", 2, views.AllocatedLine{SKU: "sku-1"})
				return request("ord-1", 2, 1)
			},
		},
		{
			name:     "one open rma per line",
			want:     returnauthorization.ErrOpenRMAExists,
			persists: true,
			prep: func(f fixture) usecases.ReturnRequest {
				f.authorize(t, "ord-1", 2, 1)
				return request("ord-1", 2, 1)
			},
		},
		{
			name: "quantity over the policy cap",
			want: returnauthorization.ErrQuantityExceedsPolicy,
			prep: func(f fixture) usecases.ReturnRequest {
				f.declare(t)
				f.seed("ord-1", 2, "sku-1", "pkg-1")
				return request("ord-1", 2, 6) // cap is 5
			},
		},
		{
			name: "blank reason code",
			want: returnauthorization.ErrBlankReasonCode,
			prep: func(f fixture) usecases.ReturnRequest {
				f.declare(t)
				f.seed("ord-1", 2, "sku-1", "pkg-1")
				r := request("ord-1", 2, 1)
				r.ReasonCode = "   "
				return r
			},
		},
		{
			name: "no policy declared",
			want: ports.ErrNotFound,
			prep: func(f fixture) usecases.ReturnRequest {
				f.seed("ord-1", 2, "sku-1", "pkg-1")
				return request("ord-1", 2, 1)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			in := tc.prep(f)
			_, err := f.authorizeUC().Execute(context.Background(), in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			assertRefusalLeftNothing(t, f, in, tc.persists)
		})
	}
}

// assertRefusalLeftNothing checks a refusal published nothing and persisted
// nothing (unless the row's setup legitimately stored an RMA).
func assertRefusalLeftNothing(t *testing.T, f fixture, in usecases.ReturnRequest, persists bool) {
	t.Helper()
	if !persists {
		if names := f.events.RMAEventNames(); len(names) != 0 {
			t.Fatalf("refusal published %v", names)
		}
	}
	_, findErr := f.rmas.FindOpenByOrderLine(context.Background(), in.OrderID, in.LineNo)
	if persists {
		if findErr != nil {
			t.Fatalf("setup rma vanished: %v", findErr)
		}
		return
	}
	if !errors.Is(findErr, ports.ErrNotFound) {
		t.Fatalf("refusal persisted something: %v", findErr)
	}
}

// TestAuthorizeReturn_RejectionCodeMapping proves the typed rejection the
// HTTP edge maps with RejectionCodeFor survives the bubble unchanged.
func TestAuthorizeReturn_RejectionCodeMapping(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	f.seed("ord-1", 2, "sku-1", "pkg-1")
	_, err := f.authorizeUC().Execute(context.Background(), request("ord-1", 2, 6))
	if code := returnauthorization.RejectionCodeFor(err); code != returnauthorization.RejectQuantityExceedsMax {
		t.Fatalf("rejection code = %q, want QUANTITY_EXCEEDS_POLICY", code)
	}
}

// TestAuthorizeReturn_SkuComesFromTheAllocationsCopy pins the shape
// decision: the copy is the sku truth, not the caller.
func TestAuthorizeReturn_SkuComesFromTheAllocationsCopy(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	f.allocated.Put("ord-1", 2, views.AllocatedLine{SKU: "sku-truth"})
	f.manifested.PutByOrderLine("ord-1", 2, views.ManifestedUnit{PackageID: "pkg-1", ManifestedAt: f.now})
	rma, err := f.authorizeUC().Execute(context.Background(), request("ord-1", 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	if rma.SKU() != "sku-truth" {
		t.Fatalf("sku = %s, want sku-truth from the copy", rma.SKU())
	}
	if rma.Line().WorkUnit() != "ord-1-line-2" {
		t.Fatalf("work unit = %s", rma.Line().WorkUnit())
	}
}

func request(orderID string, lineNo int32, qty int64) usecases.ReturnRequest {
	return usecases.ReturnRequest{OrderID: orderID, LineNo: lineNo, Quantity: qty, SiteID: "SITE1", ReasonCode: "DAMAGED"}
}
