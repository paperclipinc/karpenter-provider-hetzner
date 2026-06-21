<!-- Post as a GitHub Discussion on vitobotta/hetzner-k3s (per CONTRIBUTING: open a Discussion for feature suggestions first) -->
<!-- Title: Optional Karpenter-based autoscaling as an alternative to Cluster Autoscaler? -->

### Summary

Would you consider an **opt-in** Karpenter autoscaling backend using
[`karpenter-provider-hetzner`](https://github.com/paperclipinc/karpenter-provider-hetzner),
selectable per cluster instead of the current Cluster Autoscaler?

**Disclosure:** I help maintain `karpenter-provider-hetzner`. Following your
CONTRIBUTING guidance, I'm opening a Discussion before any PR to gauge interest.

### The motivation

`hetzner-k3s` already documents a real limitation of the CA path:

> "You can't specify different images for each autoscaled pool yet."

More broadly, the CA Hetzner provider pre-declares fixed pools, so you can't mix
instance types within a pool or get automatic consolidation. Karpenter is built
exactly for this: it picks the cheapest Hetzner type that fits each pending pod
(CX/CPX/CAX/CCX, amd64+arm64) and consolidates underused nodes.

### How it would fit hetzner-k3s

Karpenter is bootstrap-agnostic — Hetzner runs the node's `userData` as
cloud-init, so a k3s agent join works the same way your CA cloud-init does
today. The natural shape, mirroring how you already install the CA:

- A config switch, e.g. `autoscaling: { provider: karpenter | cluster_autoscaler }`.
- When `karpenter`, the CLI installs the provider (Helm) and generates the
  `HCloudNodeClass` + `NodePool` manifests, with `userData` doing the k3s agent
  join using the existing cluster token + server endpoint.
- Reuses the hcloud CCM you already install (it sets the `hcloud://<id>`
  providerID Karpenter needs; k3s servers run with `--disable-cloud-controller`
  and agents with `cloud-provider=external`).

I've written a worked k3s bootstrap example + guide for the provider so the join
path is concrete:
- https://github.com/paperclipinc/karpenter-provider-hetzner/blob/main/examples/k3s-nodeclass.yaml
- https://github.com/paperclipinc/karpenter-provider-hetzner/blob/main/docs/k3s-bootstrap.md

### Open questions

1. Is a second, opt-in autoscaler something you'd want in `hetzner-k3s`, or out
   of scope?
2. The provider is young — would you want it marked experimental / pinned until
   it has soak time?
3. Config ergonomics: a global `autoscaling.provider` switch, or per-pool?

Not looking to replace the CA — just to offer Karpenter where its
consolidation / per-pod sizing helps. Happy to prototype if you're open to it.
