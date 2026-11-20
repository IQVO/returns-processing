package retailnetwork

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// returnOutcomeRefusals is held against the byte-pinned copy of the callee's
// OpenAPI (testdata/vendor-connectivity.openapi.yaml, a pin of contracts/pinned.json,
// refreshed by copying, never edited). What the pin can prove:
//   - the Idempotency-Key text of POST /vendor/return-outcomes lists the problem types
//     the callee releases (nothing stored, the key free) and names the ones it replays;
//     the 4xx responses of the operation name the rest. Every slug named is classified
//     here, every released one is mapped retryable (but for the documented exceptions
//     below), and nothing the callee stores is mapped retryable.
//   - it does NOT carry the callee's class table (that is its ADR 0015): a slug no text
//     names is held by the table's own entries.
const pinnedCalleeSpec = "testdata/vendor-connectivity.openapi.yaml"

// releasedButTerminalHere are the slugs the callee releases that this caller still
// reads as terminal, each with its reason. A slug listed here that the pin no longer
// releases, or that the table maps retryable, fails the audit: the exception is stale.
var releasedButTerminalHere = map[string]string{
	"validation-failed": "mixed slug at the callee (a malformed body and a state-dependent check share it, ADR 0015 judgement 1); on this route it is a function of our request, which is the same on every retry, so a retry cannot pass",
}

// notSlugs are kebab-case words of the spec prose that are not problem types.
var notSlugs = map[string]bool{"vendor-connectivity": true}

var codeSpan = regexp.MustCompile("`([a-z]+(?:-[a-z]+)+)`")

func slugsIn(text string) []string {
	seen := map[string]bool{}
	for _, m := range codeSpan.FindAllStringSubmatch(text, -1) {
		if !notSlugs[m[1]] {
			seen[m[1]] = true
		}
	}
	return sortedKeys(seen)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func words(s string) string { return strings.Join(strings.Fields(s), " ") }

// specDoc is the part of an OpenAPI document this test reads, as plain maps.
type specDoc map[string]any

func child(m any, key string) map[string]any {
	c, _ := m.(map[string]any)[key].(map[string]any)
	return c
}

// resolve follows a local $ref ("#/components/...").
func (d specDoc) resolve(node map[string]any) map[string]any {
	ref, ok := node["$ref"].(string)
	if !ok {
		return node
	}
	cur := any(map[string]any(d))
	for _, part := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		cur = child(cur, part)
	}
	return cur.(map[string]any)
}

func loadPinnedSpec(t *testing.T) specDoc {
	t.Helper()
	raw, err := os.ReadFile(pinnedCalleeSpec)
	if err != nil {
		t.Fatalf("the pinned callee spec: %v", err)
	}
	var d map[string]any // not specDoc: yaml.v3 would decode every nested map into the named type
	if err := yaml.Unmarshal(raw, &d); err != nil {
		t.Fatalf("the pinned callee spec does not parse: %v", err)
	}
	return specDoc(d)
}

// specReading is what the pin says about the submit route: released are the problem
// types the Idempotency-Key text lists as not stored; named is every problem type the
// text or a 4xx response of the operation names.
type specReading struct{ released, named []string }

const releaseAnchor = "is a new request:"

// tableStatuses are the statuses refusal() reads through the table.
var tableStatuses = map[string]bool{"400": true, "404": true, "409": true, "422": true}

func readSubmitOperation(t *testing.T, d specDoc) specReading {
	t.Helper()
	op := child(child(child(map[string]any(d), "paths"), returnOutcomesPath), "post")
	if op == nil {
		t.Fatalf("the pinned spec has no POST %s", returnOutcomesPath)
	}
	var keyText string
	for _, p := range op["parameters"].([]any) {
		if param := d.resolve(p.(map[string]any)); param["name"] == "Idempotency-Key" {
			keyText = words(param["description"].(string))
		}
	}
	if j := strings.Index(keyText, "5xx"); j >= 0 {
		keyText = keyText[:j]
	}
	i := strings.Index(keyText, releaseAnchor)
	if i < 0 {
		t.Fatalf("the Idempotency-Key text no longer has %q: the callee reworded how it lists the released refusals; read the new text and update the anchor and the table together", releaseAnchor)
	}
	// keyText stops at its 5xx clause: a 5xx and a 429 never reach the table (they are
	// plain errors, retried), only the four statuses of refusal() do.
	list := keyText[i+len(releaseAnchor):]
	named := map[string]bool{}
	for _, s := range slugsIn(keyText) {
		named[s] = true
	}
	for status, resp := range child(op, "responses") {
		if !tableStatuses[status] {
			continue
		}
		for _, s := range slugsIn(words(d.resolve(resp.(map[string]any))["description"].(string))) {
			named[s] = true
		}
	}
	return specReading{released: slugsIn(list), named: sortedKeys(named)}
}

