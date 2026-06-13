# HCloudNodeClass userDataSecretRef Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `HCloudNodeClass` source `userData` from a Secret (`userDataSecretRef`) instead of inline, so secret Talos bootstrap data stays out of the CR/status/git.

**Architecture:** Add an optional `userDataSecretRef` field + `UserDataReady` status condition. The NodeClass controller validates the referenced Secret/key (gating `Ready`); `cloudprovider.Create` resolves the Secret value at server-create time. Both already hold a controller-runtime `kubeClient`, so they read Secrets directly (no new interface) and are testable with the fake client.

**Tech Stack:** Go, sigs.k8s.io/karpenter v1.13, awslabs/operatorpkg status conditions, controller-runtime (incl. fake client), controller-gen.

---

## Conventions

- Repo: `/Users/jannesstubbemann/repos/paperclip/karpenter-provider-hetzner`, branch `feat/userdata-secret-ref`.
- Run tests from repo root. `make generate` regenerates the CRD + deepcopy; `make generate-verify` must exit 0 on a clean tree.
- `cloudprovider.go` already imports `corev1 "k8s.io/api/core/v1"`, `"k8s.io/apimachinery/pkg/types"`, `"fmt"`. `controller.go` imports `"fmt"`, controller-runtime `client`; it will need `corev1`, `apierrors "k8s.io/apimachinery/pkg/api/errors"`, and `"k8s.io/apimachinery/pkg/types"` added.

---

## Task 1: API — `UserDataSecretReference` type, field, and `UserDataReady` condition

**Files:**
- Modify: `pkg/apis/v1alpha1/hcloudnodeclass_types.go`
- Test: `pkg/apis/v1alpha1/hcloudnodeclass_test.go`
- Regenerate: CRD + deepcopy

- [ ] **Step 1: Write the failing test**

Append to `pkg/apis/v1alpha1/hcloudnodeclass_test.go`:

```go
func TestUserDataReadyConditionRegistered(t *testing.T) {
	nc := &HCloudNodeClass{}
	if nc.StatusConditions().Get(ConditionTypeUserDataReady) == nil {
		t.Errorf("expected condition %q to be registered", ConditionTypeUserDataReady)
	}
}
```

- [ ] **Step 2: Run it — expect FAIL (compile error: ConditionTypeUserDataReady undefined)**

Run: `go test ./pkg/apis/v1alpha1/ -run TestUserDataReadyConditionRegistered -v`
Expected: FAIL (undefined).

- [ ] **Step 3: Add the const, register it, add the type + field**

In `pkg/apis/v1alpha1/hcloudnodeclass_types.go`, add to the condition consts block:

```go
	ConditionTypeUserDataReady  = "UserDataReady"
```

Change the `conditionTypes` var to register it:

```go
var conditionTypes = status.NewReadyConditions(ConditionTypeImagesReady, ConditionTypeNetworkReady, ConditionTypeResourcesReady, ConditionTypeUserDataReady)
```

Add the field to `HCloudNodeClassSpec` (after the existing `UserData` field):

```go
	// UserDataSecretRef sources userData from a Secret instead of inline. When
	// set, it takes precedence over UserData. The Secret is read at server-create
	// time; its value never appears in the NodeClass spec, status, or git.
	// +optional
	UserDataSecretRef *UserDataSecretReference `json:"userDataSecretRef,omitempty"`
```

Add the type (near the other helper types, e.g. after `ImageSelector`):

```go
// UserDataSecretReference points at a Secret key holding the server userData.
type UserDataSecretReference struct {
	// Namespace of the Secret (required: HCloudNodeClass is cluster-scoped).
	Namespace string `json:"namespace"`
	// Name of the Secret.
	Name string `json:"name"`
	// Key within the Secret's data holding the userData.
	Key string `json:"key"`
}
```

- [ ] **Step 4: Run the test — expect PASS**

Run: `go test ./pkg/apis/v1alpha1/ -v`
Expected: PASS.

- [ ] **Step 5: Regenerate + verify**

