#!/usr/bin/env python3
"""Assert this chart's Kong configuration contract.

Fleet decision (2026-10-13): ALL Kong configuration — routes AND plugins —
belongs to the application's Helm chart, not warehouse-infra Terraform. The
old Terraform-side null_resource + kubectl objects silently vanished on a
cluster rebuild (local-exec only runs when triggers change) while
chart-rendered objects are Helm/ArgoCD-reconciled forever.

This test is the sensor for that decision. It fails if:
  * any enabled HTTPRoute renders WITHOUT the konghq.com/plugins annotation,
  * the annotation names KongPlugins the chart does not actually render,
  * any auth/authorization plugin (key-auth, jwt, oidc, acl, basic-auth,
    oauth2, ...) is ever configured — forbidden fleet-wide,
  * the route does not strip its /api/returns-processing prefix before
    the request reaches the service (the service serves /returns,
    /disposition-policies ... at the root),
  * the CORS plugin allows "*" or does not allow the Idempotency-Key header
    the console's command POSTs send,
  * the MCP route (mcp.route.enabled) does not rewrite its host prefix to the
    SDK's /mcp endpoint, or targets anything but the -mcp Service,
  * the A2A route (a2a.route.enabled) does not rewrite its host prefix to /
    (the JSON-RPC endpoint and the Agent Card both live at the root), or
    targets anything but the -a2a Service,
  * defaults (no --set flags beyond the mandatory runtime values) render any
    Kong object at all — standalone installs without Kong must be unaffected.

This context routes its API and (optionally) its MCP server; the analytics
reports Service and the web remote have no Kong route (reports stays
ClusterIP-only, the Nginx web gateway owns the frontend).

Run: python3 charts/returns-processing/tests/test_kong_config.py
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART = Path(__file__).resolve().parents[1]
RELEASE = "returns-processing"

FORBIDDEN_PLUGINS = {
    "basic-auth", "key-auth", "jwt", "oauth2", "oidc", "acl",
    "hmac-auth", "ldap-auth", "mtls", "session", "request-termination",
}

# The chart refuses to render without a database DSN and Kafka brokers (the
# real runtime): supply dummies for template assertions.
MANDATORY = [
    "--set", "database.url=postgres://user:***@localhost:5432/db",
    "--set", "kafka.brokers=kafka.example.invalid:9092",
]

ENABLE_KONG = MANDATORY + [
    "--set", "gatewayApi.enabled=true",
    "--set", "gatewayApi.parentRefs[0].name=warehouse-gateway",
    "--set", "gatewayApi.parentRefs[0].namespace=kong",
    "--set", "gatewayApi.hosts[0].path=/api/returns-processing",
    "--set", "kongPlugins.cors.enabled=true",
    "--set", "kongPlugins.prometheus.enabled=true",
    "--set", "kongPlugins.opentelemetry.enabled=true",
    "--set", "mcp.enabled=true",
    "--set", "mcp.route.enabled=true",
    "--set", "mcp.route.parentRefs[0].name=warehouse-gateway",
    "--set", "mcp.route.parentRefs[0].namespace=kong",
    "--set", "a2a.enabled=true",
    "--set", "a2a.route.enabled=true",
    "--set", "a2a.route.parentRefs[0].name=warehouse-gateway",
    "--set", "a2a.route.parentRefs[0].namespace=kong",
]


def render(extra_args: list[str]) -> list[dict]:
    out = subprocess.run(
        ["helm", "template", RELEASE, str(CHART), *extra_args],
        capture_output=True, text=True, check=True,
    ).stdout
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert Kong config", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(out) if d]


def check_routes(routes: list[dict], plugins: dict[str, dict], failures: list[str]) -> None:
    if len(routes) != 3:
        failures.append(f"enabled render must contain the api, the mcp and the a2a HTTPRoute, got {len(routes)}")
    if not plugins:
        failures.append("enabled render contains no KongPlugin at all")
    for r in routes:
        name = r["metadata"]["name"]
        ann = (r.get("metadata", {}).get("annotations") or {}).get("konghq.com/plugins")
        if not ann:
            failures.append(f"HTTPRoute {name} has no konghq.com/plugins annotation")
        else:
            for plugin in ann.split(","):
                if plugin not in plugins:
                    failures.append(f"HTTPRoute {name} references plugin '{plugin}' that the chart does not render")
        for rule in r.get("spec", {}).get("rules", []):
            check_rule(name, rule, failures)


def check_rule(name: str, rule: dict, failures: list[str]) -> None:
    if name.endswith("-mcp"):
        check_mcp_rule(name, rule, failures)
        return
    if name.endswith("-a2a"):
        check_a2a_rule(name, rule, failures)
        return
    prefix = rule["matches"][0]["path"]["value"]
    if prefix != "/api/returns-processing":
        failures.append(f"HTTPRoute {name} matches {prefix!r}, expected '/api/returns-processing'")
    rewrites = [f for f in rule.get("filters", []) if f.get("type") == "URLRewrite"]
    if len(rewrites) != 1:
        failures.append(f"HTTPRoute {name} must strip its prefix with exactly one URLRewrite filter")
        return
    target = rewrites[0]["urlRewrite"]["path"].get("replacePrefixMatch")
    if target != "/":
        failures.append(f"HTTPRoute {name} rewrites to {target!r}, expected '/' (the service serves at the root)")


def check_mcp_rule(name: str, rule: dict, failures: list[str]) -> None:
    prefix = rule["matches"][0]["path"]["value"]
    if prefix != "/api/mcp/returns-processing":
        failures.append(f"HTTPRoute {name} matches {prefix!r}, expected '/api/mcp/returns-processing'")
    rewrites = [f for f in rule.get("filters", []) if f.get("type") == "URLRewrite"]
    if len(rewrites) != 1 or rewrites[0]["urlRewrite"]["path"].get("replacePrefixMatch") != "/mcp":
        failures.append(f"HTTPRoute {name} must rewrite its prefix to /mcp, the SDK's endpoint")
    backend = rule["backendRefs"][0]["name"]
    if backend != f"{RELEASE}-mcp":
        failures.append(f"HTTPRoute {name} targets {backend!r}, expected the {RELEASE}-mcp Service")


def check_a2a_rule(name: str, rule: dict, failures: list[str]) -> None:
    prefix = rule["matches"][0]["path"]["value"]
    if prefix != "/api/a2a/returns-processing":
        failures.append(f"HTTPRoute {name} matches {prefix!r}, expected '/api/a2a/returns-processing'")
    rewrites = [f for f in rule.get("filters", []) if f.get("type") == "URLRewrite"]
    if len(rewrites) != 1 or rewrites[0]["urlRewrite"]["path"].get("replacePrefixMatch") != "/":
        failures.append(f"HTTPRoute {name} must rewrite its prefix to /, so the Agent Card stays reachable under it")
    backend = rule["backendRefs"][0]["name"]
    if backend != f"{RELEASE}-a2a":
        failures.append(f"HTTPRoute {name} targets {backend!r}, expected the {RELEASE}-a2a Service")


def check_plugins(plugins: dict[str, dict], failures: list[str]) -> None:
    for name, p in plugins.items():
        plugin_type = p.get("plugin", "")
        if plugin_type in FORBIDDEN_PLUGINS:
            failures.append(
                f"KongPlugin {name} is '{plugin_type}' — authentication/authorization "
                "plugins are forbidden at the gateway by fleet-wide decision"
            )
    cors = next((p for p in plugins.values() if p.get("plugin") == "cors"), None)
    if cors is None:
        failures.append("the cors plugin was enabled but not rendered")
        return
    cfg = cors["config"]
    if "*" in cfg.get("origins", []):
        failures.append("the cors plugin allows origin '*': endpoints are unauthenticated, use the exact origin")
    if "Idempotency-Key" not in cfg.get("headers", []):
        failures.append("the cors plugin must allow the Idempotency-Key request header the commands send")


def main() -> int:
    failures: list[str] = []

    # 1. Defaults render zero Kong objects.
    default_docs = render(MANDATORY)
    kong_docs = [d for d in default_docs if "Kong" in (d.get("kind") or "")]
    if kong_docs:
        failures.append(
            f"default values render Kong objects {sorted(d['metadata']['name'] for d in kong_docs)} "
            "-- standalone installs must be unaffected; keep plugin/route blocks default-off"
        )
    if any(d.get("kind") == "HTTPRoute" for d in default_docs):
        failures.append("default values render an HTTPRoute")

    # 2. Enabled render.
    docs = render(ENABLE_KONG)
    plugins = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "KongPlugin"}
    routes = [d for d in docs if d.get("kind") == "HTTPRoute"]
    check_routes(routes, plugins, failures)
    check_plugins(plugins, failures)

    if failures:
        print("FAIL: returns-processing chart Kong-config contract")
        for f in failures:
            print(f"  - {f}")
        return 1
    print(f"PASS: {len(routes)} HTTPRoutes annotated and stripping their prefixes, {len(plugins)} KongPlugins, "
          "no auth plugin, defaults are a no-op")
    return 0


if __name__ == "__main__":
    sys.exit(main())
