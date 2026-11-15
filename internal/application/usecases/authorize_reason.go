package usecases

import (
	"errors"

	"github.com/claudioed/returns-processing/internal/application/ports"
	"github.com/claudioed/returns-processing/internal/domain/returnauthorization"
	"github.com/claudioed/returns-processing/internal/domain/shared"
)

// The reasons of a rejected authorization: the problem slugs the REST adapter
// answers with (apis/openapi.yaml). The application layer cannot import that
// adapter, so it owns this table; the adapter's
// TestAuthorizeRejectionReasonMatchesTheProblemSlugOfTheHandler pins the two
// vocabularies together.
const (
	reasonNotAuthorizable  = "return-not-authorizable"
	reasonOpenRMAExists    = "open-rma-exists"
	reasonConcurrentMod    = "concurrent-modification"
	reasonInvalidQuantity  = "invalid-quantity"
	reasonMalformedRequest = "malformed-request-body"
	reasonInternalError    = "internal-error"
)

// authorizeReasons maps every typed error AuthorizeReturn can return to its
// reason, in match order. An unlisted error is an internal-error.
var authorizeReasons = []struct {
	err    error
	reason string
}{
	{ErrOrderLineNotAllocated, reasonNotAuthorizable},
	{returnauthorization.ErrLineNotManifested, reasonNotAuthorizable},
	{returnauthorization.ErrWindowExpired, reasonNotAuthorizable},
	{returnauthorization.ErrQuantityExceedsPolicy, reasonNotAuthorizable},
	// A miss is the missing disposition policy, the one port read first.
	{ports.ErrNotFound, reasonNotAuthorizable},
	{returnauthorization.ErrOpenRMAExists, reasonOpenRMAExists},
	{ports.ErrConcurrentModification, reasonConcurrentMod},
	{shared.ErrQuantityBelowOne, reasonInvalidQuantity},
	{shared.ErrLineNoBelowOne, reasonMalformedRequest},
	{shared.ErrBlankID, reasonMalformedRequest},
	{shared.ErrNULInID, reasonMalformedRequest},
	{returnauthorization.ErrBlankReasonCode, reasonMalformedRequest},
}

// AuthorizeRejectionReason is the bounded `reason` attribute value of a
// rejected authorization: the problem slug the REST adapter answers with for
// err.
func AuthorizeRejectionReason(err error) string {
	for _, entry := range authorizeReasons {
		if errors.Is(err, entry.err) {
			return entry.reason
		}
	}
	return reasonInternalError
}
