# Integrating karpenter-provider-hetzner with Hetzner installers

Guidance and ready-to-use drafts for offering Karpenter autoscaling on top of
the popular Hetzner Kubernetes installers.

## Strategy

All three target installers **already ship the Cluster Autoscaler**, so
Karpenter is an *alternative* (per-pod instance-type selection + consolidation),
not a missing feature. Two consequences shape every integration here:

1. **Opt-in only.** Default-off; never replace the existing autoscaler. Guard
   against running both at once.
2. **Discussion before PR.** All three projects ask for it, none has a prior
   Karpenter thread, and the provider is young — lead by gauging appetite and
   disclosing the maintainer affiliation.

## Targets

| Installer | Distro | Fit | Status of draft here |
|--|--|--|--|
| [hcloud-k8s/terraform-hcloud-kubernetes](https://github.com/hcloud-k8s/terraform-hcloud-kubernetes) | Talos | **Best** — provider has Talos bootstrap; module already does worker machineconfig + inline manifests | `karpenter.tf` proposal + wiring guide |
| [vitobotta/hetzner-k3s](https://github.com/vitobotta/hetzner-k3s) | k3s / Ubuntu | **Medium** — CLI-driven; k3s userData now documented; addresses a real CA limitation | discussion post + k3s example/guide in repo |
| [kube-hetzner](https://github.com/kube-hetzner/terraform-hcloud-kube-hetzner) | k3s / MicroOS | **Hardest** — immutable MicroOS bootstrap is OS-specific | discussion post (feasibility-first) |

## Contents

- `terraform-hcloud-kubernetes/` — proposed `karpenter.tf`, `karpenter.variables.tf`,
  and `INTEGRATION.md` (the lighthouse integration).
- `discussion-posts/` — copy-paste GitHub Discussion drafts for all three.

## Prerequisite work (in this repo)

The k3s targets needed a worked k3s bootstrap path, which the provider now has:
- [`examples/k3s-nodeclass.yaml`](../../examples/k3s-nodeclass.yaml)
- [`docs/k3s-bootstrap.md`](../k3s-bootstrap.md)

## Recommended sequence

1. Post the three Discussions; gauge appetite.
2. Land the **hcloud-k8s (Talos)** integration first — cleanest fit, draft ready.
3. For **hetzner-k3s**, prototype the `autoscaling.provider` switch if the
   maintainer is interested.
4. For **kube-hetzner**, prove a MicroOS+k3s bootstrap end-to-end *before* any
   module PR.
