{{/*
Validate that value is a decimal integer in a small range; fail the render with
a clear message otherwise. The bound (0/1-999999) stays below 1e6 so Helm's
float64/scientific-notation coercion (e.g. 1000000 -> "1e+06") can't slip a
value past the regex. Set allowZero=true to permit 0 (replicas); otherwise >= 1.

Usage: {{ include "karpenter-hetzner.requireInt" (dict "value" .Values.replicas "name" "replicas" "allowZero" true) }}
*/}}
{{- define "karpenter-hetzner.requireInt" -}}
{{- $re := "^[1-9][0-9]{0,5}$" -}}
{{- $range := "1-999999" -}}
{{- if .allowZero -}}
{{- $re = "^(0|[1-9][0-9]{0,5})$" -}}
{{- $range = "0-999999" -}}
{{- end -}}
{{- if not (regexMatch $re (printf "%v" .value)) -}}
{{- fail (printf "%s must be an integer (%s), got %v" .name $range .value) -}}
{{- end -}}
{{- end -}}
