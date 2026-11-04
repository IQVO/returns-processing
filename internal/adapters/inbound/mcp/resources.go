package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mimeTypeJSON is the MIME type of every resource this server serves.
const mimeTypeJSON = "application/json"

// returnURITemplate is the ONE resource this server offers (Agentic Standard
// v2, AS-3): a scoped read model this context owns, not a dump of a table.
// It answers "where does this one return authorization stand?" for ONE rma
// id, built from the same GetReturn read use case the get_return tool and
// GET /returns/{rmaId} use. Bounded by construction: one RMA, the same
// twelve fields the tool returns, JSON text.
const returnURITemplate = "warehouse://returns-processing/returns/{rmaId}"

// registerResources adds the resource surface (read-only by nature, L1; the
// charter keeps resources context data an agent can load without a tool
// call). No resource ever writes: Deps holds read use cases only.
func (d Deps) registerResources(server *mcp.Server) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: returnURITemplate,
		Name:        "return authorization",
		Description: "One return authorization (RMA) by id: the order line it was authorized against, the work unit the goods shipped in, sku, quantity, site, reason code, status, rejection code, return-window end and aggregate version. An unknown id is an error.",
		MIMEType:    mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		id, err := parseReturnURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		v, err := d.GetReturn.Execute(ctx, id)
		if err != nil {
			return nil, mapError(err, "rma-not-found", "return authorization not found")
		}
		return resourceResult(req.Params.URI, toReturnView(v))
	})
}

// resourceResult marshals one read model into the resource's text contents.
func resourceResult(uri string, view any) (*mcp.ReadResourceResult, error) {
	body, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      uri,
			MIMEType: mimeTypeJSON,
			Text:     string(body),
		}},
	}, nil
}

// parseReturnURI extracts the rmaId from a
// warehouse://returns-processing/returns/{rmaId} URI. The id is
// model-supplied and therefore untrusted: it faces the same shape rule as
// the get_return tool's rma_id argument (1..64 characters without white
// space, a slash or a NUL byte), and a malformed URI is rejected rather
// than silently defaulted.
func parseReturnURI(uri string) (string, error) {
	const scheme = "warehouse://returns-processing/returns/"
	rest, ok := strings.CutPrefix(uri, scheme)
	if !ok {
		return "", fmt.Errorf("resource uri %q must start with %q", uri, scheme)
	}
	if rest == "" || strings.Contains(rest, "/") {
		return "", fmt.Errorf("resource uri %q must carry exactly one non-empty rmaId after %q", uri, scheme)
	}
	if len(rest) > maxIDLen || strings.ContainsAny(rest, " \t\x00") {
		return "", fmt.Errorf("resource uri %q rmaId must be 1..64 characters without white space, a slash or a NUL byte", uri)
	}
	return rest, nil
}
