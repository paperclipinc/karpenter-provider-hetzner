# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.1.0] - 2026-08-01

### Fixed
- Pods bound to an hcloud CSI volume can now trigger provisioning. The hcloud CSI driver pins `PersistentVolume` `nodeAffinity` on `csi.hetzner.cloud/location`, which Karpenter did not recognize, so volume-topology scheduling rejected every PVC-backed pod with `label "csi.hetzner.cloud/location" does not have known values`. That domain is now aliased to the standard `topology.kubernetes.io/zone` via `NormalizedLabels`, mirroring how `karpenter-provider-aws` aliases `topology.ebs.csi.aws.com/zone`. No NodePool or StorageClass changes are needed (#46).

### Added
- Chart: `nodeSelector`, `affinity`, `tolerations`, `topologySpreadConstraints`, `imagePullSecrets`, `priorityClassName`, `command` and `args` on the controller Deployment. All empty by default, so existing releases render unchanged (#47).
- Chart: optional `podDisruptionBudget` (off by default; `maxUnavailable: 1` when enabled). `minAvailable` and `maxUnavailable` are mutually exclusive positive integers, and configurations that would block all voluntary drains (`minAvailable >= replicas`, `maxUnavailable: 0`) are rejected at render time. `replicas` is likewise validated as a non-negative integer (#47).

## [2.0.0] - 2026-07-28

### Changed
- **BREAKING:** `HCloudNodeClass` graduated from `karpenter.hetzner.cloud/v1alpha1` to `karpenter.hetzner.cloud/v1`. There is no conversion webhook — update `apiVersion` in your manifests and re-apply your node classes (#37).

### Added
- k3s agent bootstrap support: `examples/k3s-nodeclass.yaml` and `docs/k3s-bootstrap.md`.
- Artifact Hub repository ID and badge (#32).

### Fixed
- The chart ships the Karpenter core CRDs (`NodePool`, `NodeClaim`), so a clean install no longer leaves the controller crash-looping on its own watches. Both are vendored from the pinned `sigs.k8s.io/karpenter` during `make generate`, so a dependency bump that changes either schema fails the `generate-verify` CI gate instead of shipping a stale CRD (#44).
- The operator image is pinned to the chart's `appVersion` instead of floating on `:latest`, so an installed chart runs the operator it was published with (#45).
- Return `NodeClaimNotFoundError` when deleting an already-gone server, instead of a hard error (#40).

### Changed (dependencies)
- Bumped `sigs.k8s.io/karpenter`, `hetznercloud/hcloud-go`, and GitHub Actions (#34, #35, #36, #39, #41, #42).
- CI reads the Go version from `go.mod` instead of pinning it.

## [1.0.0] - 2026-06-16

First stable release: a complete CloudProvider implementation with full drift
detection, observability, supply-chain attestations, and adoption docs.

### Added
- Image label selector: `HCloudNodeClass.spec.imageSelector.selector` filters Hetzner images by arbitrary labels, so you can pin the exact image (version plus baked extensions, e.g. a gVisor-Talos snapshot) instead of fuzzy description matching (#23).
- Wrong-arch guard: provisioning is rejected when the resolved image architecture does not match the architecture the NodeClaim requires (#23).
- Placement group creation and assignment: `placementGroupStrategy: spread` now actually creates/assigns a cluster-scoped Hetzner placement group (previously declared but a no-op) (#24).
- Location drift detection: servers whose Hetzner location is no longer in the NodeClass `locations` are flagged as drifted (#24).
- Label drift detection: servers whose labels no longer cover the NodeClass `labels` are flagged as drifted (#26).
- Structured logging across provider operations (server create/delete, image resolution, drift) via the controller-runtime contextual logger (#26).
- `seccompProfile: RuntimeDefault` on the controller pod for PSS `restricted` compliance (#26).
- Prometheus metrics (`karpenter_hetzner_*`: server create/delete results and duration, hcloud API calls, drift detections, instance-type cache hits/misses) plus a Helm `ServiceMonitor` (#29).
- Warning Events from the nodeclass controller on every NotReady path, so `kubectl describe hcloudnodeclass` explains why a class is not Ready (#29).
- Examples (`talos-nodeclass`, `ubuntu-nodeclass`, `nodepool-multiarch`) and Talos/Ubuntu bootstrap guides (#28).

### Security
- Cosign keyless signing of the release image using GitHub OIDC (no long-lived keys).
- SLSA provenance attestation (`mode=max`) attached in-registry via BuildKit.
- In-registry SBOM attestation (CycloneDX) attached via BuildKit.
- Standalone SPDX SBOM uploaded as a workflow artifact via `anchore/sbom-action`.

## [0.3.0] - 2026-06-13

### Added
- `HCloudNodeClass.spec.userDataSecretRef`: reference a Kubernetes Secret for cloud-init user data instead of inlining it in the NodeClass spec (#20).

## [0.2.0] - 2026-06-13

### Changed
- Upgraded to Karpenter v1.13.0 (#18).
- Bumped Helm chart to 0.2.0 (#19).

## [0.1.0] - 2026-06-13

### Added
- Initial `CloudProvider` implementation covering all 8 Karpenter interface methods.
- Instance provider: Hetzner Cloud server CRUD (create, get, delete, list).
- Image family provider: Talos and Ubuntu image resolution.
- Instance type provider with pricing data and caching.
- `HCloudNodeClass` CRD with labels and cluster-scope fix.
- Helm chart for `karpenter-provider-hetzner`.
- Multi-arch Docker image (`linux/amd64`, `linux/arm64`) built via cross-compilation (no emulation).
- GitHub Actions: test, lint, release, and `govulncheck` security workflows.
- CI publishes Helm chart to OCI registry on release.

### Fixed
- Resolve images per-architecture; NodeClass is `Ready` if any arch resolves (#14).
- Grant full Karpenter-core RBAC in Helm chart (#13).
- Treat `unsupported location for server type` as an unavailable offering rather than a hard error (#16).

[Unreleased]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v2.1.0...HEAD
[2.1.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v2.0.0...v2.1.0
[2.0.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v1.0.0...v2.0.0
[1.0.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v0.3.0...v1.0.0
[0.3.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/releases/tag/v0.1.0
