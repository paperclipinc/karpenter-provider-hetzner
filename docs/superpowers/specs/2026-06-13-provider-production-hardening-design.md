# Provider Production-Hardening — Design

Status: approved (2026-06-13)
Scope: Spec #1 of 4 for making `karpenter-provider-hetzner` production-ready.

## Context

`karpenter-provider-hetzner` is a public OSS Karpenter cloud provider for Hetzner
Cloud (hcloud API). It has a clean, well-structured foundation — instance,
instancetype, and imagefamily providers, an `HCloudNodeClass` CRD type, the 8
CloudProvider interface methods, partial drift detection, Talos + Ubuntu image
resolution, and real-time pricing — but it is **not yet deployable or
production-correct**. This spec makes the provider correct and deployable. It is
the first of four independent specs:

1. **Provider production-hardening** (this spec) — provider correctness + deployability.
2. **Testing + cost guardrails on mono** — e2e against the `mono` Hetzner/Talos
   cluster (no production traffic yet), with hard cost ceilings; includes the
   node-join / Talos bootstrap contract.
3. **Robot/KVM dedicated nodes** — Karpenter-provisioned KVM-capable Hetzner
   dedicated servers for the `mitos` Firecracker microVM sandbox workload
   (`/dev/kvm`, label `agentrun.dev/kvm=true`). Architecturally distinct (Robot
   billing/provisioning fights Karpenter churn) — its own design.
4. **World-class repo polish** — README, CONTRIBUTING, CODEOWNERS, issue/PR
   templates, branch protection, security scanning, release automation, docs.

The provider is an OSS product; `mono` is a convenient real-Hetzner test
environment, not the only intended consumer. Design for generic OSS users.

## Approved approach

**Comprehensive hardening (option B):** make the provider correct and deployable
in one clean pass — fix blockers, complete the half-built features (drift,
availability), and add controller-level tests — so spec #2 tests a correct,
stable provider rather than a moving target. CEL webhooks and dashboards are
explicitly pushed to spec #4 to avoid scope creep.

## Decisions

- **Node-join / Talos bootstrap is OUT of this spec** — deferred to spec #2, where
  it can be proven end-to-end. This spec is pure provider correctness.
- **Cluster-name config is IN** — multi-cluster safety is a production
  correctness concern, not a nicety.

## Work items

### 1. Build & version consistency

**Problem:** `go.mod` declares `go 1.26.3`; the Dockerfile uses `golang:1.23-alpine`
and CI pins `go-version: "1.23"`. The release build and CI are inconsistent and
will fail to build the module.

**Change:** Standardize on the Go toolchain that `go.mod` requires (1.26).
- Bump Dockerfile builder image and CI `setup-go` to 1.26.
- Make CI call the Makefile targets (`make test lint`) so local and CI paths match.
- Verify `make build` and `docker build` succeed on the chosen toolchain.

### 2. CRD generation & packaging

**Problem:** No CRD is generated or committed. `charts/.../crds/` does not exist,
so `helm install` never registers `HCloudNodeClass` and the provider is unusable
on a real cluster.

**Change:**
- Run `controller-gen` (already wired in `make generate`) to emit the
  `HCloudNodeClass` CRD into `charts/karpenter-provider-hetzner/crds/`, and commit it.
- Add the missing kubebuilder validation markers to the types: enums, required
  fields, and defaults for the new public-IP flags.
- Add a CI job that runs `make generate` and fails if the working tree has a diff,
  so the committed CRD can never drift from the Go types.

### 3. NodeClass status controller (new `pkg/controllers/nodeclass`)

**Problem:** `main.go` wires only Karpenter's *core* controllers. Nothing sets the
`Ready` condition on `HCloudNodeClass` (Karpenter gates NodeClaim launch on it) or
populates `Status.ResolvedImages`. Provisioning may never start, and image-drift
detection is hollow because the resolved image is never recorded in status.

**Change:** Add a reconciler that, per `HCloudNodeClass`:
- Validates spec against live Hetzner: network exists, referenced firewalls and
  SSH keys exist, and the image family resolves for each supported architecture.
- Populates `Status.ResolvedImages` (one entry per location/arch).
- Sets typed status conditions: `ImagesReady`, `NetworkReady` (and firewall/SSH
  validity rolled into readiness), aggregated into `Ready`.
- Is registered in `main.go` alongside the core controllers, using the operator's
  manager/client.

This unblocks provisioning and makes image-drift real.

### 4. `instance.Create` robustness

**Problem:** `Create` returns the server immediately without waiting for the create
Action, and maps no Hetzner errors. Capacity/quota failures surface as generic
errors, so the Karpenter scheduler hard-fails instead of trying another type/zone.

**Change:**
- Wait on the create Action(s) to completion (success or terminal failure) before
  returning a server; surface a server only once the create succeeded.
- Map Hetzner error codes to Karpenter error types:
  - `resource_unavailable` (and equivalent sold-out/quota signals) →
    `cloudprovider.NewInsufficientCapacityError`.
  - `rate_limit_exceeded` → a retryable error (let Karpenter requeue).
  - other terminal errors → wrapped, non-retryable.
