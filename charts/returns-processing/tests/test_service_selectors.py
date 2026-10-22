#!/usr/bin/env python3
"""Assert this chart's Service selectors isolate each component, and that every
Deployment carries the real runtime's environment.

Why the selector check exists (fleet-wide): `app.kubernetes.io/name` +
`instance` are identical on every pod a release creates. A Service that
selects on those two alone selects ALL components -- verified live in other
fleet charts, where a Kong request for an api /healthz was answered by the
wrong pod. Every Deployment here carries `app.kubernetes.io/component` in
selector.matchLabels and its pod labels, and every Service selects on it, so
each Service selects EXACTLY ONE Deployment. This test fails if that ever
stops being true.

Components: api (always), mcp (default off; the read-only MCP server of
cmd/mcp: get_return, list_returns_by_order, get_disposition_policy over
Streamable HTTP) and frontend (default off; the returns_mfe Module
Federation remote from web/, static nginx). The enabled render runs all
three TOGETHER.

It also asserts the ADR 0001 runtime: the mandatory env is rendered and no
*_MODE variable ever is.

Run: python3 charts/returns-processing/tests/test_service_selectors.py
Needs: helm, PyYAML.
"""

from __future__ import annotations

import subprocess
import sys
from pathlib import Path

CHART_DIR = Path(__file__).resolve().parents[1]
RELEASE = "returns-processing"

# Dummy values, no password: the chart refuses to render without a database
# source and without Kafka brokers.
BASE = [
    "--set", "database.url=postgres://u@example.invalid:5432/db",
    "--set", "kafka.brokers=kafka.example.invalid:9092",
]
ENABLE_EVERYTHING = BASE + [
    "--set", "config.outboxRelayInterval=500ms",
    "--set", "config.shutdownDrainDelay=5s",
    "--set", "mcp.enabled=true",
    "--set", "frontend.enabled=true",
]

# Every env var cmd/api reads that has a dedicated chart value.
API_ENV = (
    "HTTP_ADDR", "LOG_LEVEL", "DATABASE_URL", "MIGRATIONS_DATABASE_URL", "KAFKA_BROKERS",
    "PRODUCT_CLASSIFICATION_CONSUMER_GROUP", "ORDER_LINES_CONSUMER_GROUP", "MANIFEST_CONSUMER_GROUP",
    "OUTBOX_RELAY_INTERVAL", "OUTBOX_MAX_ATTEMPTS", "SHUTDOWN_DRAIN_DELAY",
)
# Never rendered: no *_MODE variable exists in the binary's contract (ADR 0001).
FORBIDDEN_ENV = ("PRODUCT_CLASSIFICATION_MODE", "ORDER_LINES_MODE", "MANIFEST_MODE")


def helm_template(extra_args: list[str]) -> subprocess.CompletedProcess:
    return subprocess.run(
        ["helm", "template", RELEASE, str(CHART_DIR), *extra_args],
        capture_output=True, text=True,
    )


def render(extra_args: list[str]) -> list[dict]:
    result = helm_template(extra_args)
    if result.returncode != 0:
        raise SystemExit(f"FAIL: helm template {' '.join(extra_args)}:\n{result.stderr}")
    try:
        import yaml  # type: ignore
    except ModuleNotFoundError:  # pragma: no cover - environment guard
        print("SKIP: PyYAML not available; cannot assert selectors", file=sys.stderr)
        raise SystemExit(0)
    return [d for d in yaml.safe_load_all(result.stdout) if d]


def selector_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("selector") or {}


def pod_labels_of(doc: dict) -> dict:
    return doc.get("spec", {}).get("template", {}).get("metadata", {}).get("labels") or {}


def matches(selector: dict, labels: dict) -> bool:
    return bool(selector) and all(labels.get(k) == v for k, v in selector.items())


def env_of(dep: dict) -> list[dict]:
    return dep["spec"]["template"]["spec"]["containers"][0].get("env", [])


def env_names(dep: dict) -> list[str]:
    return [e["name"] for e in env_of(dep)]