Run: `make generate` then `make generate-verify`
Expected: after regeneration the CRD gains `userDataSecretRef` (object with required `namespace`/`name`/`key`) and `zz_generated.deepcopy.go` gains deepcopy for the new pointer/type; `make generate-verify` exits 0 on the committed tree (run `make generate` then commit). Confirm: `grep -A4 userDataSecretRef charts/karpenter-provider-hetzner/crds/*.yaml`.

- [ ] **Step 6: Build + commit**

```bash
go build ./...
git add pkg/apis/v1alpha1 charts/karpenter-provider-hetzner/crds
git commit -m "feat(api): add HCloudNodeClass.userDataSecretRef + UserDataReady condition"
```

---

## Task 2: `cloudprovider.Create` resolves userData from the Secret

**Files:**
- Modify: `pkg/cloudprovider/cloudprovider.go`
- Test: `pkg/cloudprovider/cloudprovider_test.go`

- [ ] **Step 1: Write the failing test**

Add to `pkg/cloudprovider/cloudprovider_test.go` (reuses the `buildCPWithTypes` / fake-client harness; needs `corev1 "k8s.io/api/core/v1"` and `metav1` which are already imported). Build a CloudProvider whose kube client also holds a Secret. Add a helper + tests:

```go
func TestCreate_UserDataFromSecret(t *testing.T) {
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := baselineNodeClass()
	nc.Spec.UserData = "inline-should-be-ignored"
	nc.Spec.UserDataSecretRef = &v1alpha1.UserDataSecretReference{Namespace: "kube-system", Name: "talos", Key: "userData"}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "talos", Namespace: "kube-system"},
		Data:       map[string][]byte{"userData": []byte("machine:\n  type: worker\n")},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc, secret).Build()
	fsc := &fakeServerClient{servers: map[int64]*hcloud.Server{}}
	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(fsc, "test-cluster"),
		instancetype.NewProvider(&fakeServerTypeClient{types: []*hcloud.ServerType{cx22Type()}}),
		imagefamily.NewProvider(&fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04"}}}))
	if _, err := cp.Create(context.Background(), createNodeClaim()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := fsc.lastUserData(); got != "machine:\n  type: worker\n" {
		t.Errorf("expected userData from secret, got %q", got)
	}
}

func TestCreate_UserDataSecretMissing(t *testing.T) {
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := baselineNodeClass()
	nc.Spec.UserDataSecretRef = &v1alpha1.UserDataSecretReference{Namespace: "kube-system", Name: "absent", Key: "userData"}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc).Build()
	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(&fakeServerClient{servers: map[int64]*hcloud.Server{}}, "test-cluster"),
		instancetype.NewProvider(&fakeServerTypeClient{types: []*hcloud.ServerType{cx22Type()}}),
		imagefamily.NewProvider(&fakeImageClient{images: []*hcloud.Image{{ID: 42, Description: "Ubuntu 24.04"}}}))
	if _, err := cp.Create(context.Background(), createNodeClaim()); err == nil {
		t.Fatal("expected error when userData secret is missing")
	}
}
```

Add a `lastUserData()` accessor to the test's `fakeServerClient` (it already records `createErr`/`servers`; add capture of the last create opts' UserData). In `fakeServerClient`:

```go
// add field:  lastOpts hcloud.ServerCreateOpts
// in Create(): set m.lastOpts = opts  (before returning)
// add method:
func (f *fakeServerClient) lastUserData() string { return f.lastOpts.UserData }
```

(If `fakeServerClient.Create` does not already record opts, add `f.lastOpts = opts` as its first line.)

- [ ] **Step 2: Run — expect FAIL (UserDataSecretReference undefined OR userData not resolved)**

Run: `go test ./pkg/cloudprovider/ -run TestCreate_UserData -v`
Expected: FAIL.

- [ ] **Step 3: Implement resolution in cloudprovider.go**

Add a helper method (near `resolveNodeClass`):

```go
// resolveUserData returns the userData for a NodeClass, reading it from the
// referenced Secret when userDataSecretRef is set (precedence over inline).
func (cp *CloudProvider) resolveUserData(ctx context.Context, nc *v1alpha1.HCloudNodeClass) (string, error) {
	ref := nc.Spec.UserDataSecretRef
	if ref == nil {
		return nc.Spec.UserData, nil
	}
	secret := &corev1.Secret{}
	if err := cp.kubeClient.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, secret); err != nil {
		return "", fmt.Errorf("reading userData secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	data, ok := secret.Data[ref.Key]
	if !ok || len(data) == 0 {
		return "", fmt.Errorf("userData secret %s/%s has no non-empty key %q", ref.Namespace, ref.Name, ref.Key)
	}
	return string(data), nil
}
```

