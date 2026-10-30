package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"

	archgo "github.com/arch-go/arch-go/api"
	"github.com/arch-go/arch-go/api/configuration"
)

// TestA2AAdapterDependencyRule encodes ADR-0008: the A2A inbound adapter is
// additive, never load-bearing. It may depend only on the application and
// domain layers (never on outbound adapters, never on cmd), and nothing else
// in this codebase may depend on it; cmd/a2a wires it up and nothing else
// touches it.
func TestA2AAdapterDependencyRule(t *testing.T) {
	moduleInfo := configuration.Load(modulePath)

	t.Run("a2a adapter depends only on application and domain", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.adapters.inbound.a2a.**",
					ShouldOnlyDependsOn: &configuration.Dependencies{
						Internal: []string{
							"**.internal.adapters.inbound.a2a.**",
							"**.internal.application.**",
							"**.internal.domain.**",
						},
					},
				},
			},
		})

		assertArchGoPasses(t, result)
	})

	t.Run("nothing else depends on the a2a adapter", func(t *testing.T) {
		result := archgo.CheckArchitecture(moduleInfo, configuration.Config{
			DependenciesRules: []*configuration.DependenciesRule{
				{
					Package: "**.internal.**",
					ShouldNotDependsOn: &configuration.Dependencies{
						Internal: []string{"**.internal.adapters.inbound.a2a.**"},
					},
				},
			},
		})

		assertArchGoPasses(t, result)
	})
}

// a2aWatched is the READ-ONLY ALLOWLIST of the A2A surface, keyed by the
// import-path suffix of a watched package: the only identifiers the A2A
// adapter and cmd/a2a (non-test sources) may use from it. This context's MCP
// charter is entirely read-only (docs/docs/mcp/tools.md: get_return, list_returns_by_order,
// get_disposition_policy), so the A2A agent derived from it is read-only: it
// holds exactly the three read use cases, the not-found sentinel its problem
// mapping needs, and the Postgres read plumbing -- and no write use case, no
// unit of work, no event publisher, no outbox. An ALLOWLIST (not a denylist
// of write names) means a write added under any name fails here.
var a2aWatched = map[string]map[string]bool{
	"/internal/application/usecases": {"GetReturn": true, "ListReturnsByOrder": true, "GetDispositionPolicy": true},
	"/internal/application/ports":    {"ErrNotFound": true},
	"/internal/adapters/outbound/postgres": {
		"RunMigrations": true, "NewPool": true, "NewRMAs": true, "NewPolicies": true, "EnvMigrationsDatabaseURL": true,
	},
}

// a2aForbiddenImports are import-path fragments the A2A surface never has:
// an outbound A2A client (a delegation is a call to a sibling; this agent only
// answers) and the Kafka adapter (a read-only agent publishes no fact).
var a2aForbiddenImports = map[string]string{
	"a2a-go/v2/a2aclient":               "an outbound A2A client (this agent only answers; ADR 0008)",
	"/internal/adapters/outbound/kafka": "the Kafka adapter (a read-only agent publishes and consumes no fact)",
}

// a2aViolations parses one Go source (comments are not code, so prose that
// merely mentions a write verb never trips this) and reports every use of a
// watched package outside its allowlist and every forbidden import.
func a2aViolations(filename string, src any) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return nil, err
	}
	out, watchedByName, err := a2aImportFindings(fset, f)
	if err != nil {
		return nil, err
	}
	return append(out, a2aUseFindings(fset, f, watchedByName)...), nil
}

// a2aImportFindings reports the forbidden imports of one file and returns the
// allowlist of every watched package it imports, keyed by local import name.
func a2aImportFindings(fset *token.FileSet, f *ast.File) ([]string, map[string]map[string]bool, error) {
	var out []string
	watchedByName := map[string]map[string]bool{}
	for _, imp := range f.Imports {
		importPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, nil, err
		}
		for fragment, why := range a2aForbiddenImports {
			if strings.Contains(importPath, fragment) {
				out = append(out, fmt.Sprintf("%s imports %s: %s", fset.Position(imp.Pos()), importPath, why))
			}
		}
		for suffix, allow := range a2aWatched {
			if strings.HasSuffix(importPath, suffix) {
				watchedByName[a2aLocalName(imp, importPath)] = allow
			}
		}
	}
	return out, watchedByName, nil
}