- Keep `Delete`/`Get` idempotency (NotFound → nil) as-is.

### 5. Offerings: availability + pricing

**Problem:** Offerings hardcode `Available: true`; pricing uses Gross (incl. VAT)
divided by 730 rather than the real hourly Net figure; the now-billed primary
IPv4 is ignored.

**Change:**
- Build offerings only for locations where the server type is actually priced
  (already implicit in `Pricings`; make it explicit and tested).
- Add an **unavailable-offerings cache** (the AWS-provider pattern): when a create
  hits `resource_unavailable` for a `(serverType, location)`, mark that offering
  unavailable for a short TTL so the next scheduling pass picks an alternative
  instead of retrying a sold-out combination. Cache is in-memory, TTL-bounded,
  and shared between the instance and instancetype providers (or surfaced via a
  small interface the cloudprovider owns).
- Switch pricing to **Net hourly** (`Pricing.Hourly.Net` when present, else
  `Monthly.Net / 730`).
- Add NodeClass spec fields `enablePublicIPv4` / `enablePublicIPv6` (default
  `true`, matching Hetzner's default). When public IPv4 is enabled, fold the
  primary-IPv4 monthly charge into the offering's hourly price so cost-optimal
  scheduling accounts for it; when disabled, omit it and create the server with
  public IPv4 off (private-network clusters save the IPv4 charge). Directly
  serves the "keep Hetzner costs reasonable" requirement.

### 6. Drift completeness

**Problem:** `IsDrifted` declares `FirewallDrift` but implements only image +
network. SSH-key, server-type, and userData drift are unchecked.

**Change:** Implement every declared `DriftReason` and add the missing obvious
ones, comparing live server state against resolved NodeClass intent:
- Image (already present — keep, now backed by real `Status.ResolvedImages`).
- Network (already present — keep).
- Firewall — server's attached firewalls vs `Spec.FirewallIDs`.
- SSH keys — best-effort (Hetzner does not always echo applied keys; document
  the limitation if it cannot be reliably read post-create).
- Server type — server's type vs the type recorded for the NodeClaim.
- UserData — hashed and compared if Hetzner exposes it; otherwise documented as
  not-readable and omitted rather than declared-and-unimplemented.

No declared `DriftReason` is left unimplemented; anything not reliably detectable
is removed from the declared set and documented, not faked.

### 7. Multi-cluster-safe tagging + config

**Problem:** Servers are tagged only `karpenter.sh/managed-by=karpenter` with no
cluster identifier. Two clusters sharing one Hetzner project would list and GC
each other's nodes.

**Change:**
- Introduce a required `CLUSTER_NAME` configuration (env var + `--cluster-name`
  flag + Helm value). Fail fast at startup if unset.
- Tag every created server with `karpenter.sh/cluster=<name>` in addition to the
  managed-by label.
- Filter `List` (and therefore GC) by the cluster label so the provider only ever
  sees its own cluster's servers.
- Expose leader-election and health/metrics ports in the Helm deployment, and add
  liveness/readiness probes pointing at the operator's health endpoints.

### 8. Testing

**Problem:** ~650 lines / 31 tests across the three providers; `cloudprovider_test.go`
is only 38 lines (the 8-method interface is barely covered); no controller tests.

**Change:**
- Table-driven unit tests for: Hetzner error-mapping, the unavailable-offerings
  cache (mark/expire/skip), the full drift matrix, Net pricing + IPv4 folding,
  and cluster-label tagging/filtering.
- **envtest** coverage for the NodeClass controller: conditions transition
  correctly and `Status.ResolvedImages` is populated for valid specs; invalid
  specs (missing network/firewall/image) set the right not-ready condition.
- Raise `cloudprovider_test.go` to exercise all 8 methods against a fake hcloud
  client (Create/Delete/Get/List/IsDrifted/GetInstanceTypes/Name/RepairPolicies).
- Coverage target: every new branch exercised. Real e2e on `mono` is spec #2.

## Out of scope (own specs)

- Node-join / Talos bootstrap contract and e2e validation + cost guardrails (#2).
- Robot/KVM dedicated nodes for `mitos` (#3).
- World-class repo polish: README, CONTRIBUTING, CODEOWNERS, issue/PR templates,
  branch protection, security scanning, release automation, docs site (#4).

## Success criteria

- `make build test lint generate` all pass on a single consistent Go toolchain,
  and `docker build` succeeds.
- A committed CRD installs via Helm and `HCloudNodeClass` reaches `Ready` when its
  spec is valid against live Hetzner.
- `Create` waits for readiness and maps capacity/quota/rate-limit errors to the
  correct Karpenter error types.
- Every declared `DriftReason` is implemented (or removed + documented).
- Servers are cluster-scoped via `CLUSTER_NAME`; a second cluster in the same
  project is invisible to this one.
- New unit + envtest coverage is green; no faked/half-built feature remains.
