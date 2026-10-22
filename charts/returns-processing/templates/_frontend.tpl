{{/*
Fully qualified name of the frontend Module Federation remote deployment/service.

The remote (web/, `returns_mfe`) is served by its own nginx pod and reached
through warehouse-infra's Nginx web gateway at /mfes/returns-processing/.
It is deliberately a separate workload from the API: Kong never routes to it,
and the api Service must never select it (component=frontend vs
component=api). Kept in its own helper file so the frontend component stays
self-contained (templates/frontend-*.yaml + this file + the frontend: values
block).
*/}}
{{- define "returns-processing.frontendFullname" -}}
{{- include "returns-processing.fullname" . }}-frontend
{{- end }}
