# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Image label selector: `HCloudNodeClass.spec.imageSelector.selector` allows filtering Hetzner images by arbitrary labels, replacing the previous name-only approach (#23).
- Wrong-arch guard: provisioning is now rejected when the resolved image architecture does not match the requested node architecture (#23).
- Placement group creation and assignment: the provider now creates and assigns Hetzner placement groups to provisioned servers (#24).
- Location drift detection: servers whose Hetzner location no longer matches the `NodeClass` spec are detected and disrupted (#24).
- Label drift detection: servers whose Hetzner server-type labels diverge from the desired state are flagged and reconciled (#26).
- Structured logging: all log output now uses structured `slog` key-value fields for easier filtering and ingestion (#26).
- `seccompProfile: RuntimeDefault` on the controller `Deployment` for reduced syscall attack surface (#26).

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

[Unreleased]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/paperclipinc/karpenter-provider-hetzner/releases/tag/v0.1.0
