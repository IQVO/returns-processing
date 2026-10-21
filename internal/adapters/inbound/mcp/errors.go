package mcp

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/claudioed/returns-processing/internal/application/ports"
)

// toolError builds a tool-level error "<slug>: <detail>". Returned from a
// handler it becomes an isError tool result, never a transport failure. The
// slugs are the last segments of the REST adapter's RFC 7807 "type" URIs
// (internal/adapters/inbound/http/errors.go), so a client sees one error
// vocabulary over both surfaces; only the errors a READ can raise are used.
func toolError(slug, detail string) error {
	return fmt.Errorf("%s: %s", slug, detail)
}

// mapError turns a use-case/repository error into the tool error the model
// sees. A miss of the one shared not-found sentinel is typed by the CALLER
// (which resource was addressed: rma-not-found, policy-not-found); anything
// else is logged and reported generically so infrastructure details (DSNs,
// SQL) never reach the model.
func mapError(err error, notFoundSlug, notFoundDetail string) error {
	if errors.Is(err, ports.ErrNotFound) {
		if notFoundDetail == "" {
			notFoundDetail = err.Error()
		}
		return toolError(notFoundSlug, notFoundDetail)
	}
	slog.Error("mcp tool failed with an unexpected error", "error", err)
	return toolError("internal-error", "an unexpected internal error occurred")
}
