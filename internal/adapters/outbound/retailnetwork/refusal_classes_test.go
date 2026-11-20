package retailnetwork

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/views"
)

func problemBody(slug string) string {
	return `{"type":"` + slug + `","title":"t","status":0,"detail":"d of ` + slug + `"}`
}

// The submit route's refusals, as the network answers them: each row is one
// 4xx and how the gateway must hand it to the drain.
func TestSubmitRefusalIsReleasedOnlyWhenTheCalleeReleasesItsKey(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		retryable bool
		slug      string
	}{
		{"404 not-found: the advice is not consumed yet", http.StatusNotFound, problemBody("not-found"), true, "not-found"},
		{"409 concurrent-update", http.StatusConflict, problemBody("concurrent-update"), true, "concurrent-update"},
		{"409 illegal-transition", http.StatusConflict, problemBody("illegal-transition"), true, "illegal-transition"},
		{"409 vendor-suspended", http.StatusConflict, problemBody("vendor-suspended"), true, "vendor-suspended"},
		{"422 validation-failed is a function of the request here", http.StatusUnprocessableEntity, problemBody("validation-failed"), false, "validation-failed"},
		{"409 ack-window-closed is FINAL at the callee", http.StatusConflict, problemBody("ack-window-closed"), false, "ack-window-closed"},
		{"409 idempotency-key-reused", http.StatusConflict, problemBody("idempotency-key-reused"), false, "idempotency-key-reused"},
		{"400 idempotency-key-invalid", http.StatusBadRequest, problemBody("idempotency-key-invalid"), false, "idempotency-key-invalid"},
		{"an unknown slug is terminal", http.StatusConflict, problemBody("a-slug-nobody-classified"), false, "a-slug-nobody-classified"},
		{"a 4xx without a problem body is terminal", http.StatusNotFound, "", false, ""},
		{"a 4xx with a body that is not a problem is terminal", http.StatusConflict, "<html>conflict</html>", false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spy := newVendorAPISpy(t)
			spy.submit = func() (int, string) { return tc.status, tc.body }
			_, err := spy.gateway(t).SubmitReturnOutcome(context.Background(), outcomeFixture())
			var refusal ports.OutcomeRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want a ports.OutcomeRefusal", err)
			}
			if refusal.Released() != tc.retryable || refusal.ProblemSlug() != tc.slug {
				t.Fatalf("refusal = {slug %q retryable %t}, want {slug %q retryable %t}", refusal.ProblemSlug(), refusal.Released(), tc.slug, tc.retryable)
			}
			// The two classes are told apart by errors.Is too: a terminal
			// refusal is the verdict of the port, a released one is not.
			if got := errors.Is(err, ports.ErrOutcomeRefused); got == tc.retryable {
				t.Fatalf("errors.Is(ErrOutcomeRefused) = %t for retryable=%t", got, tc.retryable)
			}
		})
	}
}

// The released refusal keeps the text the operator reads today: status,
// slug and the callee's detail.
func TestReleasedRefusalTextNamesStatusSlugAndDetail(t *testing.T) {
	spy := newVendorAPISpy(t)
	spy.submit = func() (int, string) { return http.StatusNotFound, problemBody("not-found") }
	_, err := spy.gateway(t).SubmitReturnOutcome(context.Background(), outcomeFixture())
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"POST /vendor/return-outcomes", "404", "not-found", "d of not-found"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal text %q misses %q", err.Error(), want)
		}
	}
}

// A retry after a released refusal is the same request under the SAME key.
func TestReleasedRefusalIsRetriedUnderTheSameKey(t *testing.T) {
	spy := newVendorAPISpy(t)
	answers := []func() (int, string){
		func() (int, string) { return http.StatusNotFound, problemBody("not-found") },
		func() (int, string) { return http.StatusConflict, problemBody("concurrent-update") },
		func() (int, string) { return http.StatusAccepted, `{"transactionId":"tx-1"}` },
	}
	calls := 0
	spy.submit = func() (int, string) { calls++; return answers[calls-1]() }
	g := spy.gateway(t)
	var txID string
	var err error
	for i := 0; i < 3; i++ {
		txID, err = g.SubmitReturnOutcome(context.Background(), outcomeFixture())
	}
	if err != nil || txID != "tx-1" {
		t.Fatalf("third attempt = %q, %v", txID, err)
	}
	if len(spy.keys) != 3 || spy.keys[0] != "return-outcome-ret-1001" || spy.keys[1] != spy.keys[0] || spy.keys[2] != spy.keys[0] {
		t.Fatalf("keys = %v, want one key three times", spy.keys)
	}
	if spy.submits[0] != spy.submits[1] || spy.submits[1] != spy.submits[2] {
		t.Fatalf("the body changed between attempts: %v", spy.submits)
	}
}

// Only the submit route reads the table: a 404 on a read keeps the verdict it
// always had.
func TestOnlyTheSubmitRouteIsReadThroughTheTable(t *testing.T) {
	spy := newVendorAPISpy(t)
	spy.submit = func() (int, string) { return http.StatusAccepted, `{"transactionId":"tx-1"}` }
	c := spy.gateway(t).client
	err := c.refusal(http.MethodGet, "/vendor/transactions/tx-1", http.StatusNotFound, []byte(problemBody("not-found")))
	var refusal ports.OutcomeRefusal
	if errors.As(err, &refusal) && refusal.Released() {
		t.Fatalf("a read refusal was released: %v", err)
	}
	if !errors.Is(err, ports.ErrOutcomeRefused) {
		t.Fatalf("a read 404 changed class: %v", err)
	}
}

func TestRateLimitAndServerErrorsStayPlainRetriableErrors(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		spy := newVendorAPISpy(t)
		spy.submit = func() (int, string) { return status, problemBody("rate-limited") }
		_, err := spy.gateway(t).SubmitReturnOutcome(context.Background(), outcomeFixture())
		var refusal ports.OutcomeRefusal
		if err == nil || errors.Is(err, ports.ErrOutcomeRefused) || errors.As(err, &refusal) {
			t.Fatalf("%d = %v", status, err)
		}
	}
}

func TestRefusalTableClassifiesBySlugAndReadsUnknownAsTerminal(t *testing.T) {
	for slug, want := range map[string]bool{
		"not-found": true, "concurrent-update": true, "illegal-transition": true, "vendor-suspended": true,
		"validation-failed": false, "ack-window-closed": false, "": false, "nobody-knows-this": false,
	} {
		if got := returnOutcomeRefusals.retryable(slug); got != want {
			t.Errorf("retryable(%q) = %t, want %t", slug, got, want)
		}
	}
}

func outcomeFixture() views.ReturnOutcome {
	return views.ReturnOutcome{
		AuthorizationID: "ret-1001", NodeID: "n-1", ReceivedAt: time.Unix(1, 0),
		Lines: []views.ReturnOutcomeLine{{CatalogItemID: "ci-1", QtyUnsellable: 1}},
	}
}
