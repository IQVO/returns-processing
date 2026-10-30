// Command a2a is the composition root for the returns-processing A2A agent:
// it wires env config to the Postgres repositories, the repositories to the
// SAME read use cases the REST and MCP adapters use (GetReturn,
// ListReturnsByOrder, GetDispositionPolicy), and those to the inbound A2A
// adapter, then serves Agent2Agent JSON-RPC over HTTP with the Agent Card at
// the well-known path (ADR 0008). It is a third, independent deployable
// alongside cmd/api and cmd/mcp.
//
// The surface is READ-ONLY, exactly like cmd/mcp (docs/docs/mcp/tools.md): no write use
// case is wired here at all, so this binary can never insert into the outbox
// (it builds no event sink, no unit of work and no outbox repository). It
// never starts the outbox relay, never runs the local-copy consumers and
// never dials Kafka (no KAFKA_BROKERS, no consumer groups, no *_MODE): those
// run in cmd/api only. Like cmd/api it has no in-memory mode: without
// DATABASE_URL it refuses to boot. It contains no outbound A2A client.
//
// There is NO auth of any kind (fleet-wide revert 2026-09-11): no keys, no
// bearer checks. Access control is the in-cluster ClusterIP boundary.
//
// Environment:
//
//	DATABASE_URL             REQUIRED. Postgres DSN
//	A2A_AGENT_URL            REQUIRED. Absolute http(s) URL of this agent's
//	                         JSON-RPC endpoint, advertised on the Agent Card
//	MIGRATIONS_DATABASE_URL  direct DSN for the boot migrations (default DATABASE_URL)
//	A2A_ADDR                 listen address (default :8093)
//	LOG_LEVEL                debug | info | warn | error (default info)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	inbounda2a "github.com/claudioed/returns-processing/internal/adapters/inbound/a2a"
	"github.com/claudioed/returns-processing/internal/adapters/outbound/postgres"
	"github.com/claudioed/returns-processing/internal/application/usecases"
)

// agentVersion is the Agent Card version (like the MCP server's serverVersion
// it is a constant of the contract, not deployment configuration).
const agentVersion = "1.0.0"

// shutdownTimeout bounds the HTTP server's graceful drain.
const shutdownTimeout = 10 * time.Second

// errNoDatabase is the boot error of a missing DATABASE_URL.
var errNoDatabase = errors.New("DATABASE_URL is required: this context runs on real Postgres")

// errNoAgentURL is the boot error for a missing A2A_AGENT_URL: the Agent Card
// must advertise the absolute URL clients dial (ADR 0008).
var errNoAgentURL = errors.New("A2A_AGENT_URL is required: the Agent Card must advertise the absolute URL of this agent's JSON-RPC endpoint (ADR 0008), e.g. http://<release>-a2a.<ns>.svc.cluster.local:8093/")

// bootConfig is the mandatory configuration of cmd/a2a: real Postgres and the
// URL the Agent Card advertises, with no default and no degraded mode behind
// either.
type bootConfig struct {
	databaseURL string
	agentURL    string
}

// checkAgentURL validates A2A_AGENT_URL: an absolute http(s) URL. The card is
// the discovery contract; a relative or schemeless URL is a misconfiguration
// every client would hit, so it is refused at boot, never silently served.
func checkAgentURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errNoAgentURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("A2A_AGENT_URL=%q is not an absolute http(s) URL: the Agent Card must advertise the endpoint clients dial (ADR 0008)", raw)
	}
	return nil
}

// loadBootConfig reads and validates the mandatory environment. Every problem
// is reported at once, each naming its variable, so the process fails fast at
// boot, before any connection and the listener.
func loadBootConfig() (bootConfig, error) {
	cfg := bootConfig{
		databaseURL: os.Getenv("DATABASE_URL"),
		agentURL:    strings.TrimSpace(os.Getenv("A2A_AGENT_URL")),
	}
	var errs []error
	if cfg.databaseURL == "" {
		errs = append(errs, errNoDatabase)
	}
	if err := checkAgentURL(cfg.agentURL); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return bootConfig{}, errors.Join(errs...)
	}
	return cfg, nil
}

func main() {
	if err := run(); err != nil {
		slog.Error("a2a agent exited with error", "error", err)
		os.Exit(1)
	}
}

func run() error {
	logger := newLogger(getenv("LOG_LEVEL", "info"))
	slog.SetDefault(logger)

	// Fail fast, before any connection and the listener: DATABASE_URL (real
	// Postgres, no in-memory mode) and the absolute A2A_AGENT_URL the Agent
	// Card advertises are both mandatory.
	cfg, err := loadBootConfig()
	if err != nil {
		return err
	}
	deps, closeAdapters, err := buildDeps(context.Background(), logger, cfg.databaseURL,
		getenv(postgres.EnvMigrationsDatabaseURL, cfg.databaseURL))
	if err != nil {
		return err
	}
	defer closeAdapters()

	srv := &http.Server{
		Addr:              getenv("A2A_ADDR", ":8093"),
		Handler:           newAgent(deps, cfg.agentURL, agentVersion),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return serveA2A(logger, srv)
}

// buildDeps migrates and connects Postgres and wires the READ use cases over
// it. The returned close func releases the pool. An empty databaseURL is a
// boot error: there is no fallback runtime.
//
// Like cmd/mcp this binary runs the idempotent embedded
// migrations on start, so it can boot against a fresh database before cmd/api
// has; golang-migrate's advisory lock makes concurrent starts safe. The
// migrations run against MIGRATIONS_DATABASE_URL when it is set (a DIRECT
// Postgres DSN where DATABASE_URL points at PgBouncer, which cannot honour
// the session-scoped advisory lock; postgres.MigrationsURL applies the
// fallback); the runtime pool always uses databaseURL.
func buildDeps(ctx context.Context, logger *slog.Logger, databaseURL, migrationsDatabaseURL string) (inbounda2a.Deps, func(), error) {
	noop := func() {}
	if databaseURL == "" {
		return inbounda2a.Deps{}, noop, errNoDatabase
	}
	if err := postgres.RunMigrations(migrationsDatabaseURL); err != nil {
		logger.Error("migrations failed", "error", err)
		return inbounda2a.Deps{}, noop, err
	}
	pool, err := postgres.NewPool(ctx, databaseURL)
	if err != nil {
		logger.Error("postgres pool failed", "error", err)
		return inbounda2a.Deps{}, noop, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		logger.Error("postgres ping failed", "error", err)
		return inbounda2a.Deps{}, noop, err
	}
	logger.Info("postgres repositories configured")

	rmas, policies := postgres.NewRMAs(pool), postgres.NewPolicies(pool)
	return inbounda2a.Deps{
		GetReturn:   &usecases.GetReturn{RMAs: rmas},
		ListByOrder: &usecases.ListReturnsByOrder{RMAs: rmas},
		GetPolicy:   &usecases.GetDispositionPolicy{Policies: policies},
	}, pool.Close, nil
}

// serveA2A runs srv until it fails or SIGINT/SIGTERM arrives, then drains it
// gracefully.
func serveA2A(logger *slog.Logger, srv *http.Server) error {
	serverErr := make(chan error, 1)
	go func() {
		logger.Info("a2a agent listening (JSON-RPC over HTTP)", "addr", srv.Addr)
		serverErr <- srv.ListenAndServe()
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newLogger builds the process-wide JSON logger. LOG_LEVEL maps
// debug|info|warn|warning|error (case-insensitive; "warning" is accepted
// like "warn" fleet-wide), defaulting to Info.
func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
