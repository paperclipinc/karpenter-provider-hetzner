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

{{/*
Chart name, overridable. Truncated to 63 characters because it is used to build
names that end up as label values, which the apiserver caps at 63.
*/}}
{{- define "karpenter-hetzner.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully-qualified resource name, scoped to the release.

Every resource in this chart used to carry the hardcoded name
"karpenter-provider-hetzner", which made a second release in the same namespace
impossible: Helm refuses to adopt an object owned by another release, so the
install failed before the label selectors below ever mattered.

The standard Helm scaffold is used deliberately, including the "release name
already contains the chart name" case: the documented install command names the
release `karpenter-provider-hetzner`, so the overwhelmingly common install keeps
exactly the names it had. `fullnameOverride` pins the old name for anyone whose
release is named something else.
*/}}
{{- define "karpenter-hetzner.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "karpenter-hetzner.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Name for the CLUSTER-scoped RBAC objects.

These are not namespaced, so two releases in *different* namespaces collide on
them just as two releases in one namespace collide on the Deployment. The
namespace is therefore part of the name, which is the only thing that makes the
pair unique cluster-wide.
*/}}
{{- define "karpenter-hetzner.clusterRoleName" -}}
{{- printf "%s-%s" (include "karpenter-hetzner.fullname" .) .Release.Namespace | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Selector labels: the minimal, STABLE identity of this release's pods.

`app.kubernetes.io/instance` is what stops two releases' controllers fighting
over each other's pods. Nothing version-bearing may appear here -- a Deployment's
.spec.selector is immutable, so a chart or app version in the selector would make
every upgrade fail.
*/}}
{{- define "karpenter-hetzner.selectorLabels" -}}
app.kubernetes.io/name: {{ include "karpenter-hetzner.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Full label set for object metadata. Superset of the selector labels; the extra
ones change across upgrades, which is exactly why they are not selectors.
*/}}
{{- define "karpenter-hetzner.labels" -}}
{{ include "karpenter-hetzner.selectorLabels" . }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Chart.AppVersion }}
app.kubernetes.io/version: {{ . | quote }}
{{- end }}
{{- end -}}

{{/*
ServiceAccount name. Defaults to the release-scoped fullname rather than the
bare "karpenter" it used to default to -- that name is also what the upstream
karpenter chart creates, so the two charts installed in one namespace silently
shared a ServiceAccount and one release's uninstall revoked the other's access.
*/}}
{{- define "karpenter-hetzner.serviceAccountName" -}}
{{- default (include "karpenter-hetzner.fullname" .) .Values.serviceAccount.name -}}
{{- end -}}
