#!/usr/bin/env bash
# Chart render assertions: the chart deploys the REAL runtime of this context
# (ADR 0001: Postgres and Kafka are the only runtime, no in-memory mode), so
# the mandatory configuration has no silent default, no *_MODE variable is
# ever rendered, and the env the binary reads is the env the chart renders.
# Run: bash charts/returns-processing/tests/render_test.sh   (needs helm)
set -euo pipefail

chart_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
repo_dir="$(cd "$chart_dir/../.." && pwd)"
release=returns-processing
# Dummy values, no password: the chart refuses to render without a database
# source and without Kafka brokers.
base=(--set database.url=postgres://u@example.invalid:5432/db --set kafka.brokers=kafka.example.invalid:9092)
# Every optional value set (ct lint's values file), so every env var the
# binary can read is rendered.
everything=(-f "$chart_dir/ci/all-components-values.yaml")

fail() { echo "FAIL: $*" >&2; exit 1; }

render() { helm template "$release" "$chart_dir" "$@"; }
# render_only <template> <helm args...>
render_only() { local t="$1"; shift; render "$@" --show-only "templates/$t"; }

configmap="$(render_only configmap.yaml "${base[@]}")"
deployment="$(render_only deployment.yaml "${base[@]}")"
configmap_name="$(awk '/^  name:/ {print $2; exit}' <<<"$configmap")"
[[ -n "$configmap_name" ]] || fail "could not read the ConfigMap name"

# --- the ConfigMap is mounted (gap fix D14) ---------------------------------
grep -A3 'envFrom:' <<<"$deployment" | grep -q 'configMapRef:' \
  || fail "Deployment does not mount the ConfigMap via envFrom"
grep -A3 'envFrom:' <<<"$deployment" | grep -q "name: $configmap_name" \
  || fail "Deployment envFrom does not reference ConfigMap $configmap_name"

# --- mandatory configuration is rendered from values -------------------------
env_value() { # env_value <rendered> <NAME>
  grep -A1 "name: $2\$" <<<"$1" | grep -m1 'value:' | sed -E 's/.*value: "?//; s/"$//'
}
[[ "$(env_value "$deployment" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "KAFKA_BROKERS is not rendered from kafka.brokers"
[[ "$(env_value "$deployment" PRODUCT_CLASSIFICATION_CONSUMER_GROUP)" == "returns-processing-product-classification" ]] || fail "PRODUCT_CLASSIFICATION_CONSUMER_GROUP is not rendered from config.productClassificationConsumerGroup"
[[ "$(env_value "$deployment" ORDER_LINES_CONSUMER_GROUP)" == "returns-processing-order-lines" ]] || fail "ORDER_LINES_CONSUMER_GROUP is not rendered from config.orderLinesConsumerGroup"
[[ "$(env_value "$deployment" MANIFEST_CONSUMER_GROUP)" == "returns-processing-manifest" ]] || fail "MANIFEST_CONSUMER_GROUP is not rendered from config.manifestConsumerGroup"
grep -A4 'name: DATABASE_URL$' <<<"$deployment" | grep -q 'secretKeyRef:' || fail "DATABASE_URL must come from a Secret (secretKeyRef)"
grep -A6 'name: MIGRATIONS_DATABASE_URL$' <<<"$deployment" | grep -q 'optional: true' || fail "MIGRATIONS_DATABASE_URL must be an optional secretKeyRef"
for var in KAFKA_BROKERS PRODUCT_CLASSIFICATION_CONSUMER_GROUP ORDER_LINES_CONSUMER_GROUP MANIFEST_CONSUMER_GROUP; do
  grep -q "^  $var: " <<<"$configmap" || fail "the ConfigMap does not carry $var"
done
if grep -qE '^  (MIGRATIONS_)?DATABASE_URL:' <<<"$configmap"; then fail "a DSN must never land in the ConfigMap"; fi

# The consumer group ids come from values: overriding them changes the env.
renamed="$(render_only deployment.yaml "${base[@]}" --set config.productClassificationConsumerGroup=g-class --set config.orderLinesConsumerGroup=g-orders --set config.manifestConsumerGroup=g-manifest)"
[[ "$(env_value "$renamed" PRODUCT_CLASSIFICATION_CONSUMER_GROUP)" == "g-class" ]] || fail "config.productClassificationConsumerGroup does not reach PRODUCT_CLASSIFICATION_CONSUMER_GROUP"
[[ "$(env_value "$renamed" ORDER_LINES_CONSUMER_GROUP)" == "g-orders" ]] || fail "config.orderLinesConsumerGroup does not reach ORDER_LINES_CONSUMER_GROUP"
[[ "$(env_value "$renamed" MANIFEST_CONSUMER_GROUP)" == "g-manifest" ]] || fail "config.manifestConsumerGroup does not reach MANIFEST_CONSUMER_GROUP"

# --- no in-memory / permissive mode, ever (ADR 0001) -------------------------
no_mode() { # no_mode <label> <rendered>
  if grep -qE '(name: |^  )[A-Z_]+_MODE:?$|^  [A-Z_]+_MODE: ' <<<"$2"; then fail "$1 renders a *_MODE variable; none exists in the binary's contract and they must never be set"; fi
  if grep -qiE 'value: "?permissive' <<<"$2"; then fail "$1 renders permissive, which no runtime of this service has"; fi
}
no_mode "ConfigMap" "$configmap"
no_mode "Deployment" "$deployment"
all_docs="$(render "${base[@]}" "${everything[@]}")"
no_mode "the full render" "$all_docs"
if grep -q 'DATABASE_URL: ""' <<<"$all_docs"; then fail "an empty DATABASE_URL must never be rendered"; fi

# --- probes: /healthz liveness (and startup), /readyz readiness --------------
probe_block() { # probe_block <probeName> <rendered deployment>
  awk -v p="$1:" '$1 == p {f=1; next} f && /^          [a-zA-Z]/ {exit} f {print}' <<<"$2"
}
grep -q 'path: /healthz' <<<"$(probe_block livenessProbe "$deployment")" || fail "livenessProbe must hit /healthz"
grep -q 'path: /healthz' <<<"$(probe_block startupProbe "$deployment")" || fail "startupProbe must hit /healthz"
readiness="$(probe_block readinessProbe "$deployment")"
grep -q 'path: /readyz' <<<"$readiness" || fail "readinessProbe must hit /readyz"
# /readyz checks Postgres AND that a Kafka broker answers (cached, bounded):
# give it a sane delay, period, timeout and failure budget so a broker blip
# does not flap.
grep -q 'initialDelaySeconds: [1-9]' <<<"$readiness" || fail "readinessProbe needs an initialDelaySeconds"
grep -q 'periodSeconds: [1-9]' <<<"$readiness" || fail "readinessProbe needs a periodSeconds"
grep -q 'timeoutSeconds: [3-9]' <<<"$readiness" || fail "readinessProbe timeoutSeconds must exceed the dependency check bound"
grep -q 'failureThreshold: [3-9]' <<<"$readiness" || fail "readinessProbe needs a failureThreshold of at least 3"
probe_block livenessProbe "$deployment" | grep -q 'failureThreshold: [3-9]' || fail "livenessProbe needs a failureThreshold of at least 3"

# --- container hardening ------------------------------------------------------
grep -q 'runAsNonRoot: true' <<<"$deployment" || fail "the pod must run as non-root"
grep -q 'readOnlyRootFilesystem: true' <<<"$deployment" || fail "the root filesystem must be read-only"
grep -q 'allowPrivilegeEscalation: false' <<<"$deployment" || fail "privilege escalation must be disabled"
grep -A2 'capabilities:' <<<"$deployment" | grep -q 'ALL' || fail "capabilities must drop ALL"

# --- graceful shutdown room ----------------------------------------------------
# SHUTDOWN_DRAIN_DELAY (5s) + HTTP drain 10s + consumers 10s + relay 10s = 35s.
grace="$(awk '/terminationGracePeriodSeconds:/ {print $2; exit}' <<<"$deployment")"
(( grace > 35 )) || fail "terminationGracePeriodSeconds is $grace; want more than 35 (drain delay + 3 x 10 s worker/HTTP bounds)"

# --- optional values reach the ConfigMap and the Deployment -------------------
tuned_args=("${base[@]}" --set config.outboxRelayInterval=7s --set config.outboxMaxAttempts=5 \
  --set config.shutdownDrainDelay=3s --set config.httpAddr=:9090 --set config.logLevel=debug)
tuned_cm="$(render_only configmap.yaml "${tuned_args[@]}")"
tuned_dep="$(render_only deployment.yaml "${tuned_args[@]}")"
for kv in 'OUTBOX_RELAY_INTERVAL: "7s"' 'OUTBOX_MAX_ATTEMPTS: "5"' 'SHUTDOWN_DRAIN_DELAY: "3s"' \
  'HTTP_ADDR: ":9090"' 'LOG_LEVEL: "debug"'; do
  grep -q "$kv" <<<"$tuned_cm" || fail "the ConfigMap does not render $kv"
done
for pair in OUTBOX_RELAY_INTERVAL=7s OUTBOX_MAX_ATTEMPTS=5 SHUTDOWN_DRAIN_DELAY=3s HTTP_ADDR=:9090 LOG_LEVEL=debug; do
  [[ "$(env_value "$tuned_dep" "${pair%%=*}")" == "${pair#*=}" ]] || fail "the Deployment does not render ${pair%%=*}=${pair#*=}"
done
# Unset optional values are not rendered at all, so the binary's defaults apply.
for var in OUTBOX_RELAY_INTERVAL SHUTDOWN_DRAIN_DELAY; do
  if grep -q "$var" <<<"$deployment"; then fail "$var must not be rendered while its value is empty"; fi
done
# The relay attempt cap defaults to 20 and 0 (unlimited) is rendered as is.
grep -q 'OUTBOX_MAX_ATTEMPTS: "20"' <<<"$configmap" || fail "OUTBOX_MAX_ATTEMPTS must default to 20"
unlimited="$(render_only configmap.yaml "${base[@]}" --set config.outboxMaxAttempts=0)"
grep -q 'OUTBOX_MAX_ATTEMPTS: "0"' <<<"$unlimited" || fail "config.outboxMaxAttempts=0 is not rendered"

# --- chart vs binary ------------------------------------------------------------
# Every env var cmd/api reads must be rendered into the Deployment when every
# optional value is set. The names come from the binary's own os.Getenv /
# getenv("...") literals and its *_CONSUMER_GROUP specs, so a new variable in
# cmd/api fails this test until the chart renders it. MIGRATIONS_DATABASE_URL
# is read by internal/adapters/outbound/postgres/migrate.go.
binary_env() {
  local f
  for f in "$repo_dir"/cmd/api/*.go; do
    case "$f" in *_test.go) continue ;; esac
    # sed, not grep -E: BSD grep's ERE matcher silently misses these
    # patterns (observed 2026-10-21); ubuntu CI is unaffected either way.
    sed -nE 's/.*[Gg]etenv\("([A-Z][A-Z0-9_]*)".*/\1/p' "$f"
    sed -nE 's/.*"([A-Z][A-Z0-9_]*_CONSUMER_GROUP)".*/\1/p' "$f"
  done | sort -u
}
# Word splitting is safe: the names are [A-Z0-9_] only (bash 3.2 has no mapfile).
# shellcheck disable=SC2207
api_vars=($(binary_env))
(( ${#api_vars[@]} >= 10 )) || fail "found only ${#api_vars[@]} env literals in cmd/api; the scan is looking in the wrong place"
full_deployment="$(render_only deployment.yaml "${everything[@]}")"
for var in "${api_vars[@]}" MIGRATIONS_DATABASE_URL; do
  grep -q "name: $var\$" <<<"$full_deployment" || fail "the Deployment does not render $var, which the binary reads"
done
# Every key the ConfigMap renders is a variable cmd/api reads (no dead keys).
full_configmap="$(render_only configmap.yaml "${everything[@]}")"
known=" ${api_vars[*]} "
while read -r key; do
  [[ "$known" == *" $key "* ]] || fail "ConfigMap key $key is read by no binary"
done < <(awk '/^data:/ {d=1; next} d && /^  [A-Z]/ {sub(/:.*/, ""); sub(/^  /, ""); print}' <<<"$full_configmap")

# --- the Secret ----------------------------------------------------------------
secret="$(render_only secret.yaml "${base[@]}")"
grep -q 'DATABASE_URL: "postgres://u@example.invalid:5432/db"' <<<"$secret" || fail "database.url does not land in the chart Secret"
existing="$(render --set database.existingSecret=my-db --set kafka.brokers=k:9092)"
if grep -q 'kind: Secret' <<<"$existing"; then fail "an existingSecret must suppress the chart Secret"; fi
grep -A4 'name: DATABASE_URL$' <<<"$existing" | grep -q 'name: my-db' || fail "an existingSecret is not referenced by DATABASE_URL"

# --- refusals: the mandatory values name themselves -------------------------------
expect_refusal() { # expect_refusal <why> <needle> <helm args...>
  local why="$1" needle="$2" err; shift 2
  if err="$(helm template "$release" "$chart_dir" "$@" 2>&1)"; then
    fail "the chart must refuse to render: $why"
  fi
  grep -qF -- "$needle" <<<"$err" || fail "the refusal for '$why' does not name '$needle':\n$err"
}
expect_refusal "no database source and no brokers" "database.url or database.existingSecret"
expect_refusal "no database source" "database.url or database.existingSecret" --set kafka.brokers=k:9092
expect_refusal "no brokers" "kafka.brokers" --set database.url=postgres://u@h/db
expect_refusal "an empty classification group" "config.productClassificationConsumerGroup" "${base[@]}" --set config.productClassificationConsumerGroup=
expect_refusal "an empty order-lines group" "config.orderLinesConsumerGroup" "${base[@]}" --set config.orderLinesConsumerGroup=
expect_refusal "an empty manifest group" "config.manifestConsumerGroup" "${base[@]}" --set config.manifestConsumerGroup=
expect_refusal "a short grace period" "terminationGracePeriodSeconds" "${base[@]}" --set terminationGracePeriodSeconds=20
expect_refusal "a *_MODE variable through extraEnv" "*_MODE" "${base[@]}" --set 'extraEnv[0].name=MANIFEST_MODE' --set 'extraEnv[0].value=permissive'
expect_refusal "a *_MODE variable through mcp.extraEnv" "*_MODE" "${base[@]}" --set mcp.enabled=true --set 'mcp.extraEnv[0].name=OUTBOX_MODE' --set 'mcp.extraEnv[0].value=kafka'
expect_refusal "a *_MODE variable through a2a.extraEnv" "*_MODE" "${base[@]}" --set a2a.enabled=true --set 'a2a.extraEnv[0].name=OUTBOX_MODE' --set 'a2a.extraEnv[0].value=kafka'

# --- the optional mcp, a2a and frontend components ---------------------------------
# Off by default: the default render is the api alone.
default_render="$(render "${base[@]}")"
if grep -q 'app.kubernetes.io/component: mcp$' <<<"$default_render"; then
  fail "the mcp component must not render by default"
fi
if grep -q 'app.kubernetes.io/component: a2a$' <<<"$default_render"; then
  fail "the a2a component must not render by default"
fi
if grep -q 'app.kubernetes.io/component: frontend$' <<<"$default_render"; then
  fail "the frontend component must not render by default"
fi

# A binary's env literals (os.Getenv / getenv("...")) -- for cmd/<dir> -- must all be
# rendered by its Deployment, so a new variable fails until the chart carries it.
cmd_env() { # cmd_env <dir>...
  local d f
  for d in "$@"; do
    for f in "$repo_dir/cmd/$d"/*.go; do
      case "$f" in *_test.go) continue ;; esac
      grep -hoE '(os\.Getenv|getenv)\("[A-Z][A-Z0-9_]*"' "$f" || true
    done
  done | sed -E 's/.*"([A-Z0-9_]+)".*/\1/' | sort -u
}
assert_renders_env() { # assert_renders_env <label> <rendered deployment> <dir>...
  local label="$1" rendered="$2" var; shift 2
  local vars; vars="$(cmd_env "$@")"
  [[ -n "$vars" ]] || fail "found no env literals in cmd/$* ($label); the scan is looking in the wrong place"
  for var in $vars; do
    grep -q "name: $var\$" <<<"$rendered" || fail "the $label Deployment does not render $var, which cmd/$* reads"
  done
}

# MCP: read-only, Postgres only, its own Service.
mcp_dep="$(render_only mcp-deployment.yaml "${base[@]}" --set mcp.enabled=true)"
mcp_svc="$(render_only mcp-service.yaml "${base[@]}" --set mcp.enabled=true)"
grep -q 'app.kubernetes.io/component: mcp' <<<"$mcp_dep" || fail "the mcp Deployment is not labelled component=mcp"
grep -q 'command: \["/app/mcp"\]' <<<"$mcp_dep" || fail "the mcp Deployment must run /app/mcp"
grep -A1 'selector:' <<<"$mcp_svc" >/dev/null && grep -q 'app.kubernetes.io/component: mcp' <<<"$mcp_svc" || fail "the mcp Service must select component=mcp"
grep -A4 'name: DATABASE_URL$' <<<"$mcp_dep" | grep -q 'secretKeyRef:' || fail "the mcp DATABASE_URL must come from a Secret"
if grep -Eq 'name: (KAFKA_BROKERS|[A-Z]+_CONSUMER_GROUP|OUTBOX_[A-Z_]+)$' <<<"$mcp_dep"; then
  fail "the mcp Deployment must not get Kafka/relay/consumer env (read-only, never dials Kafka)"
fi
no_mode "mcp Deployment" "$mcp_dep"
mcp_full="$(render_only mcp-deployment.yaml "${everything[@]}")"
assert_renders_env "mcp" "$mcp_full" mcp
[[ "$(env_value "$(render_only mcp-deployment.yaml "${base[@]}" --set mcp.enabled=true --set mcp.httpAddr=:9191)" MCP_ADDR)" == ":9191" ]] || fail "mcp.httpAddr does not reach MCP_ADDR"

# A2A: read-only Agent2Agent server (ADR 0008), Postgres only, its own Service.
a2a_dep="$(render_only a2a-deployment.yaml "${base[@]}" --set a2a.enabled=true)"
a2a_svc="$(render_only a2a-service.yaml "${base[@]}" --set a2a.enabled=true)"
grep -q 'app.kubernetes.io/component: a2a' <<<"$a2a_dep" || fail "the a2a Deployment is not labelled component=a2a"
grep -q 'command: \["/app/a2a"\]' <<<"$a2a_dep" || fail "the a2a Deployment must run /app/a2a"
grep -A1 'selector:' <<<"$a2a_svc" >/dev/null && grep -q 'app.kubernetes.io/component: a2a' <<<"$a2a_svc" || fail "the a2a Service must select component=a2a"
grep -q 'port: 8093' <<<"$a2a_svc" || fail "the a2a Service must listen on 8093"
grep -A4 'name: DATABASE_URL$' <<<"$a2a_dep" | grep -q 'secretKeyRef:' || fail "the a2a DATABASE_URL must come from a Secret"
if grep -Eq 'name: (KAFKA_BROKERS|EVENT_PUBLISHER|[A-Z]+_CONSUMER_GROUP|OUTBOX_[A-Z_]+)$' <<<"$a2a_dep"; then
  fail "the a2a Deployment must not get Kafka/relay/consumer env (read-only, never dials Kafka)"
fi
no_mode "a2a Deployment" "$a2a_dep"
a2a_full="$(render_only a2a-deployment.yaml "${everything[@]}")"
assert_renders_env "a2a" "$a2a_full" a2a
[[ "$(env_value "$(render_only a2a-deployment.yaml "${base[@]}" --set a2a.enabled=true --set a2a.httpAddr=:9193)" A2A_ADDR)" == ":9193" ]] || fail "a2a.httpAddr does not reach A2A_ADDR"
# A2A_AGENT_URL is mandatory in the binary: it defaults to the in-cluster
# Service DNS name (a pattern, the namespace is the release's) and is
# overridable via a2a.agentUrl.
a2a_url="$(env_value "$a2a_dep" A2A_AGENT_URL || true)"
[[ "$a2a_url" =~ ^http://$release-a2a\.[^.]+\.svc\.cluster\.local:8093/$ ]] || fail "A2A_AGENT_URL does not default to the in-cluster Service DNS name (got '$a2a_url')"
a2a_url="$(env_value "$(render_only a2a-deployment.yaml "${base[@]}" --set a2a.enabled=true --set a2a.agentUrl=https://agents.example.invalid/a2a/)" A2A_AGENT_URL || true)"
[[ "$a2a_url" == "https://agents.example.invalid/a2a/" ]] || fail "a2a.agentUrl does not reach A2A_AGENT_URL (got '$a2a_url')"
# Every mandatory variable the binary refuses to boot without ("<VAR> is
# required" in cmd/a2a/*.go) is rendered by default AND with every value set.
a2a_required="$(cat "$repo_dir"/cmd/a2a/*.go | grep -v '^//' | grep -ohE '[A-Z][A-Z0-9_]+ is required' | sed -E 's/ is required//' | sort -u || true)"
[[ -n "$a2a_required" ]] || fail "found no '<VAR> is required' boot errors in cmd/a2a; the scan is looking in the wrong place"
for var in $a2a_required; do
  grep -q "name: $var\$" <<<"$a2a_dep" || fail "the a2a Deployment (default values) does not render $var, which cmd/a2a refuses to boot without"
  grep -q "name: $var\$" <<<"$a2a_full" || fail "the a2a Deployment (every value set) does not render $var, which cmd/a2a refuses to boot without"
done

# --- the optional frontend component (returns_mfe remote) -------------------------
# Its own Deployment + ClusterIP Service, labelled component=frontend; nginx
# health probes; NO env (a static SPA that reads its API origin at runtime
# from the console's /config.json -- nothing about it is configured here);
# never a NodePort, ingress or route (the Nginx web gateway owns the path).
fe_dep="$(render_only frontend-deployment.yaml "${base[@]}" --set frontend.enabled=true)"
fe_svc="$(render_only frontend-service.yaml "${base[@]}" --set frontend.enabled=true)"
grep -q 'app.kubernetes.io/component: frontend' <<<"$fe_dep" || fail "the frontend Deployment is not labelled component=frontend"
grep -A1 'selector:' <<<"$fe_svc" >/dev/null && grep -q 'app.kubernetes.io/component: frontend' <<<"$fe_svc" || fail "the frontend Service must select component=frontend"
grep -q 'path: /healthz' <<<"$fe_dep" || fail "the frontend container must probe /healthz"
if grep -qE '^(        env:|          env:)' <<<"$fe_dep"; then
  fail "the frontend Deployment must render no env (its API origin is runtime /config.json)"
fi
if ! grep -q 'type: ClusterIP' <<<"$fe_svc"; then
  fail "the frontend Service must stay ClusterIP (the web gateway owns host exposure)"
fi
no_mode "frontend Deployment" "$fe_dep"

# --- the optional analytics components (ADR 0007) -----------------------------
# The projector (the analytical database's ONLY writer) and the read-only
# reports binary. Off by default; each is its own component; the projector
# gets no Service (its :8091 is probes only), the reports get one.
if grep -q 'app.kubernetes.io/component: analytics-' <<<"$default_render"; then
  fail "the analytics components must not render by default"
fi
an=(--set analytics.enabled=true --set analytics.url=postgres://u@example.invalid:5432/analytics)
prj_dep="$(render_only projector-deployment.yaml "${base[@]}" "${an[@]}")"
rep_dep="$(render_only reports-deployment.yaml "${base[@]}" "${an[@]}")"
grep -q 'app.kubernetes.io/component: analytics-projector' <<<"$prj_dep" || fail "the projector Deployment is not labelled component=analytics-projector"
grep -q 'command: \["/app/returns-projector"\]' <<<"$prj_dep" || fail "the projector Deployment must run /app/returns-projector"
grep -q 'path: /readyz' <<<"$prj_dep" || fail "the projector must probe /readyz (its readiness flips at shutdown)"
grep -A4 'name: ANALYTICS_DATABASE_URL$' <<<"$prj_dep" | grep -q 'secretKeyRef:' || fail "the projector ANALYTICS_DATABASE_URL must come from a Secret"
[[ "$(env_value "$prj_dep" ANALYTICS_CONSUMER_GROUP)" == "returns-processing-analytics" ]] || fail "analytics.consumerGroup does not reach ANALYTICS_CONSUMER_GROUP"
[[ "$(env_value "$prj_dep" KAFKA_BROKERS)" == "kafka.example.invalid:9092" ]] || fail "the projector KAFKA_BROKERS is not rendered from kafka.brokers"
if grep -Eq 'name: (DATABASE_URL|MIGRATIONS_DATABASE_URL|[A-Z]+_CONSUMER_GROUP)$' <<<"$prj_dep" && ! grep -q 'name: ANALYTICS_CONSUMER_GROUP$' <<<"$prj_dep"; then
  fail "the projector Deployment must not get OLTP env (it never opens the OLTP database)"
fi
no_mode "projector Deployment" "$prj_dep"

grep -q 'app.kubernetes.io/component: analytics-reports' <<<"$rep_dep" || fail "the reports Deployment is not labelled component=analytics-reports"
grep -q 'command: \["/app/returns-reports"\]' <<<"$rep_dep" || fail "the reports Deployment must run /app/returns-reports"
grep -A4 'name: ANALYTICS_DATABASE_URL$' <<<"$rep_dep" | grep -q 'secretKeyRef:' || fail "the reports ANALYTICS_DATABASE_URL must come from a Secret"
if grep -Eq 'name: (KAFKA_BROKERS|DATABASE_URL|MIGRATIONS_DATABASE_URL|[A-Z]+_CONSUMER_GROUP)$' <<<"$rep_dep"; then
  fail "the reports Deployment must not get Kafka/OLTP env (read-only, never dials Kafka)"
fi
no_mode "reports Deployment" "$rep_dep"

# Every env literal each analytics binary reads is rendered by its Deployment.
assert_renders_env "projector" "$(render_only projector-deployment.yaml "${base[@]}" "${an[@]}")" returns-projector
assert_renders_env "reports" "$(render_only reports-deployment.yaml "${base[@]}" "${an[@]}")" returns-reports

# The analytics Secret: created from analytics.url, suppressed by existingSecret.
an_secret="$(render_only analytics-secret.yaml "${base[@]}" "${an[@]}")"
grep -q 'ANALYTICS_DATABASE_URL: "postgres://u@example.invalid:5432/analytics"' <<<"$an_secret" || fail "analytics.url does not land in the analytics Secret"
an_existing="$(render "${base[@]}" --set analytics.enabled=true --set analytics.existingSecret=my-analytics-db)"
if grep -q 'name: returns-processing-analytics$' <<<"$an_existing"; then
  fail "an analytics.existingSecret must suppress the chart analytics Secret"
fi
grep -A4 'name: ANALYTICS_DATABASE_URL$' <<<"$an_existing" | grep -q 'name: my-analytics-db' || fail "analytics.existingSecret is not referenced by ANALYTICS_DATABASE_URL"

# Refusals: analytics on without a DSN source / with an empty consumer group.
expect_refusal "analytics without a DSN source" "analytics.url nor analytics.existingSecret" "${base[@]}" --set analytics.enabled=true
expect_refusal "analytics with an empty consumer group" "analytics.consumerGroup" "${base[@]}" --set analytics.enabled=true --set analytics.url=postgres://u@h/analytics --set analytics.consumerGroup=
expect_refusal "a *_MODE variable through analytics.projector.extraEnv" "*_MODE" "${base[@]}" --set analytics.enabled=true --set analytics.url=postgres://u@h/analytics --set 'analytics.projector.extraEnv[0].name=OUTBOX_MODE' --set 'analytics.projector.extraEnv[0].value=kafka'

# All components together: each labelled, each Service selecting exactly its own.
together="$(render "${base[@]}" --set mcp.enabled=true --set a2a.enabled=true --set frontend.enabled=true "${an[@]}")"
for component in api mcp a2a frontend analytics-projector analytics-reports; do
  grep -q "app.kubernetes.io/component: $component\$" <<<"$together" || fail "$component was not rendered when every component is enabled"
done

# --- the Gateway API routes + Kong plugins (fleet decision 2026-10-13) ------
# Off by default: standalone installs without Kong are unaffected.
if grep -q 'kind: HTTPRoute' <<<"$default_render"; then
  fail "an HTTPRoute must not render by default (gatewayApi.enabled is false)"
fi
if grep -q 'kind: KongPlugin' <<<"$default_render"; then
  fail "a KongPlugin must not render by default (every kongPlugins.*.enabled is false)"
fi
# The mcp route stays off until mcp.route.enabled, even with mcp on.
if render_only mcp-route.yaml "${base[@]}" --set mcp.enabled=true 2>&1 | grep -q 'kind: HTTPRoute'; then
  fail "the mcp route must stay off until mcp.route.enabled"
fi
# The a2a route stays off until a2a.route.enabled, even with a2a on.
if render_only a2a-route.yaml "${base[@]}" --set a2a.enabled=true 2>&1 | grep -q 'kind: HTTPRoute'; then
  fail "the a2a route must stay off until a2a.route.enabled"
fi
# With everything on (ct lint's values file): the api route strips
# /api/returns-processing, the mcp route rewrites to /mcp and both carry
# the konghq.com/plugins annotation naming three rendered KongPlugins (the
# deep contract — forbidden plugins, cors headers, backends — is
# tests/test_kong_config.py).
route="$(render_only httproute.yaml "${everything[@]}")"
grep -q 'kind: HTTPRoute' <<<"$route" || fail "httproute.yaml does not render with gatewayApi.enabled=true"
grep -q 'value: /api/returns-processing' <<<"$route" || fail "the api HTTPRoute does not match /api/returns-processing"
grep -q 'replacePrefixMatch: /' <<<"$route" || fail "the api HTTPRoute does not strip its prefix"
grep -q 'konghq.com/plugins:' <<<"$route" || fail "the api HTTPRoute has no konghq.com/plugins annotation"
mcp_route="$(render_only mcp-route.yaml "${everything[@]}")"
grep -q 'value: /api/mcp/returns-processing' <<<"$mcp_route" || fail "the mcp HTTPRoute does not match /api/mcp/returns-processing"
grep -q 'replacePrefixMatch: /mcp' <<<"$mcp_route" || fail "the mcp HTTPRoute does not rewrite to /mcp"
grep -q "name: $release-mcp" <<<"$mcp_route" || fail "the mcp HTTPRoute does not target the -mcp Service"
a2a_route="$(render_only a2a-route.yaml "${everything[@]}")"
grep -q 'value: /api/a2a/returns-processing' <<<"$a2a_route" || fail "the a2a HTTPRoute does not match /api/a2a/returns-processing"
grep -q 'replacePrefixMatch: /$' <<<"$a2a_route" || fail "the a2a HTTPRoute does not rewrite to /"
grep -q "name: $release-a2a" <<<"$a2a_route" || fail "the a2a HTTPRoute does not target the -a2a Service"
grep -q 'konghq.com/plugins:' <<<"$a2a_route" || fail "the a2a HTTPRoute has no konghq.com/plugins annotation"
plugins="$(render_only kong-plugins.yaml "${everything[@]}")"
for plugin in cors prometheus opentelemetry; do
  grep -q "plugin: $plugin" <<<"$plugins" || fail "the $plugin KongPlugin does not render when enabled"
done
if grep -q 'origins:\s*$' <<<"$plugins" && grep -A1 'origins:' <<<"$plugins" | grep -q '\*'; then
  fail "the cors plugin must never allow origin '*' (endpoints are unauthenticated)"
fi

echo "PASS: returns-processing chart render assertions"
