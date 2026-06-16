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

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `clusterName` | `""` (required) | Scopes which servers the controller manages |
| `replicas` | `1` | Controller replicas |
| `image.repository` | `ghcr.io/paperclipinc/karpenter-provider-hetzner` | Image |
| `image.tag` | `latest` | Pin a released tag in production |
| `auth.secretRef.name` | `hcloud-token` | Secret holding the Hetzner token |
| `auth.secretRef.key` | `token` | Key within the secret |
| `serviceAccount.create` | `true` | Create the service account |
| `serviceAccount.name` | `karpenter` | Service account name |
| `metrics.port` | `8080` | Prometheus metrics port |
| `healthProbe.port` | `8081` | Health/readiness probe port |
| `resources` | see values.yaml | Container resources |
| `serviceMonitor.enabled` | `false` | Deploy a Service + ServiceMonitor for Prometheus Operator |
| `serviceMonitor.interval` | `30s` | Scrape interval |
| `serviceMonitor.additionalLabels` | `{}` | Extra labels on the ServiceMonitor (for Prometheus Operator selector) |

The CRD is installed from `crds/` automatically by Helm.

## Prometheus Operator integration

When `serviceMonitor.enabled=true` the chart creates:

- a `Service` named `karpenter-provider-hetzner-metrics` exposing port `http-metrics`
- a `ServiceMonitor` that selects that Service and scrapes `/metrics` at the configured interval

Requires the [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator) CRDs to be present. The controller exposes provider metrics under the `karpenter_hetzner_` prefix (server creates/deletes, durations, drift reasons, instance-type cache hits/misses, and raw hcloud API call counts).
