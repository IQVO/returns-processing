package architecture

// TestOutboxRelaysPassAttemptCap (gap class "uncapped outbox relay",
// ecosystem-gaps 2026-10 gap 8, decision D12). Copied from the product repos
// (inventory-storage first, then every relay) and made conditional for the
// template: a service without a transactional outbox relay skips it.
//
// The fleet convention (inventory-storage internal/adapters/outbound/postgres
// outbox_relay.go): NewOutboxRelay(..., WithMaxAttempts(n)) excludes a row
// from future claims once it has failed n times; the row stays in
// outbox_events with attempts and last_error, the relay logs it at ERROR
// (id, topic, type, key), env OUTBOX_MAX_ATTEMPTS (default 20, 0 =
// unlimited) sets n, and an operator re-arms it with
// `UPDATE outbox_events SET attempts = 0 WHERE id = ...`.

import (
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"testing"
)

// callName returns the bare function name of a call: NewOutboxRelay for both
// NewOutboxRelay(...) and postgres.NewOutboxRelay(...).
func callName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// passesMaxAttempts reports whether one of the call's arguments is a
// WithMaxAttempts(...) option.
func passesMaxAttempts(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if inner, ok := arg.(*ast.CallExpr); ok && callName(inner) == "WithMaxAttempts" {
			return true
		}
	}
	return false
}

// outboxRelayCapViolations parses one Go file and returns how many
// NewOutboxRelay calls it makes plus one violation per call that does not
// pass WithMaxAttempts inline.
func outboxRelayCapViolations(t *testing.T, path string) (calls int, violations []string) {
	t.Helper()
	fset, f := parseFile(t, path)
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || callName(call) != "NewOutboxRelay" {
			return true
		}
		calls++
		if !passesMaxAttempts(call) {
			violations = append(violations, relayCapMessage(fset, call))
		}
		return true
	})
	return calls, violations
}

func relayCapMessage(fset *token.FileSet, call *ast.CallExpr) string {
	return fmt.Sprintf("%s: NewOutboxRelay without WithMaxAttempts: one poison row would block the relay forever. "+
		"FIX: pass postgres.WithMaxAttempts(outboxMaxAttempts(logger)) (env OUTBOX_MAX_ATTEMPTS, default 20, "+
		"0 = unlimited; record the per-key ordering trade-off in an ADR)",
		fset.Position(call.Pos()))
}

// relayDeclarations reports whether one Go file declares the relay
// constructor NewOutboxRelay and the WithMaxAttempts option (package-level
// functions, no receiver).
func relayDeclarations(t *testing.T, path string) (declaresRelay, declaresCap bool) {
	t.Helper()
	_, f := parseFile(t, path)
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		switch fn.Name.Name {
		case "NewOutboxRelay":
			declaresRelay = true
		case "WithMaxAttempts":
			declaresCap = true
		}
	}
	return declaresRelay, declaresCap
}

// relayPackageState scans the non-test sources under internal/ for the
// relay constructor and its cap option.
func relayPackageState(t *testing.T) (declaresRelay, declaresCap bool) {
	t.Helper()
	for _, path := range goFiles(t, "internal") {
		r, c := relayDeclarations(t, path)
		declaresRelay = declaresRelay || r
		declaresCap = declaresCap || c
	}
	return declaresRelay, declaresCap
}

// TestOutboxRelaysPassAttemptCap enforces the cap: every non-test file under
// cmd/ that builds an outbox relay passes WithMaxAttempts, so the cap is a
// deliberate setting at each composition root and never the unlimited
// default by omission. A service with no relay (no NewOutboxRelay declared
// under internal/ and none called under cmd/) skips; a relay package
// without WithMaxAttempts, or a relay that no cmd/ builds, fails.
func TestOutboxRelaysPassAttemptCap(t *testing.T) {
	declaresRelay, declaresCap := relayPackageState(t)
	total := 0
	for _, path := range goFiles(t, "cmd") {
		calls, violations := outboxRelayCapViolations(t, path)
		total += calls
		for _, v := range violations {
			t.Error(v)
		}
	}
	if !declaresRelay && total == 0 {
		t.Skip("no outbox relay in this service (no NewOutboxRelay under internal/ or cmd/): nothing to check")
	}
	if declaresRelay && !declaresCap {
		t.Error("internal/ declares NewOutboxRelay but no WithMaxAttempts option: a poison row blocks the relay forever. " +
			"FIX: add WithMaxAttempts(n) with inventory-storage's semantics (exclude a row from claims once attempts >= n, " +
			"keep it with attempts/last_error, log it at ERROR with id, topic, type and key) plus its integration test " +
			"(a poison row, then a good row, which publishes)")
	}
	if total == 0 {
		t.Fatal("internal/ declares NewOutboxRelay but no cmd/ builds it; the relay is never started or the sensor is looking in the wrong place")
	}
}

// TestOutboxRelayCapSensorFailsOnBadFixtures proves the sensor can fail and
// accepts both call shapes that pass the option.
func TestOutboxRelayCapSensorFailsOnBadFixtures(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantBad int
	}{
		{"no options", "package x\n\nfunc f() { _ = postgres.NewOutboxRelay(pool, sink) }\n", 1},
		{"other options only", "package x\n\nfunc f() { _ = postgres.NewOutboxRelay(pool, sink, postgres.WithLogger(l), postgres.WithInterval(d)) }\n", 1},
		{"capped", "package x\n\nfunc f() { _ = postgres.NewOutboxRelay(pool, sink, postgres.WithMaxAttempts(n)) }\n", 0},
		{"capped, unqualified", "package x\n\nfunc f() { _ = NewOutboxRelay(pool, sink, WithLogger(l), WithMaxAttempts(20)) }\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeRelayFixture(t, tc.src)
			calls, got := outboxRelayCapViolations(t, p)
			if calls != 1 || len(got) != tc.wantBad {
				t.Fatalf("calls=%d violations=%v, want 1 call and %d violation(s)", calls, got, tc.wantBad)
			}
		})
	}
}

// TestOutboxRelayDeclarationDetector proves the relay-package check sees the
// constructor and the option, and ignores methods of the same name.
func TestOutboxRelayDeclarationDetector(t *testing.T) {
	cases := []struct {
		name               string
		src                string
		wantRelay, wantCap bool
	}{
		{"relay without cap", "package postgres\n\nfunc NewOutboxRelay() *R { return nil }\n", true, false},
		{"relay with cap", "package postgres\n\nfunc NewOutboxRelay(opts ...Opt) *R { return nil }\n\nfunc WithMaxAttempts(n int) Opt { return nil }\n", true, true},
		{"method is not the constructor", "package postgres\n\nfunc (b *B) NewOutboxRelay() {}\n", false, false},
		{"unrelated", "package postgres\n\nfunc NewRepo() {}\n", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, c := relayDeclarations(t, writeRelayFixture(t, tc.src))
			if r != tc.wantRelay || c != tc.wantCap {
				t.Fatalf("relay=%v cap=%v, want relay=%v cap=%v", r, c, tc.wantRelay, tc.wantCap)
			}
		})
	}
}

func writeRelayFixture(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fixture.go")
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}
