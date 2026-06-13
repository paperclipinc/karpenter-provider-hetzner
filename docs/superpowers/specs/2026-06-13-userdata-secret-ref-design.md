# HCloudNodeClass userDataSecretRef — Design

Status: approved (2026-06-13)
Scope: Sub-project #1 of the mono cluster-autoscaler→Karpenter migration. Lets a
NodeClass source its `userData` from a Secret instead of inline, so secret
bootstrap data (Talos worker config: machine/bootstrap tokens + CA) never lands
in the CR spec, status, or a GitOps repo.

## Why

Karpenter on a Talos cluster needs a valid Talos **worker machine config** as the
server `userData` for nodes to join. That config contains the cluster's machine
token, bootstrap token, and CA — genuinely secret. `HCloudNodeClass.userData` is
an inline string, so a Flux/GitOps-managed NodeClass would commit those secrets
to git. mono is EU-sovereign and 1Password-driven; secrets in git are a
non-starter. The cluster-autoscaler keeps this config in a Secret today; once it
is removed (migration sub-project #3) that source is gone. This feature gives the
provider a first-class way to read `userData` from a Secret.

This is also a generally useful upstream feature: sensitive cloud-init/ignition
bootstrap data should not have to live inline in a CR.

## API

Add an optional field to `HCloudNodeClassSpec` (inline `userData` is retained):

```go
// UserDataSecretRef sources userData from a Secret instead of inline. When set,
// it takes precedence over UserData. The Secret is read at server-create time;
// its value never appears in the NodeClass spec, status, or a GitOps repo.
// +optional
UserDataSecretRef *UserDataSecretReference `json:"userDataSecretRef,omitempty"`
```

```go
type UserDataSecretReference struct {
    // Namespace of the Secret. Required because HCloudNodeClass is cluster-scoped.
    Namespace string `json:"namespace"`
    // Name of the Secret.
    Name string `json:"name"`
    // Key within the Secret's data holding the userData.
    Key string `json:"key"`
}
```

Precedence: if `UserDataSecretRef != nil`, its resolved value is used; inline
`UserData` is ignored. If neither is set, userData is empty (current behavior).

## NodeClass status controller

Add condition `ConditionTypeUserDataReady = "UserDataReady"` and register it as a
dependent of `Ready` (alongside ImagesReady / NetworkReady / ResourcesReady).

In `Reconcile`, when `UserDataSecretRef` is set, validate it via the kube client:
- Secret not found → `SetFalse(UserDataReady, "UserDataSecretNotFound", msg)`.
- Key missing or empty in the Secret → `SetFalse(UserDataReady, "UserDataKeyMissing", msg)`.
- transient Get error → `SetUnknownWithReason(UserDataReady, "UserDataCheckFailed", err)`.
- otherwise → `SetTrue(UserDataReady)`.
When `UserDataSecretRef` is nil, `SetTrue(UserDataReady)` (nothing to validate).

This gates provisioning exactly like the existing network/image/resource checks:
a bad/missing ref keeps the NodeClass `NotReady` so Karpenter does not launch.

The controller needs a Secret reader. It already has `secrets get/list/watch`
RBAC (from the chart's Karpenter-core RBAC). Add a narrow `SecretGetter`
interface (mirrors `NetworkGetter` etc.) backed by the controller-runtime client,
so it is unit-testable with a fake.

## cloudprovider.Create

Before building `instance.CreateOpts`, resolve userData:
- If `nodeClass.Spec.UserDataSecretRef != nil`: `kubeClient.Get` the Secret in the
  given namespace; read `Data[key]`; use it as the userData. A missing secret/key
  at this point returns a wrapped error (the NodeClass should already be NotReady,
  so this is a belt-and-suspenders path).
- Else: use `nodeClass.Spec.UserData`.

The resolved value flows only into `instance.CreateOpts.UserData` → the Hetzner
server's userData at create time. Same exposure as the cluster-autoscaler's
Secret today; never persisted to the CR/status/git.

## Testing

- **Unit (cloudprovider):** with a fake kube client holding a Secret, `Create`
  resolves userData from the ref; `userDataSecretRef` takes precedence over inline
  `userData`; a missing secret/key yields a clear error. Reuse the existing
  `buildCP`/fake-client harness.
- **Controller (fake client):** `userDataSecretRef` pointing at an existing
  Secret+key → `UserDataReady` true and `Ready` true; missing Secret → `Ready`
  false; missing key → `Ready` false; nil ref → `UserDataReady` true.
- `make generate-verify` clean (CRD/deepcopy regenerated for the new field/type).

## Release

Cut **v0.3.0** (multi-arch image + OCI Helm chart, via the existing release
workflow). This is the artifact mono sub-project #2 deploys.

## Out of scope (own specs)

- **#2 mono coexist:** Flux-deploy Karpenter into mono; a 1Password→Secret holding
  the Talos worker config; a sandbox `NodePool` (distinct taint, gVisor runsc
  tune) referencing that Secret via `userDataSecretRef`; a low-priority balloon
  pod for one warm node; validate it runs a real gVisor sandbox workload alongside
  the cluster-autoscaler.
- **#3 cutover:** retarget the `workload=sandbox` scheduling to the Karpenter pool,
  drain, disable + remove the cluster-autoscaler sandbox pool in terraform.

## Success criteria

- `HCloudNodeClass` with `userDataSecretRef` reaches `Ready` only when the Secret +
  key resolve; a bad ref keeps it `NotReady` and blocks provisioning.
- A server created from such a NodeClass boots with the Secret's userData; the
  secret value is absent from the CR, its status, and any committed manifest.
- v0.3.0 image + chart published; unit/controller tests green.
