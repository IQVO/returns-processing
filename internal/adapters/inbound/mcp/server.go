// Package mcp is the inbound Model Context Protocol adapter: it exposes this
// bounded context to the AI ecosystem as a second driving adapter over the
// same application-layer read use cases the REST adapter uses. It is built on
// the official MCP Go SDK and served over Streamable HTTP only.
//
// The surface is READ-ONLY: every tool reads return data (get_return,
// list_returns_by_order, get_disposition_policy); authorizing or rejecting a
// return, opening a receipt, inspecting a line, overriding a disposition and
// declaring a policy stay on REST, and governance_test.go fails the build on a
// write-verb tool name. This package depends inward on the application layer
// and the domain only -- never on an outbound adapter or the REST adapter --
// and nothing else may depend on it (internal/architecture's
// TestMCPAdapterDependencyRule). The composition root (cmd/mcp) wires concrete
// repositories into the use cases. There is no auth of any kind (fleet-wide
// revert 2026-09-11; TestNoAuthMiddlewareReintroduced).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP initialize
// handshake.
const (
	serverName    = "returns-processing-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Reverse-logistics returns of the warehouse (read-only): see one return " +
	"authorization (RMA) with get_return, every RMA of one original order with list_returns_by_order " +
	"(an order with no returns answers an empty list), and the disposition policy table -- the grade x " +
	"product-class rules, the return window and the per-line quantity cap -- with get_disposition_policy. " +
	"An RMA is authorized against an order line that both allocated and shipped; its status runs " +
	"REQUESTED, AUTHORIZED or REJECTED, then RECEIVED and CLOSED. Nothing here changes data: authorizing " +
	"or rejecting a return, receiving and inspecting goods, overriding a disposition and declaring the " +
	"policy are REST operations of returns-processing."

// NewServer builds the MCP server for this bounded context with every tool
// registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