// a2aLocalName is the name an import is referred to by in the file.
func a2aLocalName(imp *ast.ImportSpec, importPath string) string {
	if imp.Name != nil {
		return imp.Name.Name
	}
	return path.Base(importPath)
}

// a2aUseFindings reports every selector on a watched package whose member is
// outside that package's allowlist.
func a2aUseFindings(fset *token.FileSet, f *ast.File, watchedByName map[string]map[string]bool) []string {
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if allow, watched := watchedByName[pkg.Name]; watched && !allow[sel.Sel.Name] {
			out = append(out, fmt.Sprintf("%s uses %s.%s, which is outside the read-only allowlist", fset.Position(sel.Pos()), pkg.Name, sel.Sel.Name))
		}
		return true
	})
	return out
}

// TestA2AAgentIsReadOnlyAndHasNoOutboundClient is the sensor behind the ADR
// 0008 boundaries: across the A2A adapter and cmd/a2a (test files excluded:
// the wire tests legitimately seed with write use cases and drive the agent
// with the SDK client) only the read allowlist is used, and there is no
// outbound A2A client and no Kafka adapter.
func TestA2AAgentIsReadOnlyAndHasNoOutboundClient(t *testing.T) {
	var files []string
	files = append(files, goFilesUnder(t, "../adapters/inbound/a2a", false)...)
	files = append(files, goFilesUnder(t, "../../cmd/a2a", false)...)
	if len(files) < 4 {
		t.Fatalf("found only %d A2A sources %v; the sensor is looking in the wrong place", len(files), files)
	}
	sort.Strings(files)
	for _, file := range files {
		violations, err := a2aViolations(file, nil)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, v := range violations {
			t.Errorf("A2A read-only rule: %s", v)
		}
	}
}

// TestA2AReadOnlySensorBites proves the detector flags each forbidden shape
// in CODE (a write use case under any name, a unit of work, the outbox
// publisher, an outbound client, the Kafka adapter) and passes the allowed
// ones, so the sensor above cannot pass vacuously.
func TestA2AReadOnlySensorBites(t *testing.T) {
	const header = "package x\nimport (\n" +
		"\t\"github.com/claudioed/returns-processing/internal/application/usecases\"\n" +
		"\t\"github.com/claudioed/returns-processing/internal/application/ports\"\n" +
		"\t\"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres\"\n)\n"
	bad := map[string]string{
		"a write use case":          "var _ = usecases.AuthorizeReturn{}",
		"another write use case":    "var _ = usecases.OverrideDisposition{}",
		"a renamed future write":    "var _ = usecases.CloseReturn{}",
		"a unit of work":            "var _ ports.UnitOfWork",
		"an event publisher port":   "var _ ports.EventPublisher",
		"the postgres outbox":       "var _ = postgres.NewOutbox",
		"the postgres unit of work": "var _ = postgres.NewUnitOfWork",
	}
	for name, body := range bad {
		got, err := a2aViolations("fixture.go", header+body)
		if err != nil || len(got) == 0 {
			t.Errorf("the sensor does not flag %s: %v / %v", name, got, err)
		}
	}
	for name, src := range map[string]string{
		"an outbound a2a client": "package x\nimport _ \"github.com/a2aproject/a2a-go/v2/a2aclient\"\n",
		"the kafka adapter":      "package x\nimport _ \"github.com/claudioed/returns-processing/internal/adapters/outbound/kafka\"\n",
	} {
		if got, err := a2aViolations("fixture.go", src); err != nil || len(got) == 0 {
			t.Errorf("the sensor does not flag %s: %v / %v", name, got, err)
		}
	}

	allowed := header + "var (\n\t_ = usecases.GetReturn{}\n\t_ = usecases.ListReturnsByOrder{}\n\t_ = usecases.GetDispositionPolicy{}\n\t_ = ports.ErrNotFound\n\t_ = postgres.NewRMAs\n)\n"
	if got, err := a2aViolations("fixture.go", allowed); err != nil || len(got) != 0 {
		t.Errorf("false positive on the read allowlist: %v / %v", got, err)
	}
	// Prose that merely mentions a write verb is not code.
	prose := header + "// usecases.AuthorizeReturn is a REST operation; ports.UnitOfWork too.\nvar _ = usecases.GetReturn{}\n"
	if got, err := a2aViolations("fixture.go", prose); err != nil || len(got) != 0 {
		t.Errorf("false positive on a comment: %v / %v", got, err)
	}
}