In `Create`, before the `cp.instanceProvider.Create(ctx, instance.CreateOpts{...})` call, resolve userData and use it:

```go
	userData, err := cp.resolveUserData(ctx, nodeClass)
	if err != nil {
		return nil, fmt.Errorf("resolving userData: %w", err)
	}
```

Then change the `CreateOpts` field from `UserData: nodeClass.Spec.UserData,` to `UserData: userData,`.

- [ ] **Step 4: Run — expect PASS**

Run: `go test ./pkg/cloudprovider/ -run TestCreate_UserData -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 5: Commit**

```bash
git add pkg/cloudprovider/cloudprovider.go pkg/cloudprovider/cloudprovider_test.go
git commit -m "feat: resolve HCloudNodeClass userData from userDataSecretRef at create time"
```

---

## Task 3: NodeClass controller validates `userDataSecretRef`

**Files:**
- Modify: `pkg/controllers/nodeclass/controller.go`
- Test: `pkg/controllers/nodeclass/controller_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `pkg/controllers/nodeclass/controller_test.go`:

```go
func TestReconcile_UserDataSecretValid(t *testing.T) {
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Spec.UserDataSecretRef = &v1alpha1.UserDataSecretReference{Namespace: "kube-system", Name: "talos", Key: "userData"}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "talos", Namespace: "kube-system"}, Data: map[string][]byte{"userData": []byte("machine: {}\n")}}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc, secret).WithStatusSubresource(nc).Build()
	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)
	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	got := &v1alpha1.HCloudNodeClass{}
	_ = kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got)
	if !got.StatusConditions().Get(v1alpha1.ConditionTypeUserDataReady).IsTrue() {
		t.Error("UserDataReady should be true for a valid secret ref")
	}
	if !got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should be true")
	}
}

func TestReconcile_UserDataSecretMissing(t *testing.T) {
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := newNodeClass()
	nc.Spec.UserDataSecretRef = &v1alpha1.UserDataSecretReference{Namespace: "kube-system", Name: "absent", Key: "userData"}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc).WithStatusSubresource(nc).Build()
	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, fakeFirewalls{}, fakeSSHKeys{}, img)
	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatal(err)
	}
	got := &v1alpha1.HCloudNodeClass{}
	_ = kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got)
	if got.StatusConditions().Get(v1alpha1.ConditionTypeUserDataReady).IsTrue() {
		t.Error("UserDataReady should be false for a missing secret")
	}
	if got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should not be true when the userData secret is missing")
	}
}
```

(`corev1 "k8s.io/api/core/v1"` and `metav1` are already imported in this test file via the shared harness; if not, add them.)

- [ ] **Step 2: Run — expect FAIL (validateUserData not wired / UserDataReady never set)**

Run: `go test ./pkg/controllers/nodeclass/ -run TestReconcile_UserData -v`
Expected: FAIL.

- [ ] **Step 3: Implement in controller.go**

Add imports if missing: `corev1 "k8s.io/api/core/v1"`, `apierrors "k8s.io/apimachinery/pkg/api/errors"`, `"k8s.io/apimachinery/pkg/types"`.

In `Reconcile`, after the resources-validation block and before the image-resolution block, add:

```go
	// Validate the userData secret ref (if set).
	if reason, msg, unknown, ok := c.validateUserData(ctx, nc); ok {
		nc.StatusConditions().SetTrue(v1alpha1.ConditionTypeUserDataReady)
	} else if unknown {
		nc.StatusConditions().SetUnknownWithReason(v1alpha1.ConditionTypeUserDataReady, reason, msg)
	} else {
		nc.StatusConditions().SetFalse(v1alpha1.ConditionTypeUserDataReady, reason, msg)
	}
```

Add the helper (near `validateResources`):

