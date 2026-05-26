# karpenter-provider-hetzner

Karpenter cloud provider for [Hetzner Cloud](https://www.hetzner.com/cloud). Enables intelligent node provisioning, bin-packing, and autoscaling on Hetzner Cloud servers.

## Status

**Alpha.** Not yet production-ready.

## Features

- All Hetzner Cloud server types (CX, CPX, CAX, CCX) including ARM
- Cost-optimal scheduling via real-time Hetzner pricing
- Drift detection (image, server type, network, firewall)
- Talos Linux and Ubuntu image support
- Placement group support for high availability

## Installation

```bash
helm install karpenter-provider-hetzner charts/karpenter-provider-hetzner \
  --namespace kube-system \
  --set auth.secretRef.name=hcloud-token
```

## License

Apache 2.0
