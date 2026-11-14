package usecases_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// recordingMetrics counts the ports.AuthorizationMetrics calls of a use case.
type recordingMetrics struct {
	mu         sync.Mutex
	authorized int
	rejected   []string
}

var _ ports.AuthorizationMetrics = (*recordingMetrics)(nil)

func (m *recordingMetrics) ReturnAuthorized(context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authorized++
}

func (m *recordingMetrics) ReturnRejected(_ context.Context, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rejected = append(m.rejected, reason)
}

func TestAuthorizeReturn_CountsAnAuthorization(t *testing.T) {
	f := newFixture(t)
	f.declare(t)
	f.seed("ord-1", 2, "sku-1", "pkg-1")
	m := &recordingMetrics{}
	uc := f.authorizeUC()
	uc.Metrics = m
	if _, err := uc.Execute(context.Background(), request("ord-1", 2, 1)); err != nil {
		t.Fatal(err)
	}
	if m.authorized != 1 || len(m.rejected) != 0 {
		t.Fatalf("authorized=%d rejected=%v, want 1 / none", m.authorized, m.rejected)
	}
}

// Every refusal is counted exactly once, with the problem slug the REST
// adapter answers with as its reason.
func TestAuthorizeReturn_CountsEachRefusalOnceWithItsProblemSlug(t *testing.T) {
	cases := []struct {
		name string
		prep func(f fixture) usecases.ReturnRequest
		want string
	}{
		{"line not allocated", func(f fixture) usecases.ReturnRequest {
			f.declare(t)
			return request("ord-1", 2, 1)
		}, "return-not-authorizable"},
		{"quantity over the cap", func(f fixture) usecases.ReturnRequest {
			f.declare(t)
			f.seed("ord-1", 2, "sku-1", "pkg-1")
			return request("ord-1", 2, 6)
		}, "return-not-authorizable"},
		{"open rma exists", func(f fixture) usecases.ReturnRequest {
			f.authorize(t, "ord-1", 2, 1)
			return request("ord-1", 2, 1)
		}, "open-rma-exists"},
		{"quantity below one", func(f fixture) usecases.ReturnRequest {
			f.declare(t)
			f.seed("ord-1", 2, "sku-1", "pkg-1")
			return request("ord-1", 2, 0)
		}, "invalid-quantity"},
		{"blank reason code", func(f fixture) usecases.ReturnRequest {
			f.declare(t)
			f.seed("ord-1", 2, "sku-1", "pkg-1")
			r := request("ord-1", 2, 1)
			r.ReasonCode = "  "
			return r
		}, "malformed-request-body"},
		{"no policy declared", func(f fixture) usecases.ReturnRequest {
			f.seed("ord-1", 2, "sku-1", "pkg-1")
			return request("ord-1", 2, 1)
		}, "return-not-authorizable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			in := tc.prep(f)
			m := &recordingMetrics{}
			uc := f.authorizeUC()
			uc.Metrics = m
			if _, err := uc.Execute(context.Background(), in); err == nil {
				t.Fatal("the authorization must be refused")
			}
			if m.authorized != 0 || !reflect.DeepEqual(m.rejected, []string{tc.want}) {
				t.Fatalf("authorized=%d rejected=%v, want 0 / [%s]", m.authorized, m.rejected, tc.want)
			}
		})
	}
}

func TestAuthorizeRejectionReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{usecases.ErrOrderLineNotAllocated, "return-not-authorizable"},
		{returnauthorization.ErrLineNotManifested, "return-not-authorizable"},
		{returnauthorization.ErrWindowExpired, "return-not-authorizable"},
		{returnauthorization.ErrQuantityExceedsPolicy, "return-not-authorizable"},
		{ports.ErrNotFound, "return-not-authorizable"},
		{returnauthorization.ErrOpenRMAExists, "open-rma-exists"},
		{ports.ErrConcurrentModification, "concurrent-modification"},
		{shared.ErrQuantityBelowOne, "invalid-quantity"},
		{shared.ErrBlankID, "malformed-request-body"},
		{returnauthorization.ErrBlankReasonCode, "malformed-request-body"},
		{errors.New("boom"), "internal-error"},
	}
	for _, tc := range cases {
		if got := usecases.AuthorizeRejectionReason(tc.err); got != tc.want {
			t.Errorf("AuthorizeRejectionReason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func TestAuthorizeReturn_WithoutMetricsStillWorks(t *testing.T) {
	f := newFixture(t)
	f.authorize(t, "ord-1", 2, 1) // the fixture's use case carries no Metrics
}
