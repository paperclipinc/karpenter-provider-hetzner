# karpenter-provider-hetzner (Helm chart)

Deploys the Karpenter cloud provider for Hetzner Cloud. See the [project README](../../README.md) for concepts and usage.

## Install

```bash
kubectl create secret generic hcloud-token -n kube-system --from-literal=token=$HCLOUD_TOKEN

helm install karpenter-provider-hetzner ./charts/karpenter-provider-hetzner \
  --namespace kube-system \
  --set clusterName=my-cluster \
  --set auth.secretRef.name=hcloud-token
```

`clusterName` is required; the controller fails to start without it.

## Upgrading to 2.0.0 (CRD `v1`)

Chart 2.0.0 graduates the `HCloudNodeClass` CRD from
`karpenter.hetzner.cloud/v1alpha1` to the stable `/v1`. There is **no
conversion webhook**, so this is a breaking change:

```bash
helm upgrade karpenter-provider-hetzner ./charts/karpenter-provider-hetzner -n kube-system
# Helm does not upgrade CRDs in crds/ on its own — apply the new CRD, then
# re-apply your node classes with apiVersion: karpenter.hetzner.cloud/v1:
kubectl apply -f charts/karpenter-provider-hetzner/crds/
kubectl apply -f your-nodeclasses-v1.yaml
```

Existing `v1alpha1` objects are not migrated automatically; recreate them under
`v1` (the spec is unchanged — only the `apiVersion` differs).

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `clusterName` | `""` (required) | Scopes which servers the controller manages |
| `replicas` | `1` | Controller replicas |
| `image.repository` | `ghcr.io/paperclipinc/karpenter-provider-hetzner` | Image |
| `image.tag` | `""` | Empty tracks the chart appVersion; pin a tag in production |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `auth.secretRef.name` | `hcloud-token` | Secret holding the Hetzner token |
| `auth.secretRef.key` | `token` | Key within the secret |
| `serviceAccount.create` | `true` | Create the service account |
| `serviceAccount.name` | `karpenter` | Service account name |
| `metrics.port` | `8080` | Prometheus metrics port |
| `healthProbe.port` | `8081` | Health/readiness probe port |
| `resources` | see values.yaml | Container resources |
| `nodeSelector` | `{}` | Pin the controller pod to specific nodes |
| `affinity` | `{}` | `nodeAffinity` / `podAntiAffinity` |
| `tolerations` | `[]` | Tolerate node taints |
| `topologySpreadConstraints` | `[]` | Spread replicas across nodes/zones; needs a labelSelector matching the pod labels |
| `imagePullSecrets` | `[]` | Pull from a private registry mirror |
| `priorityClassName` | `""` | Pod priority class |
| `podDisruptionBudget.enabled` | `false` | Create a PodDisruptionBudget (enable with `replicas > 1`) |
| `podDisruptionBudget.minAvailable` | unset | Positive integer; mutually exclusive with `maxUnavailable` |
| `podDisruptionBudget.maxUnavailable` | `1` (default) | Positive integer; never blocks drains (run `replicas >= 2` for availability) |
| `command` | `[]` | Override the container entrypoint (advanced) |
| `args` | `[]` | Controller flags, e.g. `--log-level`, `--feature-gates` |
| `serviceMonitor.enabled` | `false` | Deploy a Service + ServiceMonitor for Prometheus Operator |
| `serviceMonitor.interval` | `30s` | Scrape interval |
| `serviceMonitor.additionalLabels` | `{}` | Extra labels on the ServiceMonitor (for Prometheus Operator selector) |

The CRD is installed from `crds/` automatically by Helm.

## Scheduling

`nodeSelector`, `affinity`, `tolerations`, `topologySpreadConstraints`,
`imagePullSecrets` and `priorityClassName` pass straight through to the
Deployment pod spec; `command` and `args` pass through to the container (the
hardcoded probes expect `/healthz` and `/readyz`, so a custom command must serve
them). All default to empty. Leader election is enabled by default, so `replicas > 1` won't
double-reconcile — only the leader acts. For drain safety with `replicas > 1`,
enable `podDisruptionBudget`: it defaults to `maxUnavailable: 1`, which never
blocks drains (a single replica can still be evicted, so run `replicas >= 2` to
keep the controller available). Configs that would block all voluntary drains
(`minAvailable >= replicas` or `maxUnavailable: 0`) are rejected by design —
checked at install/upgrade, so runtime scaling is the operator's responsibility.

## Prometheus Operator integration

When `serviceMonitor.enabled=true` the chart creates:

- a `Service` named `karpenter-provider-hetzner-metrics` exposing port `http-metrics`
- a `ServiceMonitor` that selects that Service and scrapes `/metrics` at the configured interval

Requires the [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator) CRDs to be present. The controller exposes provider metrics under the `karpenter_hetzner_` prefix (server creates/deletes, durations, drift reasons, instance-type cache hits/misses, and raw hcloud API call counts).
