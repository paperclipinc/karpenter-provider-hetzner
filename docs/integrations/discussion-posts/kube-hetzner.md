<!-- Post as a GitHub Discussion on kube-hetzner/terraform-hcloud-kube-hetzner -->
<!-- Title: Interest in optional Karpenter autoscaling (k3s on MicroOS)? Feasibility check -->

### Summary

Gauging interest in an **opt-in** Karpenter autoscaling option using
[`karpenter-provider-hetzner`](https://github.com/paperclipinc/karpenter-provider-hetzner),
as an alternative to the current Cluster Autoscaler — and being upfront that
MicroOS makes this the hardest of the Hetzner installers to support.

**Disclosure:** I help maintain `karpenter-provider-hetzner`. This is a
Discussion, not a PR — I want to validate feasibility and appetite with you
first, because the honest answer may be "not yet."

### The hard part (stated plainly)

kube-hetzner runs **k3s on immutable openSUSE MicroOS**. Karpenter is
bootstrap-agnostic (Hetzner runs the node `userData` as cloud-init), but the
provider currently ships worked examples only for Ubuntu/kubeadm and Talos. A
MicroOS path would need a cloud-init that reproduces your
`autoscaler-cloudinit.yaml.tpl` flow — the k3s-agent install on the
transactional-update OS, snapshot/reboot semantics, the shared k3s token and
endpoint. That's real, MicroOS-specific work, not a thin addon.

It would also overlap directly with your well-integrated Cluster Autoscaler
path (same token/`HCLOUD_CLUSTER_CONFIG` plumbing), so it'd be a *second*
autoscaler to maintain, not a gap-filler.

### Why it might still be worth it

Karpenter does per-pod instance-type selection across CX/CPX/CAX/CCX and
consolidation, rather than fixed pre-declared pools. For users who want
mixed-type bin-packing and aggressive scale-down, that's a meaningful upgrade
over the CA model.

### What I'd want from you before building anything

1. Is an opt-in second autoscaler something kube-hetzner would consider, given
   the maintenance surface?
2. If yes — would you prefer I first prove a **MicroOS + k3s-agent** Karpenter
   bootstrap works end-to-end (as a standalone example against a kube-hetzner
   cluster) before any module PR? That de-risks the OS-specific part.
3. Any prior context I'm missing — I searched issues/discussions and found no
   existing Karpenter thread, but you'd know if it's been considered.

For reference, here's the k3s (Ubuntu) bootstrap path I've already documented;
the MicroOS variant would build on it:
- https://github.com/paperclipinc/karpenter-provider-hetzner/blob/main/docs/k3s-bootstrap.md

Thanks — kube-hetzner is the gold standard for k3s-on-Hetzner, so I'd rather do
this right (or not at all) than bolt on something fragile.
