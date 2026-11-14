package http

import (
	"errors"
	"testing"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/application/usecases"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// The authorization counter's `reason` is documented as "the problem slug the
// REST adapter answers with". The use case owns its own table (the application
// layer cannot import this adapter), so this test pins the two vocabularies
// together: for every error AuthorizeReturn can return, the reason equals the
// slug of the problem the handler writes.
func TestAuthorizeRejectionReasonMatchesTheProblemSlugOfTheHandler(t *testing.T) {
	cases := []error{
		usecases.ErrOrderLineNotAllocated,
		returnauthorization.ErrLineNotManifested,
		returnauthorization.ErrWindowExpired,
		returnauthorization.ErrQuantityExceedsPolicy,
		returnauthorization.ErrOpenRMAExists,
		returnauthorization.ErrBlankReasonCode,
		ports.ErrConcurrentModification,
		shared.ErrQuantityBelowOne,
		shared.ErrLineNoBelowOne,
		shared.ErrBlankID,
		shared.ErrNULInID,
		errors.New("database unreachable"),
	}
	for _, err := range cases {
		if got, want := usecases.AuthorizeRejectionReason(err), problemFor(err).slug; got != want {
			t.Errorf("AuthorizeRejectionReason(%v) = %q, but the handler answers %q", err, got, want)
		}
	}
	// A missing policy is the one case handleAuthorizeReturn special-cases:
	// ports.ErrNotFound is the 409 return-not-authorizable.
	if got, want := usecases.AuthorizeRejectionReason(ports.ErrNotFound), notAuthorizable.slug; got != want {
		t.Errorf("AuthorizeRejectionReason(ErrNotFound) = %q, want %q", got, want)
	}
}
