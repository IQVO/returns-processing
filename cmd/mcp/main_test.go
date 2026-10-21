package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	inboundmcp "github.com/claudioed/returns-processing/internal/adapters/inbound/mcp"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The mcp binary has no in-memory mode: a missing DATABASE_URL is a boot
// error naming it.
func TestBuildDepsRefusesWithoutADatabase(t *testing.T) {
	_, _, err := buildDeps(context.Background(), quietLogger(), "", "")
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("buildDeps without a database = %v, want a boot error naming DATABASE_URL", err)
	}
}

// An unreachable database is a boot error, not a silent serve.
func TestBuildDepsBadDatabaseFailsBoot(t *testing.T) {
	url := "postgres://u@127.0.0.1:1/db" // nothing listens on port 1
	_, _, err := buildDeps(context.Background(), quietLogger(), url, url)
	if err == nil {
		t.Fatal("an unreachable database must fail boot")
	}
}

// newTestServer serves the real router and MCP server over a deps value whose
// use cases are unwired (the tests here never call a tool).
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(newRouter(inboundmcp.Handler(inboundmcp.NewServer(inboundmcp.Deps{}))))
	t.Cleanup(srv.Close)
	return srv
}

func TestRouterHealthzIsOpenAndServesOK(t *testing.T) {
	srv := newTestServer(t)
	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"status":"ok"}` {
		t.Fatalf("GET /healthz = %d %q, want 200 {\"status\":\"ok\"}", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
}

// Both mount points speak Streamable HTTP, with no credentials, and a real
// client lists the three read-only tools.
func TestStreamableHTTPRootAndMCPPathsListTools(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/", "/mcp"} {
		t.Run(path, func(t *testing.T) {
			client := sdk.NewClient(&sdk.Implementation{Name: "t", Version: "0"}, nil)
			session, err := client.Connect(context.Background(), &sdk.StreamableClientTransport{Endpoint: srv.URL + path}, nil)
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			defer session.Close()
			tools, err := session.ListTools(context.Background(), nil)
			if err != nil {
				t.Fatalf("list tools: %v", err)
			}
			if len(tools.Tools) != 3 {
				t.Fatalf("tools = %d, want 3", len(tools.Tools))
			}
			for _, tool := range tools.Tools {
				if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
					t.Fatalf("tool %q is not read-only", tool.Name)
				}
			}
		})
	}
}

// The binary reads no API key: requests with or without any Authorization
// header behave identically.
func TestNoAuthIsRequired(t *testing.T) {
	srv := newTestServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	for _, auth := range []string{"", "Bearer nonsense"} {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("initialize with Authorization=%q = %d, want 200", auth, resp.StatusCode)
		}
	}
}

func TestGetenv(t *testing.T) {
	t.Setenv("RP_MCP_TEST_SET", "v")
	t.Setenv("RP_MCP_TEST_EMPTY", "")
	if got := getenv("RP_MCP_TEST_SET", "d"); got != "v" {
		t.Errorf("set: %q", got)
	}
	if got := getenv("RP_MCP_TEST_EMPTY", "d"); got != "d" {
		t.Errorf("empty: %q", got)
	}
}

func TestNewLoggerLevels(t *testing.T) {
	for level, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "warning": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo, "bogus": slog.LevelInfo} {
		if got := newLogger(level); !got.Handler().Enabled(context.Background(), want) || (want > slog.LevelDebug && got.Handler().Enabled(context.Background(), want-1)) {
			t.Errorf("newLogger(%q): level %v not the minimum enabled", level, want)
		}
	}
}
