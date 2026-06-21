<!-- Post as a GitHub Discussion (Ideas) on hcloud-k8s/terraform-hcloud-kubernetes -->
<!-- Title: Optional Karpenter autoscaling (karpenter-provider-hetzner) as an alternative to Cluster Autoscaler? -->

### Summary

Would you be open to an **opt-in** Karpenter integration alongside the existing
Cluster Autoscaler, using [`karpenter-provider-hetzner`](https://github.com/paperclipinc/karpenter-provider-hetzner)?

**Disclosure:** I help maintain `karpenter-provider-hetzner`, so I have an
interest here. I'm raising this as a Discussion (not a surprise PR) precisely
because the module already has solid Hetzner autoscaling — I want to check
appetite before building anything.

### Why this module specifically

Of the popular Hetzner installers, this one is the cleanest fit for Karpenter:

- The provider already supports **Talos worker bootstrap**.
- This module already **generates Talos worker machineconfigs** and ships
  components as **Talos inline manifests** — so the integration reuses existing
  machinery (`data.talos_machine_configuration`, the `*_manifest` pattern)
  rather than adding new bootstrap logic.
- The hcloud CCM you already install provides the `hcloud://<id>` providerID
  Karpenter needs.

### What I'm proposing (not a replacement)

Karpenter is an *alternative* to the Cluster Autoscaler, not a gap-filler — it
does per-pod instance-type selection and consolidation instead of pre-declared
ASGs. So I'd keep it strictly opt-in:

- New `karpenter_nodepools` variable; empty by default → **zero change** for
  current users.
- A guard so Karpenter and `cluster_autoscaler_nodepools` can't both be enabled
  (both want to own provisioning).
- Follows `cluster_autoscaler.tf` exactly: `data.helm_template` to render the
  chart, a generated worker machineconfig as the node `userData`, manifests
  appended to `talos_inline_manifests`.

I have a working draft of `karpenter.tf` + variables ready to share.

### Open questions

1. Is an alternative autoscaler something you'd want to carry, or do you prefer
   to keep one autoscaling path?
2. `karpenter-provider-hetzner` is young — would you want a stability bar (e.g.
   N months / pinned chart, marked beta) before merge?
3. Preference on shape: single shared `HCloudNodeClass` + per-pool NodePools
   (what I drafted), or a richer per-pool NodeClass mapping?

Happy to open the PR if there's interest. Thanks for the module — it's the
nicest Talos-on-Hetzner setup out there.