def env_value(dep: dict, name: str) -> str | None:
    return next((e.get("value") for e in env_of(dep) if e["name"] == name), None)


def check_selectors(docs: list[dict], failures: list[str]) -> None:
    services = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Service"}
    deployments = {d["metadata"]["name"]: d for d in docs if d.get("kind") == "Deployment"}

    expected = {RELEASE, f"{RELEASE}-mcp", f"{RELEASE}-frontend"}
    if set(deployments) != expected:
        failures.append(f"expected Deployments {sorted(expected)}, got {sorted(deployments)}")
    if set(services) != expected:
        failures.append(f"expected a Service per Deployment, got {sorted(services)}")

    for name, component in ((RELEASE, "api"), (f"{RELEASE}-mcp", "mcp"), (f"{RELEASE}-frontend", "frontend")):
        if name in services and selector_of(services[name]).get("app.kubernetes.io/component") != component:
            failures.append(f"Service {name} selector must pin component={component}")
        if name in deployments:
            labels = deployments[name]["metadata"].get("labels", {})
            if labels.get("app.kubernetes.io/component") != component:
                failures.append(f"Deployment {name} must be labelled component={component}")

    # Every Deployment's own selector must pin a component too, and be
    # satisfied by its pod labels.
    for name, dep in deployments.items():
        match_labels = dep["spec"]["selector"].get("matchLabels") or {}
        if "app.kubernetes.io/component" not in match_labels:
            failures.append(f"Deployment {name} selector.matchLabels lacks app.kubernetes.io/component")
        if not matches(match_labels, pod_labels_of(dep)):
            failures.append(f"Deployment {name} pod labels do not satisfy its own selector")

    # The real invariant: each Service selects exactly one Deployment.
    for svc_name, svc in services.items():
        sel = selector_of(svc)
        hit = [d for d, dep in deployments.items() if matches(sel, pod_labels_of(dep))]
        if len(hit) != 1:
            failures.append(f"Service {svc_name} selects {len(hit)} Deployments {sorted(hit)}; expected exactly 1")

    # Every Service's named target port exists on its container.
    for name, svc in services.items():
        dep = deployments.get(name)
        if dep is None:
            continue
        ports = {p["name"] for p in dep["spec"]["template"]["spec"]["containers"][0].get("ports", [])}
        if "http" not in ports:
            failures.append(f"the {name} container does not expose the named port 'http' the Service targets")

    check_env(deployments.get(RELEASE), failures)
    check_mcp(deployments.get(f"{RELEASE}-mcp"), failures)
    check_frontend(deployments.get(f"{RELEASE}-frontend"), failures)


def command_of(dep: dict) -> list[str] | None:
    return dep["spec"]["template"]["spec"]["containers"][0].get("command")


def check_env(api: dict | None, failures: list[str]) -> None:
    if api is None:
        failures.append("the api Deployment was not rendered")
        return
    api_env = env_names(api)
    for want in API_ENV:
        if want not in api_env:
            failures.append(f"the api Deployment does not render {want} from its dedicated value")
    for banned in FORBIDDEN_ENV:
        if banned in api_env:
            failures.append(f"the api Deployment renders {banned}: no *_MODE variable exists in the contract (ADR 0001)")
    duplicates = sorted({n for n in api_env if api_env.count(n) > 1})
    if duplicates:
        failures.append(f"the api Deployment renders env vars twice: {duplicates}")
    for name, want in (
        ("KAFKA_BROKERS", "kafka.example.invalid:9092"),
        ("OUTBOX_RELAY_INTERVAL", "500ms"),
        ("SHUTDOWN_DRAIN_DELAY", "5s"),
    ):
        if env_value(api, name) != want:
            failures.append(f"the api Deployment renders {name}={env_value(api, name)!r}, want {want!r}")