```go
// validateUserData checks the referenced userData Secret + key exist and are
// non-empty. Returns ok=true when there is nothing to validate or it resolves;
// unknown=true on a transient API error.
func (c *Controller) validateUserData(ctx context.Context, nc *v1alpha1.HCloudNodeClass) (reason, msg string, unknown, ok bool) {
	ref := nc.Spec.UserDataSecretRef
	if ref == nil {
		return "", "", false, true
	}
	secret := &corev1.Secret{}
	if err := c.kubeClient.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			return "UserDataSecretNotFound", fmt.Sprintf("secret %s/%s not found", ref.Namespace, ref.Name), false, false
		}
		return "UserDataCheckFailed", err.Error(), true, false
	}
	if v, present := secret.Data[ref.Key]; !present || len(v) == 0 {
		return "UserDataKeyMissing", fmt.Sprintf("secret %s/%s has no non-empty key %q", ref.Namespace, ref.Name, ref.Key), false, false
	}
	return "", "", false, true
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `go test -race -count=1 ./pkg/controllers/nodeclass/ -v && go build ./...`
Expected: PASS (incl. the existing controller tests), clean build.

- [ ] **Step 5: Full suite + generate-verify, then commit**

```bash
go test -race -count=1 ./...
make generate-verify
git add pkg/controllers/nodeclass
git commit -m "feat(nodeclass): validate userDataSecretRef and gate Ready on UserDataReady"
```
Expected: all packages pass; generate-verify exits 0.

---

## Task 4: README + release v0.3.0

**Files:**
- Modify: `README.md`, `charts/karpenter-provider-hetzner/Chart.yaml`

- [ ] **Step 1: Document the field in the README**

In `README.md`, add `userDataSecretRef` to the HCloudNodeClass reference table (after the `userData` row):

```markdown
| `userDataSecretRef` | `{namespace,name,key}` | no | — | Source `userData` from a Secret (keeps secret bootstrap data out of git); takes precedence over `userData` |
```

- [ ] **Step 2: Open the PR, merge after green**

```bash
git add README.md
git commit -m "docs: document userDataSecretRef"
git push -u origin feat/userdata-secret-ref
gh pr create --base main --title "feat: HCloudNodeClass userDataSecretRef" --body "Source userData from a Secret so secret Talos bootstrap config stays out of git. Adds the field, a UserDataReady gating condition, create-time resolution, and tests."
```
Wait for CI green, then `gh pr merge --squash --admin --delete-branch`. The `image` job rebuilds `:main`.

- [ ] **Step 3: Cut v0.3.0**

```bash
git checkout main && git pull
# bump chart
sed -i '' -e 's/^version: 0.2.0/version: 0.3.0/' -e 's/^appVersion: "0.2.0"/appVersion: "0.3.0"/' charts/karpenter-provider-hetzner/Chart.yaml
git checkout -b release/v0.3.0 && git add charts && git commit -m "chore: chart 0.3.0 (userDataSecretRef)"
git push -u origin release/v0.3.0
gh pr create --base main --title "chore: chart 0.3.0" --body "userDataSecretRef release"
# merge after green, then:
gh release create v0.3.0 --target main --title "v0.3.0 — userDataSecretRef" --notes "Adds HCloudNodeClass.userDataSecretRef: source userData from a Secret so secret bootstrap config (e.g. Talos worker config) stays out of git. Image + chart 0.3.0."
```
Expected: Release workflow success; image `0.3.0` + chart `0.3.0` published.

---

## Self-review

- **Spec coverage:** API field+type (Task 1) ✓; UserDataReady condition + controller validation gating Ready (Tasks 1, 3) ✓; create-time resolution with precedence (Task 2) ✓; unit + controller tests (Tasks 2, 3) ✓; release v0.3.0 (Task 4) ✓. Simplification vs spec: uses the existing `kubeClient` directly instead of a new `SecretGetter` interface (k8s API is fake-client-testable) — same behavior, less surface.
- **Type consistency:** `UserDataSecretReference{Namespace,Name,Key}`, field `UserDataSecretRef`, const `ConditionTypeUserDataReady`, helpers `resolveUserData`/`validateUserData` are used consistently across tasks.
- **No placeholders:** every code/command step is concrete.
- Out of scope (own specs): mono coexist (#2), cutover (#3).
