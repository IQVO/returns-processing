package mcp_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// update regenerates testdata/tool_registry.golden.json:
//
//	go test ./internal/adapters/inbound/mcp -run TestToolRegistryGolden -update
var update = flag.Bool("update", false, "rewrite the MCP tool registry golden file")

// wantTools is the curated, READ-ONLY tool set: exactly the read use cases of
// the returns surface. get_value_recovery_report joins when the reports wave
// lands (its use case does not exist yet).
var wantTools = []string{
	"get_disposition_policy", "get_return", "list_returns_by_order",
}

// maxTools is the tool budget. Raising it is a deliberate, reviewed act (an
// ADR amendment): TestToolSurface still pins the exact curated set.
const maxTools = 3

// writeVerbs are the verbs of this service's write use cases (authorize,
// reject, open a receipt, inspect a line, override, declare a policy) and the
// generic mutators. A tool whose name contains one of them as a word is a
// write tool, which the read-only MCP surface must never expose: writes go
// through REST, the transactional outbox and the version/idempotency guards.
var writeVerbs = []string{
	"authorize", "reject", "open", "inspect", "override", "declare",
	"stage", "restock", "receive", "create", "update", "delete", "set",
	"put", "post", "apply", "save", "cancel", "complete",
}

// writeVerbViolations returns one message per tool name carrying a write verb
// as one of its snake_case words.
func writeVerbViolations(names []string) []string {
	var out []string
	for _, name := range names {
		for _, word := range strings.Split(name, "_") {
			for _, verb := range writeVerbs {
				if word == verb {
					out = append(out, name+": contains the write verb "+verb+" (the MCP surface is read-only)")
				}
			}
		}
	}
	return out
}

// TestToolSurface is the mechanical gate that pins the surface read-only: the
// advertised tool set is exactly the curated one, within the budget,
// snake_case verb_noun with NO write verb, annotated read-only (and not
// destructive), described, and every argument is snake_case and documented.
func TestToolSurface(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) > maxTools {
		t.Fatalf("advertised %d tools, over the budget of %d", len(res.Tools), maxTools)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		checkToolMetadata(t, tool)
		checkToolArguments(t, tool)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("advertised tools %v, want exactly %v", names, wantTools)
	}
	for _, v := range writeVerbViolations(names) {
		t.Error(v)
	}
}

// The write-verb sensor must actually be able to fail.
func TestWriteVerbSensorFailsOnWriteNames(t *testing.T) {
	for _, verb := range writeVerbs {
		for _, name := range []string{verb + "_return", "bulk_" + verb + "_returns"} {
			if len(writeVerbViolations([]string{name})) == 0 {
				t.Errorf("write tool name %q was not flagged", name)
			}
		}
	}
	if got := writeVerbViolations(wantTools); len(got) != 0 {
		t.Errorf("read tool names were flagged: %v", got)
	}
	// Substrings of a word are not verbs: "dataset" is not "set", "declared"
	// is not "declare", "orders" is not "open"-ed.
	if got := writeVerbViolations([]string{"get_dataset", "list_declared_policies", "get_orders_total"}); len(got) != 0 {
		t.Errorf("false positive on non-verb words: %v", got)
	}
}

var (
	toolNaming = regexp.MustCompile(`^[a-z]+(_[a-z]+)+$`)
	argNaming  = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)
)

func checkToolMetadata(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	if !toolNaming.MatchString(tool.Name) {
		t.Errorf("tool %q is not snake_case verb_noun", tool.Name)
	}
	if tool.Description == "" {
		t.Errorf("tool %q has no description", tool.Name)
	}
	if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
		t.Errorf("tool %q is not annotated read-only: %+v", tool.Name, tool.Annotations)
		return
	}
	if tool.Annotations.DestructiveHint != nil && *tool.Annotations.DestructiveHint {
		t.Errorf("tool %q is annotated destructive", tool.Name)
	}
}

func checkToolArguments(t *testing.T, tool *sdk.Tool) {
	t.Helper()
	schema, isMap := tool.InputSchema.(map[string]any)
	if !isMap {
		t.Errorf("tool %q input schema = %T", tool.Name, tool.InputSchema)
		return
	}
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		t.Errorf("tool %q has no arguments in its schema", tool.Name)
	}
	for arg, p := range props {
		if !argNaming.MatchString(arg) {
			t.Errorf("tool %q argument %q is not snake_case", tool.Name, arg)
		}
		if d, _ := p.(map[string]any)["description"].(string); d == "" {
			t.Errorf("tool %q argument %q has no description", tool.Name, arg)
		}
	}
}

// TestToolRegistryGolden pins the whole advertised registry (names,
// descriptions, annotations, input AND output schemas) byte for byte, so any
// change to what an MCP client sees shows up as a reviewed golden diff.
func TestToolRegistryGolden(t *testing.T) {
	h := newHarness(t)
	res, err := h.session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	sort.Slice(res.Tools, func(i, j int) bool { return res.Tools[i].Name < res.Tools[j].Name })
	got, err := json.MarshalIndent(res.Tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')

	path := filepath.Join("testdata", "tool_registry.golden.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("the MCP tool registry drifted from %s; review the change and re-run with -update.\ngot:\n%s", path, got)
	}
}