def check_mcp(dep: dict | None, failures: list[str]) -> None:
    if dep is None:
        return
    if command_of(dep) != ["/app/mcp"]:
        failures.append("the MCP Deployment must run /app/mcp")
    env = set(env_names(dep))
    # Read-only: it never relays, never consumes, never dials Kafka.
    stray = {
        "KAFKA_BROKERS", "OUTBOX_RELAY_INTERVAL", "OUTBOX_MAX_ATTEMPTS", "SHUTDOWN_DRAIN_DELAY",
        "PRODUCT_CLASSIFICATION_CONSUMER_GROUP", "ORDER_LINES_CONSUMER_GROUP", "MANIFEST_CONSUMER_GROUP",
        *FORBIDDEN_ENV,
    } & env
    if stray:
        failures.append(f"the MCP Deployment must not get Kafka/relay/consumer env (it never dials Kafka): {sorted(stray)}")
    for want in ("DATABASE_URL", "MIGRATIONS_DATABASE_URL", "MCP_ADDR", "LOG_LEVEL"):
        if want not in env:
            failures.append(f"the MCP Deployment does not render {want}")


def check_frontend(dep: dict | None, failures: list[str]) -> None:
    """The frontend is a static nginx pod: no env from this chart's runtime at
    all (its API origin is read at runtime from the console's /config.json)."""
    if dep is None:
        return
    if env_names(dep):
        failures.append(f"the frontend Deployment must render no env (static SPA): {env_names(dep)}")
    ports = dep["spec"]["template"]["spec"]["containers"][0].get("ports", [])
    if not any(p.get("name") == "http" for p in ports):
        failures.append("the frontend container does not expose the named port 'http'")


def check_defaults(failures: list[str]) -> None:
    """Default values deploy the api only."""
    defaults = render(BASE)
    stray = [d["metadata"]["name"] for d in defaults
             if d["metadata"]["name"].endswith(("-mcp", "-frontend"))]
    if stray:
        failures.append(f"optional components rendered with default values: {stray}")
    kinds = sorted(d["kind"] for d in defaults if d["kind"] in {"Deployment", "Service"})
    if kinds != ["Deployment", "Service"]:
        failures.append("default values must deploy the api only (one Deployment, one Service)")
    dep = next(d for d in defaults if d.get("kind") == "Deployment")
    names = env_names(dep)
    for banned in FORBIDDEN_ENV:
        if banned in names:
            failures.append(f"{banned} must not be rendered with default values")
    # Optional values stay absent so the binary's own defaults apply.
    off = {"OUTBOX_RELAY_INTERVAL", "SHUTDOWN_DRAIN_DELAY"}
    if off & set(names):
        failures.append(f"these must be off with default values: {sorted(off & set(names))}")


def check_refusals(failures: list[str]) -> None:
    for label, args, needle in (
        ("without a database source", ["--set", "kafka.brokers=k:9092"], "database.url or database.existingSecret"),
        ("without Kafka brokers", ["--set", "database.url=postgres://u@h/db"], "kafka.brokers"),
        ("an empty classification group", BASE + ["--set", "config.productClassificationConsumerGroup="],
         "config.productClassificationConsumerGroup"),
        ("an empty order-lines group", BASE + ["--set", "config.orderLinesConsumerGroup="], "config.orderLinesConsumerGroup"),
        ("an empty manifest group", BASE + ["--set", "config.manifestConsumerGroup="], "config.manifestConsumerGroup"),
        ("a *_MODE variable through extraEnv",
         BASE + ["--set", "extraEnv[0].name=MANIFEST_MODE", "--set", "extraEnv[0].value=permissive"], "MANIFEST_MODE"),
    ):
        result = helm_template(args)
        if result.returncode == 0 or needle not in result.stderr:
            failures.append(f"chart rendered (or failed for another reason) {label}")


def main() -> int:
    failures: list[str] = []

    check_selectors(render(ENABLE_EVERYTHING), failures)
    check_defaults(failures)
    check_refusals(failures)

    if failures:
        for f in failures:
            print(f"FAIL: {f}")
        return 1

    print("PASS: every Service selects exactly one Deployment (api, mcp, frontend); mcp and frontend "
          "are off by default; every cmd/api env var has a dedicated value and no *_MODE is rendered; "
          "the frontend renders no env; the chart refuses to render without a database source, without "
          "brokers, with an empty consumer group or with a *_MODE injected through extraEnv")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