// auditTable returns what is wrong between a table and the spec's reading.
func auditTable(table refusalTable, r specReading, exceptions map[string]string) []string {
	var problems []string
	released := map[string]bool{}
	for _, s := range r.released {
		released[s] = true
		_, excepted := exceptions[s]
		switch {
		case excepted && table[s] == classRetryable:
			problems = append(problems, "the exception for "+s+" is stale: the table maps it retryable")
		case !excepted && table[s] != classRetryable:
			problems = append(problems, "spec releases "+s+" under the key but the table does not map it retryable")
		}
	}
	for s := range exceptions {
		if !released[s] {
			problems = append(problems, "the exception for "+s+" is stale: the pin no longer releases it")
		}
	}
	for _, s := range r.named {
		if table[s] == classUnset {
			problems = append(problems, "spec names "+s+" and the table classifies it neither retryable nor terminal")
		}
	}
	for s, class := range table {
		if class == classRetryable && !released[s] {
			problems = append(problems, "the table retries "+s+" but the spec does not release it: a stored refusal would only be replayed")
		}
	}
	sort.Strings(problems)
	return problems
}

func TestEveryProblemTypeThePinnedSpecNamesIsClassifiedAndEveryReleasedOneRetries(t *testing.T) {
	r := readSubmitOperation(t, loadPinnedSpec(t))
	if len(r.released) < 8 || len(r.named) <= len(r.released) {
		t.Fatalf("read %d released and %d named problem types from the pin; the extraction is broken: %+v", len(r.released), len(r.named), r)
	}
	if problems := auditTable(returnOutcomeRefusals, r, releasedButTerminalHere); len(problems) > 0 {
		t.Fatalf("returnOutcomeRefusals drifted from %s:\n  %s", pinnedCalleeSpec, strings.Join(problems, "\n  "))
	}
}

// The slugs the issue names are all read from the pin as released: the test above
// would pass vacuously if the extraction lost them.
func TestThePinReleasesTheRefusalsThisCallerRetries(t *testing.T) {
	released := map[string]bool{}
	for _, s := range readSubmitOperation(t, loadPinnedSpec(t)).released {
		released[s] = true
	}
	for _, want := range []string{"not-found", "concurrent-update", "illegal-transition", "vendor-suspended"} {
		if !released[want] {
			t.Errorf("the pin does not release %s", want)
		}
	}
	for _, final := range []string{"partial-acknowledgement", "ack-window-closed", "idempotency-key-reused"} {
		if released[final] {
			t.Errorf("the pin releases %s: it is stored and replayed, a retry cannot pass", final)
		}
	}
}

// The audit itself must fail when the table drifts: drop an entry, flip a released
// slug to terminal, map a stored one retryable, or keep a stale exception.
func TestTheAuditFailsWhenTheTableDrifts(t *testing.T) {
	r := readSubmitOperation(t, loadPinnedSpec(t))
	drifted := refusalTable{}
	for k, v := range returnOutcomeRefusals {
		drifted[k] = v
	}
	delete(drifted, "ack-window-closed")                // a documented slug nobody classified
	drifted["vendor-suspended"] = classTerminal         // released at the callee, terminal here
	drifted["partial-acknowledgement"] = classRetryable // stored at the callee, retried here
	drifted["validation-failed"] = classRetryable       // the exception is now stale
	got := strings.Join(auditTable(drifted, r, releasedButTerminalHere), "\n")
	for _, want := range []string{
		"spec names ack-window-closed",
		"spec releases vendor-suspended under the key but the table does not map it retryable",
		"the table retries partial-acknowledgement",
		"the exception for validation-failed is stale",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the audit missed %q:\n%s", want, got)
		}
	}
	if clean := auditTable(returnOutcomeRefusals, r, releasedButTerminalHere); len(clean) != 0 {
		t.Errorf("the shipped table is not clean: %v", clean)
	}
}
