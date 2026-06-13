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

The CRD is installed from `crds/` automatically by Helm.
