# hcloud-k8s/terraform-hcloud-kubernetes — Karpenter integration

A proposal to add `karpenter-provider-hetzner` as an **opt-in alternative** to
the module's existing Cluster Autoscaler. Talos is the best-fit target of the
three popular Hetzner installers: the provider already supports Talos worker
bootstrap, and this module already generates Talos worker machineconfigs and
ships components as Talos inline manifests — so the integration reuses existing
machinery rather than adding new bootstrap logic.

## Why opt-in (and why this isn't a drop-in replacement)

The module already provides Hetzner node autoscaling via the Cluster Autoscaler
(`cluster_autoscaler_nodepools`). Karpenter is an *alternative* with different
trade-offs (per-pod instance-type selection, consolidation, no pre-declared
ASGs) — not a gap-filler. So:

- It is **disabled by default** (`karpenter_nodepools = []`).
- A guard prevents enabling Karpenter and the Cluster Autoscaler simultaneously
  (both want to own provisioning).
- `karpenter-provider-hetzner` is young; pin a released chart version and treat
  it as beta in this module until it has soak time.

## Files in this proposal

| File | Goes to | Purpose |
|--|--|--|
| `karpenter.tf` | repo root | locals + `data.helm_template` + machineconfig + manifests, mirroring `cluster_autoscaler.tf` |
| `karpenter.variables.tf` | merge into `variables.tf` | `karpenter_nodepools` + chart vars |

## One-line wiring

In `talos_config_control_plane.tf`, add Karpenter to the inline-manifest list:

```hcl
talos_inline_manifests = concat(
  ...
  local.cluster_autoscaler_manifest != null ? [local.cluster_autoscaler_manifest] : [],
  local.karpenter_manifest          != null ? [local.karpenter_manifest]          : [],   # add
  ...
)
```

## How it works

1. **Install** — `data.helm_template.karpenter` renders the OCI chart; the
   controller authenticates with the existing `hcloud` Secret (same one the
   Cluster Autoscaler uses).
2. **Bootstrap** — `data.talos_machine_configuration.karpenter` generates a
   Talos *worker* machineconfig from `local.talos_base_config_patches` (the same
   base the static workers and autoscaler use). It is shipped as a Secret and
   referenced by the `HCloudNodeClass` via `userDataSecretRef`. Karpenter passes
   it verbatim as the server's userData; Talos consumes it on first boot.
3. **Image** — the `HCloudNodeClass.imageSelector.selector` reuses
   `local.image_label_selector`, so Karpenter resolves the *same* Talos snapshot
   (per architecture) as the rest of the fleet.
4. **Network / CCM** — nodes attach to `worker_shared` subnet; the module's
   hcloud CCM (already installed) assigns the `hcloud://<id>` providerID
   Karpenter needs.

## Example usage

```hcl
module "kubernetes" {
  # ... existing config ...

  # Leave cluster_autoscaler_nodepools empty when using Karpenter.
  karpenter_nodepools = [
    {
      name            = "general"
      locations       = ["nbg1", "fsn1"]
      architectures   = ["amd64"]
      server_families = ["cpx"]
      limits          = { cpu = "100", memory = "400Gi" }
    },
    {
      name            = "arm"
      locations       = ["nbg1", "fsn1"]
      architectures   = ["arm64"]
      server_families = ["cax"]   # best price/vCPU
      limits          = { cpu = "200" }
    },
  ]
}
```

## Validation status

This draft references the module's existing locals/resources but has **not** been
run through `terraform validate`/`plan` in isolation (it depends on module
internals). Before opening the PR: drop the two files in, wire the one line, and
`terraform plan` against a test cluster; confirm a pending pod triggers a
Hetzner server create and the node joins with `providerID: hcloud://…`.

> Recommended process: open a GitHub Discussion first (see
> `../discussion-posts/hcloud-k8s.md`) to confirm maintainer appetite before
> sending the PR.
