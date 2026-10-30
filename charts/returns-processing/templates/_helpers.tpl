{{/*
Expand the name of the chart.
*/}}
{{- define "returns-processing.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "returns-processing.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version as used by the chart label.
*/}}
{{- define "returns-processing.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "returns-processing.labels" -}}
helm.sh/chart: {{ include "returns-processing.chart" . }}
{{ include "returns-processing.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels. The Deployment and the Service ALSO pin
app.kubernetes.io/component (api / mcp), so a Service never selects more
than the pods of one component (tests/test_service_selectors.py).
*/}}
{{- define "returns-processing.selectorLabels" -}}
app.kubernetes.io/name: {{ include "returns-processing.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "returns-processing.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "returns-processing.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Name of the Secret holding DATABASE_URL: the operator's own
(database.existingSecret) or the one this chart creates from database.url.
*/}}
{{- define "returns-processing.databaseSecretName" -}}
{{- if .Values.database.existingSecret }}
{{- .Values.database.existingSecret }}
{{- else }}
{{- include "returns-processing.fullname" . }}-database
{{- end }}
{{- end }}

{{/*
Image reference.
*/}}
{{- define "returns-processing.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}

{{/*
MANDATORY (ADR 0001: Postgres is the only runtime). The binary exits at boot
without DATABASE_URL, so a render without a database source is never a
deployable state: surface it at render time, naming the values.
*/}}
{{- define "returns-processing.requireDatabase" -}}
{{- if not (or .Values.database.url .Values.database.existingSecret) -}}
{{- fail "returns-processing requires database.url or database.existingSecret to be set (DATABASE_URL): the service runs on real Postgres only and exits at boot without it (ADR 0001)." -}}
{{- end -}}
{{- end -}}

{{/*
MANDATORY (ADR 0001: Kafka is the only runtime). The binary exits at boot
without KAFKA_BROKERS; there is no default broker.
*/}}
{{- define "returns-processing.requireKafka" -}}
{{- $_ := required "returns-processing requires kafka.brokers to be set (KAFKA_BROKERS, comma-separated): the service runs on real Kafka only and exits at boot without it (ADR 0001)." .Values.kafka.brokers -}}
{{- end -}}

{{/*
MANDATORY: the three local-copy consumers always run, each with a STABLE
group id read from the environment (cmd/api loadBootConfig exits at boot
without one). The group ids live in values (config.*ConsumerGroup), never
in a template literal.
*/}}
{{- define "returns-processing.requireConsumerGroups" -}}
{{- $_ := required "returns-processing requires config.productClassificationConsumerGroup to be set (PRODUCT_CLASSIFICATION_CONSUMER_GROUP): a stable Kafka group id; the binary exits at boot without it." .Values.config.productClassificationConsumerGroup -}}
{{- $_ := required "returns-processing requires config.orderLinesConsumerGroup to be set (ORDER_LINES_CONSUMER_GROUP): a stable Kafka group id; the binary exits at boot without it." .Values.config.orderLinesConsumerGroup -}}
{{- $_ := required "returns-processing requires config.manifestConsumerGroup to be set (MANIFEST_CONSUMER_GROUP): a stable Kafka group id; the binary exits at boot without it." .Values.config.manifestConsumerGroup -}}
{{- end -}}

{{/*
Shutdown is sequential: SHUTDOWN_DRAIN_DELAY (default 5s) + HTTP drain 10s +
consumers 10s + outbox relay 10s = 35s in the worst case. A shorter grace
period gets the pod SIGKILLed mid-drain.
*/}}
{{- define "returns-processing.requireGracePeriod" -}}
{{- if lt (int .Values.terminationGracePeriodSeconds) 35 -}}
{{- fail (printf "terminationGracePeriodSeconds is %v — want at least 35: SHUTDOWN_DRAIN_DELAY (5s default) + 10s HTTP drain + 10s consumers + 10s outbox relay." .Values.terminationGracePeriodSeconds) -}}
{{- end -}}
{{- end -}}

{{/*
There is no in-memory mode (ADR 0001) and no *_MODE variable exists in the
binary's contract: a *_MODE name injected through extraEnv is a typo or a
copy-paste from another chart, so refuse it rather than render a variable
nothing reads.
*/}}
{{- define "returns-processing.requireNoModeEnv" -}}
{{- range .Values.extraEnv -}}
{{- if hasSuffix "_MODE" (toString .name) -}}
{{- fail (printf "extraEnv sets %s — no *_MODE variable exists in this service's contract (ADR 0001: real Postgres and real Kafka are the only runtime)." .name) -}}
{{- end -}}
{{- end -}}
{{- range .Values.mcp.extraEnv -}}
{{- if hasSuffix "_MODE" (toString .name) -}}
{{- fail (printf "mcp.extraEnv sets %s — no *_MODE variable exists in this service's contract (ADR 0001: real Postgres and real Kafka are the only runtime)." .name) -}}
{{- end -}}
{{- end -}}
{{- range .Values.a2a.extraEnv -}}
{{- if hasSuffix "_MODE" (toString .name) -}}
{{- fail (printf "a2a.extraEnv sets %s — no *_MODE variable exists in this service's contract (ADR 0001: real Postgres and real Kafka are the only runtime)." .name) -}}
{{- end -}}
{{- end -}}
{{- range .Values.analytics.projector.extraEnv -}}
{{- if hasSuffix "_MODE" (toString .name) -}}
{{- fail (printf "analytics.projector.extraEnv sets %s — no *_MODE variable exists in this service's contract (ADR 0001: real Postgres and real Kafka are the only runtime)." .name) -}}
{{- end -}}
{{- end -}}
{{- range .Values.analytics.reports.extraEnv -}}
{{- if hasSuffix "_MODE" (toString .name) -}}
{{- fail (printf "analytics.reports.extraEnv sets %s — no *_MODE variable exists in this service's contract (ADR 0001: real Postgres and real Kafka are the only runtime)." .name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Fully qualified name of the optional mcp component. It is its own workload
with its own app.kubernetes.io/component label; a Service never selects
more than one component (tests/test_service_selectors.py).
*/}}
{{- define "returns-processing.mcpFullname" -}}
{{- include "returns-processing.fullname" . }}-mcp
{{- end }}

{{/*
Fully qualified name of the optional a2a component (the Agent2Agent server of
cmd/a2a, ADR 0008). Its own workload with its own app.kubernetes.io/component
label; a Service never selects more than one component
(tests/test_service_selectors.py).
*/}}
{{- define "returns-processing.a2aFullname" -}}
{{- include "returns-processing.fullname" . }}-a2a
{{- end }}

{{/*
Fully qualified names of the analytics components (ADR 0007). Each is its own
workload with its own app.kubernetes.io/component label; the reports Service
never selects more than one of them (tests/test_service_selectors.py).
*/}}
{{- define "returns-processing.analyticsProjectorFullname" -}}
{{- include "returns-processing.fullname" . }}-projector
{{- end }}

{{- define "returns-processing.analyticsReportsFullname" -}}
{{- include "returns-processing.fullname" . }}-reports
{{- end }}

{{/*
Name of the Secret holding ANALYTICS_DATABASE_URL: the operator's own
(analytics.existingSecret) or the one this chart creates from analytics.url
(analytics-secret.yaml).
*/}}
{{- define "returns-processing.analyticsSecretName" -}}
{{- if .Values.analytics.existingSecret -}}
{{- .Values.analytics.existingSecret -}}
{{- else -}}
{{- include "returns-processing.fullname" . }}-analytics
{{- end -}}
{{- end }}

{{/*
Analytics guard (ADR 0007): the projector and reports binaries exit at boot
without ANALYTICS_DATABASE_URL, and the projector without KAFKA_BROKERS (always
required by this chart) and ANALYTICS_CONSUMER_GROUP (no default). Refuse them
at render time, only while analytics is on, naming the value.
*/}}
{{- define "returns-processing.requireAnalyticsConfig" -}}
{{- if not (or .Values.analytics.url .Values.analytics.existingSecret) -}}
{{- fail "analytics.enabled is true but neither analytics.url nor analytics.existingSecret is set: the projector and reports binaries exit at boot without ANALYTICS_DATABASE_URL (ADR 0007)." -}}
{{- end -}}
{{- $_ := required "analytics.enabled is true but analytics.consumerGroup is empty: the projector exits at boot without ANALYTICS_CONSUMER_GROUP, a stable Kafka group id (ADR 0007)." .Values.analytics.consumerGroup -}}
{{- end -}}

{{/*
Comma-joined names of this chart's enabled KongPlugins, for the
konghq.com/plugins annotation on the HTTPRoutes this chart renders
(httproute.yaml, mcp-route.yaml). Empty string (annotation omitted) when
none are enabled.
*/}}
{{- define "returns-processing.kongPluginNames" -}}
{{- $names := list -}}
{{- range $name, $cfg := .Values.kongPlugins -}}
{{- if $cfg.enabled -}}
{{- $names = append $names (printf "%s-%s" (include "returns-processing.fullname" $) $name) -}}
{{- end -}}
{{- end -}}
{{- join "," $names -}}
{{- end -}}
