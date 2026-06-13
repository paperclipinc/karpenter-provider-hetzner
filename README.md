# karpenter-provider-hetzner

[![CI](https://github.com/paperclipinc/karpenter-provider-hetzner/actions/workflows/ci.yaml/badge.svg)](https://github.com/paperclipinc/karpenter-provider-hetzner/actions/workflows/ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/paperclipinc/karpenter-provider-hetzner)](https://goreportcard.com/report/github.com/paperclipinc/karpenter-provider-hetzner)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)

A [Karpenter](https://karpenter.sh) cloud provider for [Hetzner Cloud](https://www.hetzner.com/cloud). It provisions, bin-packs, and autoscales Hetzner Cloud servers as Kubernetes nodes, picking the cost-optimal server type for pending pods from Hetzner's real-time pricing.

## Status

**Alpha.** The provider is correct and deployable, with unit and controller test coverage. End-to-end validation on a live cluster is in progress; treat it as alpha and pin a released image tag rather than `latest` in production.

## Features

- **All Hetzner Cloud server types** — CX, CPX, CAX, CCX, including ARM (CAX).
- **Cost-optimal scheduling** — offerings are priced from Hetzner's live per-hour net price, so Karpenter bin-packs onto the cheapest type that fits.
- **Capacity-aware** — when Hetzner reports a type as unavailable in a location, that offering is skipped for a short window so scheduling falls back to an alternative instead of looping on a sold-out combination.
- **Drift detection** — image, network, firewall, and server-type drift trigger node replacement.
- **Talos Linux and Ubuntu images**, resolved per architecture.
- **Placement groups** for spreading nodes across physical hosts.
- **Cost controls** — opt out of the billed public IPv4 (and/or IPv6) per node class for private-network clusters.
- **Multi-cluster safe** — every managed server is tagged with the cluster name, so several clusters can share one Hetzner project without touching each other's nodes.

## How it works

```
┌──────────────────────────────────────────────────────────┐
│ Karpenter core (provisioning, disruption, NodeClaim GC)    │
└───────────────┬────────────────────────────────────────────┘
                │ CloudProvider interface
┌───────────────▼────────────────────────────────────────────┐
│ karpenter-provider-hetzner                                  │
│  • instance      — hcloud server create/delete/get/list     │
│  • instancetype  — server types → priced InstanceTypes      │
│  • imagefamily   — resolve Talos/Ubuntu images per arch     │
│  • nodeclass ctrl— validate HCloudNodeClass, set Ready      │
└───────────────┬────────────────────────────────────────────┘
                │ hcloud API
        ┌───────▼────────┐
        │  Hetzner Cloud  │
        └─────────────────┘
```

A `NodePool` references an `HCloudNodeClass`. When pods are unschedulable, Karpenter asks this provider for instance types, picks the cheapest compatible offering, creates the server, and the node joins the cluster.

## Installation

You need a Hetzner Cloud API token with read/write access and a Kubernetes cluster on Hetzner (the [hcloud Cloud Controller Manager](https://github.com/hetznercloud/hcloud-cloud-controller-manager) should set node provider IDs as `hcloud://<id>`).

```bash
kubectl create secret generic hcloud-token \
  --namespace kube-system \
  --from-literal=token=$HCLOUD_TOKEN

helm install karpenter-provider-hetzner \
  oci://ghcr.io/paperclipinc/charts/karpenter-provider-hetzner \
  --namespace kube-system \
  --set clusterName=my-cluster \
  --set auth.secretRef.name=hcloud-token
```

`clusterName` is **required** — it scopes which servers this controller manages. The controller fails fast if it is unset.

The CRD ships in the chart's `crds/` directory and is installed automatically by Helm.

## Usage

Create an `HCloudNodeClass` describing how nodes are built, and a `NodePool` describing what Karpenter may provision:

```yaml
apiVersion: karpenter.hetzner.cloud/v1alpha1
kind: HCloudNodeClass
metadata:
  name: default
spec:
  locations: [nbg1, fsn1]          # Hetzner locations to schedule into
  networkID: 123456                # private network the nodes join
  imageSelector:
    family: talos                  # talos | ubuntu
    version: "v1.9"                # optional substring match
  firewallIDs: [987654]            # optional
  sshKeyIDs: [42]                  # optional
  placementGroupStrategy: spread   # spread | none (default spread)
  enablePublicIPv4: false          # default true; false saves the primary-IPv4 charge
  userData: |                      # cloud-init / Talos machine config (or use userDataSecretRef to source from a Secret)
    ...
---
apiVersion: karpenter.sh/v1
kind: NodePool
metadata:
  name: default
spec:
  template:
    spec:
      nodeClassRef:
        group: karpenter.hetzner.cloud
        kind: HCloudNodeClass
        name: default
      requirements:
        - key: kubernetes.io/arch
          operator: In
          values: [amd64, arm64]
        - key: karpenter.hetzner.cloud/server-family
          operator: In
          values: [cax, cpx]
  limits:
    cpu: "100"
  disruption:
    consolidationPolicy: WhenEmptyOrUnderutilized
```

### Node labels

Provisioned nodes carry, in addition to the well-known Karpenter labels:

| Label | Example | Meaning |
|-------|---------|---------|
| `topology.kubernetes.io/zone` | `nbg1` | Hetzner location |
| `node.kubernetes.io/instance-type` | `cax21` | Hetzner server type |
| `karpenter.hetzner.cloud/server-family` | `cax` | Type family (cx/cpx/cax/ccx) |
| `karpenter.hetzner.cloud/cpu-type` | `shared` | `shared` or `dedicated` |

## HCloudNodeClass reference

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `locations` | `[]string` | yes | — | Hetzner locations (min 1) |
| `networkID` | `int64` | yes | — | Private network ID nodes attach to |
| `imageSelector.family` | `talos`\|`ubuntu` | yes | — | OS image family |
| `imageSelector.version` | `string` | no | newest | Version substring to match |
| `firewallIDs` | `[]int64` | no | — | Firewalls to attach |
| `sshKeyIDs` | `[]int64` | no | — | SSH keys to install |
| `placementGroupStrategy` | `spread`\|`none` | no | `spread` | Placement-group behavior |
| `enablePublicIPv4` | `bool` | no | `true` | Attach a public IPv4 (billed) |
| `enablePublicIPv6` | `bool` | no | `true` | Attach a public IPv6 |
| `labels` | `map[string]string` | no | — | Extra labels on the Hetzner server |
| `userData` | `string` | no | — | cloud-init / Talos machine config |
| `userDataSecretRef` | `object {namespace, name, key}` | no | — | Source `userData` from a Secret (keeps secret bootstrap data out of git); takes precedence over `userData` |

Status exposes `conditions` (`ImagesReady`, `NetworkReady`, `ResourcesReady`, aggregated into `Ready`) and `resolvedImages` (image ID per architecture).

## Configuration

| Env var | Required | Description |
|---------|----------|-------------|
| `HCLOUD_TOKEN` | yes | Hetzner Cloud API token |
| `CLUSTER_NAME` | yes | Cluster identifier; scopes managed servers |
| `METRICS_PORT` | no (8080) | Prometheus metrics port |
| `HEALTH_PROBE_PORT` | no (8081) | Health/readiness probe port |

## Cost notes

- Pricing uses the **net** hourly figure, so relative comparisons match your Hetzner invoice (before VAT).
- Hetzner bills the primary IPv4 separately. On private-network clusters, set `enablePublicIPv4: false` to drop it.
- ARM (CAX) types are typically the best price/performance; constrain `kubernetes.io/arch` or `server-family` in the NodePool to steer Karpenter.

## Development

See [CONTRIBUTING.md](CONTRIBUTING.md). Quick start:

```bash
make test       # unit + controller tests (race)
make lint       # golangci-lint
make generate   # regenerate CRD + deepcopy
make build      # build the controller binary
```

## Contributing

Contributions are welcome — please read [CONTRIBUTING.md](CONTRIBUTING.md) and the [Code of Conduct](CODE_OF_CONDUCT.md).

## Security

To report a vulnerability, see [SECURITY.md](SECURITY.md). Please do not open public issues for security reports.

## License

[Apache 2.0](LICENSE).
