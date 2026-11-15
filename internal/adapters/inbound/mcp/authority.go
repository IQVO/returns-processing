package mcp

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Authority levels (Agentic Standard v2, AS-1). Every registered tool
// declares exactly one of these in its Meta under authorityMetaKey; the fleet
// rule is in warehouse-docs agents/fleet/agent-authority.md. This context's
// MCP surface is read-only (docs/docs/mcp/tools.md, pinned by
// governance_test.go), so every tool here is L1: a pure read of this
// context's own state that changes nothing. L2..L4 are declared but unused
// on purpose: the fleet's pinned declaration shape (the template's
// mcp_authority_fitness sensor) is the SAME const ladder in every repo.
const (
	// authorityMetaKey is the tools/list _meta key carrying a tool's level.
	authorityMetaKey = "com.warehouse/authority"
	// authorityL1 reads facts and changes nothing.
	authorityL1 = "L1"
	// authorityL2 is a pure recommendation: computes a proposal, changes nothing.
	authorityL2 = "L2" //nolint:unused // fleet-pinned shape; no L2 tool on this read-only surface
	// authorityL3 mutates state; the caller is accountable.
	authorityL3 = "L3" //nolint:unused // fleet-pinned shape; no L3 tool on this read-only surface
	// authorityL4 mutates state inside an envelope an ADR of this context
	// grants; no such ADR exists here.
	authorityL4 = "L4" //nolint:unused // fleet-pinned shape; no L4 tool on this read-only surface
)

// authorityMeta is the Meta a tool registers to declare its authority level.
func authorityMeta(level string) mcp.Meta {
	// Every tool here is L1; the parameter keeps the fleet's pinned shape.
	return mcp.Meta{authorityMetaKey: level}
}
