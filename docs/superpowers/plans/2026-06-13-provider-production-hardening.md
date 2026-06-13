# Provider Production-Hardening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `karpenter-provider-hetzner` correct and deployable: fix the build, ship the CRD, add a NodeClass status controller, make instance creation robust, complete drift detection, add multi-cluster-safe tagging and accurate pricing, and cover it all with unit + envtest tests.

**Architecture:** A Karpenter `CloudProvider` for Hetzner Cloud. Three resource providers (instance, instancetype, imagefamily) sit behind narrow hcloud client interfaces (already mock-friendly). We add a `pkg/controllers/nodeclass` reconciler that sets `HCloudNodeClass` status conditions, a Hetzner-error → Karpenter-error mapping, an in-memory unavailable-offerings cache, and a `Config` carrying the required cluster name. The operator wires the new reconciler alongside Karpenter's core controllers.

**Tech Stack:** Go 1.26, `hetznercloud/hcloud-go/v2`, `sigs.k8s.io/karpenter` v1.12, `awslabs/operatorpkg` (status conditions + controller), controller-runtime + envtest, controller-gen, Helm.

---

## Conventions for every task

- Run tests from the repo root: `/Users/jannesstubbemann/repos/paperclip/karpenter-provider-hetzner`.
- The narrow hcloud client interfaces are mocked with in-package structs (see `pkg/providers/instance/instance_test.go` for the established pattern: a struct holding a `map[int64]*hcloud.Server` that implements the interface).
- Branch is `feat/provider-production-hardening`. Commit after each task.
- The full provider test command is `go test -race -count=1 ./...`.

---

## Task 1: Toolchain consistency (fix the broken build)

`go.mod` requires `go 1.26.3`, but the Dockerfile and CI use 1.23 — the release image cannot build the module. Standardize on 1.26.

**Files:**
- Modify: `Dockerfile:1`
- Modify: `.github/workflows/ci.yaml:18,27`

- [ ] **Step 1: Bump the Dockerfile builder image**

In `Dockerfile`, change line 1:

```dockerfile
FROM golang:1.26-alpine AS builder
```

- [ ] **Step 2: Bump CI Go version and route through the Makefile**

Replace `.github/workflows/ci.yaml` contents with:

```yaml
name: CI

on:
  pull_request:
  push:
    branches: [main]

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: make test

  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - uses: golangci/golangci-lint-action@v6
        with:
          version: latest
```

(The `generate`-diff and `envtest` jobs are added in Tasks 2 and 10.)

- [ ] **Step 3: Bump the release workflow Go version**

In `.github/workflows/release.yaml:19`, change `go-version: "1.23"` to `go-version: "1.26"`.

- [ ] **Step 4: Verify the build**

Run: `go build ./... && docker build -t khz:plan-check .`
Expected: both succeed (Docker build completes the distroless final stage).

- [ ] **Step 5: Commit**

```bash
git add Dockerfile .github/workflows/ci.yaml .github/workflows/release.yaml
git commit -m "fix: align Go toolchain to 1.26 across build, CI, and release"
```

---

## Task 2: Generate, commit, and guard the CRD

No CRD is shipped, so `helm install` never registers `HCloudNodeClass`. Generate it into the chart and add a CI guard so it can't drift from the Go types.

**Files:**
- Create: `charts/karpenter-provider-hetzner/crds/karpenter.hetzner.cloud_hcloudnodeclasses.yaml` (generated)
- Modify: `Makefile`
- Modify: `.github/workflows/ci.yaml`

- [ ] **Step 1: Add a controller-gen install + verify target to the Makefile**

Append to `Makefile`:

```makefile
CONTROLLER_GEN := go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0

.PHONY: generate-verify
generate-verify: generate
	@if [ -n "$$(git status --porcelain pkg/apis charts/karpenter-provider-hetzner/crds)" ]; then \
		echo "generated files are out of date; run 'make generate' and commit"; \
		git --no-pager diff -- pkg/apis charts/karpenter-provider-hetzner/crds; \
		exit 1; \
	fi
```

And change the `generate` target to use `$(CONTROLLER_GEN)`:

```makefile
generate:
	$(CONTROLLER_GEN) object paths="./pkg/apis/..."
	$(CONTROLLER_GEN) crd paths="./pkg/apis/..." output:crd:dir=charts/karpenter-provider-hetzner/crds
```

- [ ] **Step 2: Generate the CRD and deepcopy**

Run: `make generate`
Expected: `charts/karpenter-provider-hetzner/crds/karpenter.hetzner.cloud_hcloudnodeclasses.yaml` is created and `pkg/apis/v1alpha1/zz_generated.deepcopy.go` is regenerated (likely unchanged).

- [ ] **Step 3: Sanity-check the CRD**

Run: `grep -c "kind: CustomResourceDefinition" charts/karpenter-provider-hetzner/crds/karpenter.hetzner.cloud_hcloudnodeclasses.yaml`
Expected: `1`. Also confirm `grep "locations" charts/karpenter-provider-hetzner/crds/*.yaml` shows the field.

- [ ] **Step 4: Add the generate-diff guard job to CI**

Add this job to `.github/workflows/ci.yaml` under `jobs:`:

```yaml
  generate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: make generate-verify
```

- [ ] **Step 5: Verify the guard passes locally**

Run: `make generate-verify`
Expected: exits 0 (no diff).

- [ ] **Step 6: Commit**

```bash
git add Makefile charts/karpenter-provider-hetzner/crds .github/workflows/ci.yaml pkg/apis/v1alpha1/zz_generated.deepcopy.go
git commit -m "feat: generate and ship HCloudNodeClass CRD with CI drift guard"
```

---

## Task 3: NodeClass public-IP fields + status condition constants

Add cost-control public-IP flags (default true, matching Hetzner) and the named status condition types the controller (Task 10) and drift (Task 9) will set.

**Files:**
- Modify: `pkg/apis/v1alpha1/hcloudnodeclass_types.go`
- Test: `pkg/apis/v1alpha1/hcloudnodeclass_test.go` (create)
- Regenerate: CRD + deepcopy

- [ ] **Step 1: Write a failing test for the condition type registration**

Create `pkg/apis/v1alpha1/hcloudnodeclass_test.go`:

```go
package v1alpha1

import "testing"

func TestStatusConditionsIncludeDependents(t *testing.T) {
	nc := &HCloudNodeClass{}
	cs := nc.StatusConditions()
	for _, ct := range []string{ConditionTypeImagesReady, ConditionTypeNetworkReady} {
		if cs.Get(ct) == nil {
			t.Errorf("expected condition %q to be registered", ct)
		}
	}
}

func TestPublicIPDefaultsHelper(t *testing.T) {
	nc := &HCloudNodeClass{}
	if !nc.Spec.PublicIPv4Enabled() {
		t.Error("nil EnablePublicIPv4 should default to true")
	}
	off := false
	nc.Spec.EnablePublicIPv4 = &off
	if nc.Spec.PublicIPv4Enabled() {
		t.Error("explicit false should disable public IPv4")
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

Run: `go test ./pkg/apis/v1alpha1/ -run TestStatusConditions -v`
Expected: FAIL — `ConditionTypeImagesReady` undefined (compile error).

- [ ] **Step 3: Add condition constants, fields, and helpers**

In `pkg/apis/v1alpha1/hcloudnodeclass_types.go`, add the condition constants near the top (after imports):

```go
// Status condition types for HCloudNodeClass.
const (
	ConditionTypeImagesReady  = "ImagesReady"
	ConditionTypeNetworkReady = "NetworkReady"
)
```

Add the public-IP fields to `HCloudNodeClassSpec` (before the closing brace):

```go
	// EnablePublicIPv4 controls whether created servers get a public IPv4.
	// Defaults to true (Hetzner's default). Set false on private-network
	// clusters to avoid the primary-IPv4 charge.
	// +optional
	EnablePublicIPv4 *bool `json:"enablePublicIPv4,omitempty"`

	// EnablePublicIPv6 controls whether created servers get a public IPv6.
	// Defaults to true.
	// +optional
	EnablePublicIPv6 *bool `json:"enablePublicIPv6,omitempty"`
```

Add helpers and register the conditions. Replace the `conditionTypes` var:

```go
var conditionTypes = status.NewReadyConditions(ConditionTypeImagesReady, ConditionTypeNetworkReady)

// PublicIPv4Enabled reports whether public IPv4 should be enabled (default true).
func (s HCloudNodeClassSpec) PublicIPv4Enabled() bool {
	return s.EnablePublicIPv4 == nil || *s.EnablePublicIPv4
}

// PublicIPv6Enabled reports whether public IPv6 should be enabled (default true).
func (s HCloudNodeClassSpec) PublicIPv6Enabled() bool {
	return s.EnablePublicIPv6 == nil || *s.EnablePublicIPv6
}
```

`status.NewReadyConditions(deps...)` makes `Ready` depend on the listed conditions (verified in `operatorpkg/status`).

- [ ] **Step 4: Run the test to confirm it passes**

Run: `go test ./pkg/apis/v1alpha1/ -v`
Expected: PASS.

- [ ] **Step 5: Regenerate CRD + deepcopy and verify**

Run: `make generate-verify`
Expected: after the regeneration the tree may differ (new fields) — if `generate-verify` reports a diff, that's expected on first run; run `make generate` then re-run `make generate-verify` so it exits 0. Confirm `enablePublicIPv4` appears in the CRD: `grep enablePublicIPv4 charts/karpenter-provider-hetzner/crds/*.yaml`.

- [ ] **Step 6: Commit**

```bash
git add pkg/apis/v1alpha1 charts/karpenter-provider-hetzner/crds
git commit -m "feat: add public-IP flags and named NodeClass status conditions"
```

---

## Task 4: Cluster-name config + multi-cluster-safe tagging

Introduce a required `CLUSTER_NAME`, tag servers with it, and filter `List` by it so two clusters in one Hetzner project never see each other.

**Files:**
- Modify: `pkg/apis/v1alpha1/labels.go`
- Create: `pkg/operator/config.go`
- Test: `pkg/operator/config_test.go` (create)
- Modify: `pkg/providers/instance/instance.go` (CreateOpts + Create + List)
- Test: `pkg/providers/instance/instance_test.go` (extend)
- Modify: `pkg/cloudprovider/cloudprovider.go` (pass cluster name)
- Modify: `cmd/controller/main.go`

- [ ] **Step 1: Add the cluster label constant**

In `pkg/apis/v1alpha1/labels.go`, add to the const block:

```go
	ServerLabelCluster = "karpenter.sh/cluster"
```

- [ ] **Step 2: Write a failing test for config loading**

Create `pkg/operator/config_test.go`:

```go
package operator

import "testing"

func TestLoadConfig_RequiresClusterName(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "")
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected error when CLUSTER_NAME is unset")
	}
}

func TestLoadConfig_ReadsClusterName(t *testing.T) {
	t.Setenv("CLUSTER_NAME", "paperclip-prod")
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ClusterName != "paperclip-prod" {
		t.Errorf("got cluster name %q, want paperclip-prod", cfg.ClusterName)
	}
}
```

- [ ] **Step 3: Run the test to confirm it fails**

Run: `go test ./pkg/operator/ -v`
Expected: FAIL — `LoadConfig` undefined.

- [ ] **Step 4: Implement config loading**

Create `pkg/operator/config.go`:

```go
package operator

import (
	"fmt"
	"os"
	"strings"
)

// Config holds provider configuration sourced from the environment.
type Config struct {
	// ClusterName scopes all managed servers so multiple clusters can share
	// one Hetzner project without colliding.
	ClusterName string
}

// LoadConfig reads provider configuration from the environment.
// CLUSTER_NAME is required.
func LoadConfig() (*Config, error) {
	name := strings.TrimSpace(os.Getenv("CLUSTER_NAME"))
	if name == "" {
		return nil, fmt.Errorf("CLUSTER_NAME environment variable is required")
	}
	return &Config{ClusterName: name}, nil
}
```

- [ ] **Step 5: Run the test to confirm it passes**

Run: `go test ./pkg/operator/ -v`
Expected: PASS.

- [ ] **Step 6: Thread the cluster name through the instance provider**

In `pkg/providers/instance/instance.go`:

Add a field and constructor parameter:

```go
type Provider struct {
	client      ServerClient
	clusterName string
}

func NewProvider(client ServerClient, clusterName string) *Provider {
	return &Provider{client: client, clusterName: clusterName}
}
```

In `Create`, after setting `labels[v1alpha1.ServerLabelManagedBy] = ...`, add:

```go
	labels[v1alpha1.ServerLabelCluster] = p.clusterName
```

In `List`, change the label selector to include the cluster:

```go
	opts := hcloud.ServerListOpts{
		ListOpts: hcloud.ListOpts{
			LabelSelector: fmt.Sprintf("%s=%s,%s=%s",
				v1alpha1.ServerLabelManagedBy, v1alpha1.ServerValueManagedBy,
				v1alpha1.ServerLabelCluster, p.clusterName),
		},
	}
```

- [ ] **Step 7: Update existing instance tests for the new constructor + add a tagging test**

In `pkg/providers/instance/instance_test.go`, every `NewProvider(client)` becomes `NewProvider(client, "test-cluster")`. Add:

```go
func TestCreate_ClusterLabelApplied(t *testing.T) {
	client := newMockServerClient()
	p := NewProvider(client, "test-cluster")
	server, err := p.Create(context.Background(), CreateOpts{
		Name: "n", ServerType: "cx22", Location: "nbg1", Image: &hcloud.Image{ID: 1},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if server.Labels[v1alpha1.ServerLabelCluster] != "test-cluster" {
		t.Errorf("expected cluster label, got %q", server.Labels[v1alpha1.ServerLabelCluster])
	}
}
```

- [ ] **Step 8: Update the cloudprovider and main wiring**

In `cmd/controller/main.go`, after `hcloudClient` is created, load config and pass it:

```go
	cfg, err := hetznerop.LoadConfig()
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to load config")
		return
	}
	instanceProvider := instance.NewProvider(&hcloudClient.Server, cfg.ClusterName)
```

(`typeProvider` and `imageProvider` construction is unchanged here.)

- [ ] **Step 9: Run the full build and tests**

Run: `go build ./... && go test ./pkg/providers/instance/ ./pkg/operator/ -v`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add pkg/apis/v1alpha1/labels.go pkg/operator pkg/providers/instance cmd/controller/main.go
git commit -m "feat: require CLUSTER_NAME and scope managed servers by cluster label"
```

---

## Task 5: Hetzner error → Karpenter error mapping

Map Hetzner API errors to Karpenter's typed errors so the scheduler falls back to other types/zones instead of hard-failing.

**Files:**
- Create: `pkg/providers/instance/errors.go`
- Test: `pkg/providers/instance/errors_test.go` (create)

- [ ] **Step 1: Write the failing test**

Create `pkg/providers/instance/errors_test.go`:

```go
package instance

import (
	"errors"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func TestMapCreateError(t *testing.T) {
	cases := []struct {
		name         string
		in           error
		wantInsuffic bool
		wantNil      bool
	}{
		{"nil", nil, false, true},
		{"unavailable", hcloud.Error{Code: hcloud.ErrorCodeResourceUnavailable}, true, false},
		{"resource-limit", hcloud.Error{Code: hcloud.ErrorCodeResourceLimitExceeded}, true, false},
		{"rate-limit", hcloud.Error{Code: hcloud.ErrorCodeRateLimitExceeded}, false, false},
		{"other", errors.New("boom"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MapCreateError(tc.in)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil, got %v", got)
				}
				return
			}
			if karpcp.IsInsufficientCapacityError(got) != tc.wantInsuffic {
				t.Errorf("IsInsufficientCapacityError=%v, want %v (err=%v)",
					karpcp.IsInsufficientCapacityError(got), tc.wantInsuffic, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to confirm it fails**

Run: `go test ./pkg/providers/instance/ -run TestMapCreateError -v`
Expected: FAIL — `MapCreateError` undefined.

- [ ] **Step 3: Implement the mapping**

Create `pkg/providers/instance/errors.go`:

```go
package instance

import (
	"fmt"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

// MapCreateError converts a Hetzner create error into the appropriate Karpenter
// error type. Capacity/quota errors become InsufficientCapacityError so the
// scheduler tries another instance type or zone; nil passes through as nil.
func MapCreateError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case hcloud.IsError(err, hcloud.ErrorCodeResourceUnavailable),
		hcloud.IsError(err, hcloud.ErrorCodeResourceLimitExceeded):
		return karpcp.NewInsufficientCapacityError(err)
	default:
		// Rate limits and transient errors stay as ordinary errors so Karpenter
		// requeues and retries the same offering.
		return fmt.Errorf("creating server: %w", err)
	}
}
```

- [ ] **Step 4: Run the test to confirm it passes**

Run: `go test ./pkg/providers/instance/ -run TestMapCreateError -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/providers/instance/errors.go pkg/providers/instance/errors_test.go
git commit -m "feat: map Hetzner capacity errors to Karpenter InsufficientCapacityError"
```

---

## Task 6: Robust `instance.Create` (wait for action + public net + error mapping)

`Create` returns before the server is actually provisioned and maps no errors. Wait on the create action(s), apply public-IP settings, and map errors.

**Files:**
- Modify: `pkg/providers/instance/instance.go`
- Test: `pkg/providers/instance/instance_test.go` (extend with a mock action waiter)

- [ ] **Step 1: Write the failing test (waiter is called; public net set; error mapped)**

Add to `pkg/providers/instance/instance_test.go`:

```go
type mockActionWaiter struct {
	waited int
	err    error
}

func (m *mockActionWaiter) WaitFor(_ context.Context, actions ...*hcloud.Action) error {
	m.waited += len(actions)
	return m.err
}

func TestCreate_WaitsForActionsAndSetsPublicNet(t *testing.T) {
	client := newMockServerClient()
	client.action = &hcloud.Action{ID: 1}
	waiter := &mockActionWaiter{}
	p := NewProviderWithWaiter(client, "test-cluster", waiter)

	disabled := false
	_, err := p.Create(context.Background(), CreateOpts{
		Name: "n", ServerType: "cx22", Location: "nbg1", Image: &hcloud.Image{ID: 1},
		EnablePublicIPv4: &disabled, EnablePublicIPv6: nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if waiter.waited == 0 {
		t.Error("expected the action waiter to be called")
	}
	if client.lastOpts.PublicNet == nil || client.lastOpts.PublicNet.EnableIPv4 {
		t.Error("expected public IPv4 to be disabled in create opts")
	}
}

func TestCreate_MapsCapacityError(t *testing.T) {
	client := newMockServerClient()
	client.createErr = hcloud.Error{Code: hcloud.ErrorCodeResourceUnavailable}
	p := NewProvider(client, "test-cluster")
	_, err := p.Create(context.Background(), CreateOpts{Name: "n", ServerType: "cx22", Location: "nbg1", Image: &hcloud.Image{ID: 1}})
	if !karpcp.IsInsufficientCapacityError(err) {
		t.Errorf("expected InsufficientCapacityError, got %v", err)
	}
}
```

Extend `mockServerClient` (in the same file) with the new fields and behavior:

```go
// add to the mockServerClient struct:
//   action    *hcloud.Action
//   createErr error
//   lastOpts  hcloud.ServerCreateOpts
```

And update its `Create` method to honor them:

```go
func (m *mockServerClient) Create(_ context.Context, opts hcloud.ServerCreateOpts) (hcloud.ServerCreateResult, *hcloud.Response, error) {
	m.lastOpts = opts
	if m.createErr != nil {
		return hcloud.ServerCreateResult{}, nil, m.createErr
	}
	id := m.nextID
	m.nextID++
	server := &hcloud.Server{ID: id, Name: opts.Name, Labels: opts.Labels, ServerType: opts.ServerType, Location: opts.Location}
	m.servers[id] = server
	return hcloud.ServerCreateResult{Server: server, Action: m.action}, nil, nil
}
```

Add the karpcp import to the test file: `karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"`.

- [ ] **Step 2: Run to confirm it fails**

Run: `go test ./pkg/providers/instance/ -run TestCreate_ -v`
Expected: FAIL — `NewProviderWithWaiter`, `ActionWaiter`, and `CreateOpts.EnablePublicIPv4` undefined.

- [ ] **Step 3: Implement the waiter, public net, error mapping**

In `pkg/providers/instance/instance.go`:

Add the waiter interface and wire it into the provider:

```go
// ActionWaiter waits for hcloud actions to complete. *hcloud.ActionClient satisfies it.
type ActionWaiter interface {
	WaitFor(ctx context.Context, actions ...*hcloud.Action) error
}

type Provider struct {
	client      ServerClient
	waiter      ActionWaiter
	clusterName string
}

func NewProvider(client ServerClient, clusterName string) *Provider {
	return &Provider{client: client, clusterName: clusterName}
}

// NewProviderWithWaiter is used when an action waiter is available (production)
// and in tests.
func NewProviderWithWaiter(client ServerClient, clusterName string, waiter ActionWaiter) *Provider {
	return &Provider{client: client, waiter: waiter, clusterName: clusterName}
}
```

Add public-IP fields to `CreateOpts`:

```go
	EnablePublicIPv4 *bool
	EnablePublicIPv6 *bool
```

In `Create`, set `PublicNet` on `createOpts` before the API call:

```go
	createOpts.PublicNet = &hcloud.ServerCreatePublicNet{
		EnableIPv4: opts.EnablePublicIPv4 == nil || *opts.EnablePublicIPv4,
		EnableIPv6: opts.EnablePublicIPv6 == nil || *opts.EnablePublicIPv6,
	}
```

Replace the create-and-return tail of `Create`:

```go
	result, _, err := p.client.Create(ctx, createOpts)
	if err != nil {
		return nil, MapCreateError(err)
	}

	// Wait for the create action and any follow-up actions so we only return a
	// server that is actually being provisioned.
	if p.waiter != nil {
		actions := make([]*hcloud.Action, 0, 1+len(result.NextActions))
		if result.Action != nil {
			actions = append(actions, result.Action)
		}
		actions = append(actions, result.NextActions...)
		if len(actions) > 0 {
			if err := p.waiter.WaitFor(ctx, actions...); err != nil {
				return nil, fmt.Errorf("waiting for server %q create actions: %w", opts.Name, err)
			}
		}
	}
	return result.Server, nil
```

- [ ] **Step 4: Wire the real waiter in main.go**

In `cmd/controller/main.go`, change the instance provider construction:

```go
	instanceProvider := instance.NewProviderWithWaiter(&hcloudClient.Server, cfg.ClusterName, &hcloudClient.Action)
```

- [ ] **Step 5: Pass public-IP flags from the cloudprovider**

In `pkg/cloudprovider/cloudprovider.go` `Create`, add to the `instance.CreateOpts{...}` literal:

```go
		EnablePublicIPv4: nodeClass.Spec.EnablePublicIPv4,
		EnablePublicIPv6: nodeClass.Spec.EnablePublicIPv6,
```

- [ ] **Step 6: Run tests and build**

Run: `go build ./... && go test ./pkg/providers/instance/ -v`
Expected: PASS (all Create tests green).

- [ ] **Step 7: Commit**

```bash
git add pkg/providers/instance pkg/cloudprovider/cloudprovider.go cmd/controller/main.go
git commit -m "feat: wait for create actions, set public-IP opts, map create errors"
```

---

## Task 7: Net hourly pricing + IPv4 cost folding

Use the real Net hourly price and add the primary-IPv4 charge to the price when public IPv4 is enabled, so cost-optimal scheduling is accurate.

**Files:**
- Modify: `pkg/providers/instancetype/instancetype.go`
- Test: `pkg/providers/instancetype/instancetype_test.go` (extend)

- [ ] **Step 1: Write the failing test**

Add to `pkg/providers/instancetype/instancetype_test.go`:

```go
func TestHourlyNetPrice(t *testing.T) {
	// Prefer hourly net; fall back to monthly net / 730.
	if got := hourlyNetPrice(hcloud.ServerTypePricing{
		Hourly:  hcloud.Price{Net: "0.0100"},
		Monthly: hcloud.Price{Net: "7.3000"},
	}); got != 0.01 {
		t.Errorf("want 0.01 from hourly net, got %v", got)
	}
	got := hourlyNetPrice(hcloud.ServerTypePricing{Monthly: hcloud.Price{Net: "7.3000"}})
	if got < 0.0099 || got > 0.0101 {
		t.Errorf("want ~0.01 from monthly net/730, got %v", got)
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

Run: `go test ./pkg/providers/instancetype/ -run TestHourlyNetPrice -v`
Expected: FAIL — `hourlyNetPrice` undefined.

- [ ] **Step 3: Implement Net pricing**

In `pkg/providers/instancetype/instancetype.go`, replace `monthlyToHourly` with:

```go
// hourlyNetPrice returns the net hourly price for a server-type pricing entry,
// preferring the explicit hourly figure and falling back to monthly/730.
func hourlyNetPrice(p hcloud.ServerTypePricing) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(p.Hourly.Net), 64); err == nil && v > 0 {
		return v
	}
	if v, err := strconv.ParseFloat(strings.TrimSpace(p.Monthly.Net), 64); err == nil {
		return v / 730
	}
	return 0
}
```

In `toInstanceType`, replace the offering price line:

```go
		price := hourlyNetPrice(*p)
```

Note: `st.Pricings` is `[]ServerTypePricing`, so within the loop `p` is `*hcloud.ServerTypePricing`; pass `*p`. Confirm the loop variable type and adjust (`hourlyNetPrice(p)` if it is already a value).

- [ ] **Step 4: Run tests**

Run: `go test ./pkg/providers/instancetype/ -v`
Expected: PASS. (The IPv4 fold is handled in the cloudprovider where the NodeClass is known; see Step 5.)

- [ ] **Step 5: Document the IPv4-fold decision inline**

Pricing in `instancetype` is NodeClass-agnostic (it has no public-IP context), so the primary-IPv4 charge cannot be folded here without coupling the provider to a NodeClass. Add a short comment above `hourlyNetPrice` recording that the IPv4 surcharge is intentionally excluded from the catalog price and that `enablePublicIPv4=false` is the cost lever:

```go
// Pricing here is the server-type base net price and intentionally excludes the
// primary-IPv4 surcharge: the catalog is NodeClass-agnostic. Cost-sensitive
// clusters drop the IPv4 charge with HCloudNodeClass.spec.enablePublicIPv4=false.
```

(This keeps the catalog stable; folding per-NodeClass would fragment the offering set. The cost lever remains effective via the create-time public-net flag from Task 6.)

- [ ] **Step 6: Commit**

```bash
git add pkg/providers/instancetype/instancetype.go pkg/providers/instancetype/instancetype_test.go
git commit -m "feat: use net hourly pricing; document IPv4 cost lever"
```

---

## Task 8: Unavailable-offerings cache

When a create hits `resource_unavailable` for a `(serverType, location)`, mark that offering unavailable for a TTL so the next scheduling pass skips it.

**Files:**
- Create: `pkg/providers/instancetype/unavailable.go`
- Test: `pkg/providers/instancetype/unavailable_test.go` (create)
- Modify: `pkg/providers/instancetype/instancetype.go` (consult cache when building `Available`)
- Modify: `pkg/cloudprovider/cloudprovider.go` (mark on capacity error)

- [ ] **Step 1: Write the failing test**

Create `pkg/providers/instancetype/unavailable_test.go`:

```go
package instancetype

import (
	"testing"
	"time"
)

func TestUnavailableCache(t *testing.T) {
	now := time.Now()
	c := newUnavailableCache(10 * time.Minute)
	c.nowFn = func() time.Time { return now }

	if c.isUnavailable("cx22", "nbg1") {
		t.Fatal("nothing marked yet")
	}
	c.markUnavailable("cx22", "nbg1")
	if !c.isUnavailable("cx22", "nbg1") {
		t.Fatal("should be unavailable after marking")
	}
	if c.isUnavailable("cx22", "fsn1") {
		t.Fatal("other location must be unaffected")
	}

	now = now.Add(11 * time.Minute) // expire
	if c.isUnavailable("cx22", "nbg1") {
		t.Fatal("entry should have expired")
	}
}
```

- [ ] **Step 2: Run to confirm it fails**

Run: `go test ./pkg/providers/instancetype/ -run TestUnavailableCache -v`
Expected: FAIL — `newUnavailableCache` undefined.

- [ ] **Step 3: Implement the cache**

Create `pkg/providers/instancetype/unavailable.go`:

```go
package instancetype

import (
	"sync"
	"time"
)

// unavailableCache tracks (serverType, location) offerings that recently failed
// to provision with a capacity error, so they are reported unavailable for a TTL.
type unavailableCache struct {
	ttl   time.Duration
	mu    sync.RWMutex
	items map[string]time.Time // key -> expiry
	nowFn func() time.Time
}

func newUnavailableCache(ttl time.Duration) *unavailableCache {
	return &unavailableCache{ttl: ttl, items: map[string]time.Time{}, nowFn: time.Now}
}

func key(serverType, location string) string { return serverType + "\x00" + location }

func (c *unavailableCache) markUnavailable(serverType, location string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key(serverType, location)] = c.nowFn().Add(c.ttl)
}

func (c *unavailableCache) isUnavailable(serverType, location string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	exp, ok := c.items[key(serverType, location)]
	return ok && c.nowFn().Before(exp)
}
```

- [ ] **Step 4: Run to confirm it passes**

Run: `go test ./pkg/providers/instancetype/ -run TestUnavailableCache -v`
Expected: PASS.

- [ ] **Step 5: Wire the cache into the provider and offering availability**

In `pkg/providers/instancetype/instancetype.go`, add a `unavailable *unavailableCache` field to `Provider`, initialize it in `NewProvider` with a 5-minute TTL, and expose marking:

```go
func NewProvider(client ServerTypeClient) *Provider {
	return &Provider{client: client, unavailable: newUnavailableCache(5 * time.Minute)}
}

// MarkUnavailable records that a (serverType, location) offering failed with a
// capacity error so it is reported unavailable for a TTL.
func (p *Provider) MarkUnavailable(serverType, location string) {
	p.unavailable.markUnavailable(serverType, location)
}
```

`toInstanceType` is a free function with no access to the cache; move availability computation into `List` after building types. The simplest correct approach: make `toInstanceType` a method `(p *Provider) toInstanceType(st)` and set each offering's `Available` from `!p.unavailable.isUnavailable(st.Name, p.Location.Name)`. Update the offering build loop:

```go
			Available: !p.unavailable.isUnavailable(st.Name, p.Location.Name),
```

and change the call site in `List` from `toInstanceType(st)` to `p.toInstanceType(st)`. Update the existing `TestToInstanceType` (if any) to call through a `Provider`.

- [ ] **Step 6: Mark unavailable from the cloudprovider on capacity error**

In `pkg/cloudprovider/cloudprovider.go` `Create`, wrap the instance create so a capacity failure marks the offering. Replace the create-error handling:

```go
	server, err := cp.instanceProvider.Create(ctx, instance.CreateOpts{ /* ...unchanged... */ })
	if err != nil {
		if karpcp.IsInsufficientCapacityError(err) {
			cp.typeProvider.MarkUnavailable(selected.Name, location)
		}
		return nil, fmt.Errorf("creating server: %w", err)
	}
```

(`karpcp` is already imported in this file.)

- [ ] **Step 7: Run build + tests**

Run: `go build ./... && go test ./pkg/providers/instancetype/ ./pkg/cloudprovider/ -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/providers/instancetype pkg/cloudprovider/cloudprovider.go
git commit -m "feat: cache unavailable offerings and skip them after capacity errors"
```

---

## Task 9: Complete drift detection

Implement every declared `DriftReason`; add server-type and firewall drift; remove any reason that cannot be reliably detected (document it).

**Files:**
- Modify: `pkg/cloudprovider/cloudprovider.go` (`IsDrifted` + drift reason consts)
- Test: `pkg/cloudprovider/cloudprovider_test.go` (extend)

- [ ] **Step 1: Write failing tests for firewall and server-type drift**

Add to `pkg/cloudprovider/cloudprovider_test.go` (using the fake clients introduced/expanded in Task 11; if Task 11 is not yet done, define a minimal fake inline). Tests:

```go
func TestIsDrifted_Firewall(t *testing.T) {
	cp, nc, nodeClaim := newDriftFixture(t)
	nc.Spec.FirewallIDs = []int64{7}
	// server has firewall 9, not 7 -> drift
	setServerFirewalls(cp, nodeClaim, []int64{9})
	reason, err := cp.IsDrifted(context.Background(), nodeClaim)
	if err != nil {
		t.Fatal(err)
	}
	if reason != DriftFirewall {
		t.Errorf("want FirewallDrift, got %q", reason)
	}
}

func TestIsDrifted_ServerType(t *testing.T) {
	cp, _, nodeClaim := newDriftFixture(t)
	setServerType(cp, nodeClaim, "cx32") // claim recorded cx22
	reason, err := cp.IsDrifted(context.Background(), nodeClaim)
	if err != nil {
		t.Fatal(err)
	}
	if reason != DriftServerType {
		t.Errorf("want ServerTypeDrift, got %q", reason)
	}
}
```

> The helpers `newDriftFixture`, `setServerFirewalls`, `setServerType` are defined in Task 11's expanded test harness. If implementing Task 9 before Task 11, add a minimal fake `instanceProvider`/`kubeClient` inline mirroring Task 11 Step 3. The behavior asserted (the new `DriftFirewall` / `DriftServerType` reasons) is what this task implements.

- [ ] **Step 2: Run to confirm it fails**

Run: `go test ./pkg/cloudprovider/ -run TestIsDrifted -v`
Expected: FAIL — `DriftServerType` undefined / firewall drift not detected.

- [ ] **Step 3: Implement the drift checks**

In `pkg/cloudprovider/cloudprovider.go`, add the new reason:

```go
const (
	DriftImage      karpcp.DriftReason = "ImageDrift"
	DriftNetwork    karpcp.DriftReason = "NetworkDrift"
	DriftFirewall   karpcp.DriftReason = "FirewallDrift"
	DriftServerType karpcp.DriftReason = "ServerTypeDrift"
)
```

In `IsDrifted`, after the network check and before `return "", nil`, add firewall and server-type checks:

```go
	// Firewall drift: every NodeClass firewall must be attached to the server.
	if len(nodeClass.Spec.FirewallIDs) > 0 {
		attached := map[int64]bool{}
		for _, fw := range server.PublicNet.Firewalls {
			if fw.Firewall != nil {
				attached[fw.Firewall.ID] = true
			}
		}
		for _, want := range nodeClass.Spec.FirewallIDs {
			if !attached[want] {
				return DriftFirewall, nil
			}
		}
	}

	// Server-type drift: the running server type must match the type recorded on
	// the NodeClaim's instance-type label.
	if want := nodeClaim.Labels[corev1.LabelInstanceTypeStable]; want != "" &&
		server.ServerType != nil && server.ServerType.Name != want {
		return DriftServerType, nil
	}
```

Verify `server.PublicNet.Firewalls` is the correct path for attached firewalls in hcloud-go v2 (`hcloud.Server.PublicNet.Firewalls []*ServerFirewallStatus`, each with a `Firewall *Firewall`). Adjust field access to match the version if needed.

- [ ] **Step 4: Document the non-detectable reasons**

Add a comment in `IsDrifted` explaining that SSH-key and userData drift are intentionally not detected because Hetzner does not reliably echo applied SSH keys or user-data post-create:

```go
	// SSH-key and user-data drift are intentionally not checked: Hetzner does not
	// reliably expose applied SSH keys or user-data after create, so a comparison
	// would produce false positives. They are omitted rather than faked.
```

(There is no `DriftSSHKey`/`DriftUserData` constant — none was declared, so nothing to remove. Confirm no such constant exists.)

- [ ] **Step 5: Run tests**

Run: `go test ./pkg/cloudprovider/ -run TestIsDrifted -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/cloudprovider/cloudprovider.go pkg/cloudprovider/cloudprovider_test.go
git commit -m "feat: complete drift detection (firewall, server-type) and document omissions"
```

---

## Task 10: NodeClass status controller (envtest)

Add a reconciler that validates an `HCloudNodeClass` against live Hetzner, populates `Status.ResolvedImages`, and sets `ImagesReady`/`NetworkReady`/`Ready`. Without it, Karpenter never sees a Ready NodeClass.

**Files:**
- Create: `pkg/controllers/nodeclass/controller.go`
- Create: `pkg/controllers/nodeclass/suite_test.go`
- Modify: `cmd/controller/main.go` (register the controller)
- Modify: `Makefile` + `.github/workflows/ci.yaml` (envtest binaries)

- [ ] **Step 1: Add envtest tooling to the Makefile**

Append to `Makefile`:

```makefile
ENVTEST := go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest
ENVTEST_K8S_VERSION ?= 1.34.0

.PHONY: test-envtest
test-envtest:
	KUBEBUILDER_ASSETS="$$($(ENVTEST) use $(ENVTEST_K8S_VERSION) -p path)" \
		go test -race -count=1 ./pkg/controllers/...
```

- [ ] **Step 2: Write the controller**

Create `pkg/controllers/nodeclass/controller.go`:

```go
package nodeclass

import (
	"context"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	"k8s.io/apimachinery/pkg/api/equality"
	controllerruntime "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
)

// NetworkGetter is the narrow hcloud networks API the controller needs.
type NetworkGetter interface {
	GetByID(ctx context.Context, id int64) (*hcloud.Network, *hcloud.Response, error)
}

// Controller reconciles HCloudNodeClass status.
type Controller struct {
	kubeClient client.Client
	networks   NetworkGetter
	images     *imagefamily.Provider
}

func NewController(kubeClient client.Client, networks NetworkGetter, images *imagefamily.Provider) *Controller {
	return &Controller{kubeClient: kubeClient, networks: networks, images: images}
}

func (c *Controller) Name() string { return "nodeclass.status" }

func (c *Controller) Reconcile(ctx context.Context, nc *v1alpha1.HCloudNodeClass) (reconcile.Result, error) {
	stored := nc.DeepCopy()

	// Network validation.
	net, _, err := c.networks.GetByID(ctx, nc.Spec.NetworkID)
	switch {
	case err != nil:
		nc.StatusConditions().SetUnknownWithReason(v1alpha1.ConditionTypeNetworkReady, "NetworkCheckFailed", err.Error())
	case net == nil:
		nc.StatusConditions().SetFalse(v1alpha1.ConditionTypeNetworkReady, "NetworkNotFound", "configured networkID does not exist")
	default:
		nc.StatusConditions().SetTrue(v1alpha1.ConditionTypeNetworkReady)
	}

	// Image resolution for both architectures.
	resolved, ierr := c.resolveImages(ctx, nc)
	if ierr != nil {
		nc.StatusConditions().SetFalse(v1alpha1.ConditionTypeImagesReady, "ImageResolutionFailed", ierr.Error())
	} else {
		nc.Status.ResolvedImages = resolved
		nc.StatusConditions().SetTrue(v1alpha1.ConditionTypeImagesReady)
	}

	if !equality.Semantic.DeepEqual(stored, nc) {
		if err := c.kubeClient.Status().Update(ctx, nc); err != nil {
			return reconcile.Result{}, err
		}
	}
	return reconcile.Result{}, nil
}

func (c *Controller) resolveImages(ctx context.Context, nc *v1alpha1.HCloudNodeClass) ([]v1alpha1.ResolvedImage, error) {
	var out []v1alpha1.ResolvedImage
	for _, arch := range []hcloud.Architecture{hcloud.ArchitectureX86, hcloud.ArchitectureARM} {
		img, err := c.images.Resolve(ctx, nc.Spec.ImageSelector, arch)
		if err != nil {
			return nil, err
		}
		for _, loc := range nc.Spec.Locations {
			out = append(out, v1alpha1.ResolvedImage{Location: loc, ImageID: img.ID})
		}
	}
	return out, nil
}

// Register wires the controller into the manager.
func (c *Controller) Register(_ context.Context, m manager.Manager) error {
	return controllerruntime.NewControllerManagedBy(m).
		For(&v1alpha1.HCloudNodeClass{}).
		Named(c.Name()).
		Complete(reconcile.AsReconciler(m.GetClient(), c))
}
```

> Confirm `reconcile.AsReconciler` exists in the pinned controller-runtime (v0.24); the karpenter readiness controller uses the same object-typed reconcile pattern. If the helper name differs, adapt to the version's typed-reconciler helper.

- [ ] **Step 3: Write the envtest suite**

Create `pkg/controllers/nodeclass/suite_test.go`:

```go
package nodeclass

import (
	"context"
	"testing"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
)

type fakeNetworks struct{ net *hcloud.Network }

func (f fakeNetworks) GetByID(_ context.Context, _ int64) (*hcloud.Network, *hcloud.Response, error) {
	return f.net, nil, nil
}

type fakeImages struct{ img *hcloud.Image }

func (f fakeImages) AllWithOpts(_ context.Context, _ hcloud.ImageListOpts) ([]*hcloud.Image, error) {
	return []*hcloud.Image{f.img}, nil
}

func TestReconcile_SetsReadyWhenValid(t *testing.T) {
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := &v1alpha1.HCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec: v1alpha1.HCloudNodeClassSpec{
			Locations:     []string{"nbg1"},
			NetworkID:     1,
			ImageSelector: v1alpha1.ImageSelector{Family: "ubuntu"},
		},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).
		WithObjects(nc).WithStatusSubresource(nc).Build()

	img := imagefamily.NewProvider(fakeImages{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}})
	c := NewController(kube, fakeNetworks{net: &hcloud.Network{ID: 1}}, img)

	if _, err := c.Reconcile(context.Background(), nc.DeepCopy()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	got := &v1alpha1.HCloudNodeClass{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(nc), got); err != nil {
		t.Fatal(err)
	}
	if !got.StatusConditions().Get(v1alpha1.ConditionTypeNetworkReady).IsTrue() {
		t.Error("NetworkReady should be true")
	}
	if len(got.Status.ResolvedImages) == 0 {
		t.Error("expected resolved images")
	}
	if !got.StatusConditions().Root().IsTrue() {
		t.Error("Ready should be true when all dependents are true")
	}
}
```

Add the `client` import (`sigs.k8s.io/controller-runtime/pkg/client`). This uses the controller-runtime fake client (no external envtest binary required for this test), so it runs under plain `go test` too.

- [ ] **Step 4: Run the test**

Run: `go test ./pkg/controllers/nodeclass/ -v`
Expected: PASS (fake client; conditions true, images resolved).

- [ ] **Step 5: Register the controller in main.go**

In `cmd/controller/main.go`, after `baseCloudProvider` is created and before/with `op.WithControllers(...)`, add the nodeclass controller to the controller list:

```go
	nodeClassController := nodeclass.NewController(op.GetClient(), &hcloudClient.Network, imageProvider)
```

and pass it into `WithControllers` alongside the core controllers:

```go
	op.WithControllers(ctx, append(
		controllers.NewControllers(ctx, op.Manager, op.Clock, op.GetClient(), op.EventRecorder, cloudProvider, baseCloudProvider, clusterState, op.InstanceTypeStore),
		nodeClassController,
	)...).Start(ctx)
```

Add the import `"github.com/paperclipinc/karpenter-provider-hetzner/pkg/controllers/nodeclass"`. (`controllers.NewControllers` returns `[]controller.Controller`; `nodeClassController` satisfies `controller.Controller` via `Register`.)

- [ ] **Step 6: Add an envtest CI job**

Add to `.github/workflows/ci.yaml` under `jobs:`:

```yaml
  controllers:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26"
      - run: go test -race -count=1 ./pkg/controllers/...
```

(The fake-client test needs no envtest binary; the `test-envtest` Makefile target remains available for future real-apiserver tests.)

- [ ] **Step 7: Build + test**

Run: `go build ./... && go test ./pkg/controllers/... -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add pkg/controllers cmd/controller/main.go Makefile .github/workflows/ci.yaml
git commit -m "feat: NodeClass status controller (network + image validation, Ready)"
```

---

## Task 11: Expand cloudprovider tests to all 8 interface methods

`cloudprovider_test.go` is 38 lines. Build a fake-client harness and exercise every CloudProvider method.

**Files:**
- Modify: `pkg/cloudprovider/cloudprovider_test.go`

- [ ] **Step 1: Write the harness + tests**

Replace/extend `pkg/cloudprovider/cloudprovider_test.go` with a fixture that builds a `CloudProvider` from fake providers and a fake kube client holding an `HCloudNodeClass`, plus the helpers referenced in Task 9 (`newDriftFixture`, `setServerFirewalls`, `setServerType`). Cover:

```go
// Tests to add (each with the shared fixture):
// - TestName: cp.Name() == "hetzner"
// - TestGetSupportedNodeClasses: returns one *HCloudNodeClass
// - TestRepairPolicies: returns the two NodeReady policies
// - TestGetInstanceTypes_NilNodePool: returns the catalog
// - TestCreate_Success: provisions, sets ProviderID hcloud://<id>, Capacity set
// - TestCreate_InsufficientCapacity: fake create returns capacity error ->
//     err is InsufficientCapacityError AND typeProvider.MarkUnavailable called
// - TestGet_NotFound: unknown providerID -> NodeClaimNotFoundError
// - TestList: returns NodeClaims for managed servers
// - TestDelete: calls instance delete
// - TestIsDrifted_* : image/network/firewall/server-type (Task 9)
```

Implement the fixture using the existing in-package mock style. Key pieces:

```go
func newDriftFixture(t *testing.T) (*CloudProvider, *v1alpha1.HCloudNodeClass, *karpv1.NodeClaim) {
	t.Helper()
	_ = v1alpha1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := &v1alpha1.HCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Spec:       v1alpha1.HCloudNodeClassSpec{Locations: []string{"nbg1"}, NetworkID: 1, ImageSelector: v1alpha1.ImageSelector{Family: "ubuntu"}},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc).WithStatusSubresource(nc).Build()
	// fake hcloud server backing the claim:
	srv := &hcloud.Server{ID: 50, ServerType: &hcloud.ServerType{Name: "cx22"}, Image: &hcloud.Image{ID: 42}}
	srvClient := &fakeServerClient{servers: map[int64]*hcloud.Server{50: srv}}
	stClient := &fakeServerTypeClient{ /* cx22 priced in nbg1 */ }
	imgClient := &fakeImageClient{img: &hcloud.Image{ID: 42, Description: "Ubuntu 24.04"}}
	cp := NewCloudProvider(kube,
		instance.NewProvider(srvClient, "test-cluster"),
		instancetype.NewProvider(stClient),
		imagefamily.NewProvider(imgClient))
	nodeClaim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim", Labels: map[string]string{corev1.LabelInstanceTypeStable: "cx22"}},
	}
	nodeClaim.Spec.NodeClassRef = &karpv1.NodeClassReference{Name: "default", Group: v1alpha1.Group, Kind: "HCloudNodeClass"}
	nodeClaim.Status.ProviderID = instance.FormatProviderID(50)
	nodeClaim.Status.ImageID = "42"
	return cp, nc, nodeClaim
}

func setServerFirewalls(cp *CloudProvider, claim *karpv1.NodeClaim, ids []int64) {
	id, _ := instance.ParseProviderID(claim.Status.ProviderID)
	srv := cp.instanceProvider.MustGetForTest(id) // add a tiny test accessor, or reach via the fake
	var fws []hcloud.ServerFirewallStatus
	for _, fid := range ids {
		fws = append(fws, hcloud.ServerFirewallStatus{Firewall: &hcloud.Firewall{ID: fid}})
	}
	srv.PublicNet.Firewalls = fwsPtr(fws)
}
```

> Implementation note: the cleanest way to mutate the backing server is to keep a reference to the `fakeServerClient` in the fixture and expose it (return it from `newDriftFixture` or store it on a small struct), rather than adding production accessors. Prefer returning the fake clients from the fixture and mutating their maps directly in the helpers — do **not** add test-only methods to production types.

Define the fakes (`fakeServerClient`, `fakeServerTypeClient`, `fakeImageClient`) mirroring the narrow interfaces, with fields to inject `createErr` and to read `markedUnavailable`.

- [ ] **Step 2: Run to confirm new tests fail/compile-error first, then pass after wiring**

Run: `go test ./pkg/cloudprovider/ -v`
Expected: after the fixture compiles, all listed tests PASS. (Iterate until green.)

- [ ] **Step 3: Confirm coverage of all 8 methods**

Run: `go test ./pkg/cloudprovider/ -cover`
Expected: coverage materially higher than baseline; every method has at least one test.

- [ ] **Step 4: Commit**

```bash
git add pkg/cloudprovider/cloudprovider_test.go
git commit -m "test: cover all CloudProvider methods with fake-client harness"
```

---

## Task 12: Helm deployment hardening

Inject `CLUSTER_NAME`, add health/metrics ports and probes, and expose leader election so the chart deploys a production-shaped controller.

**Files:**
- Modify: `charts/karpenter-provider-hetzner/values.yaml`
- Modify: `charts/karpenter-provider-hetzner/templates/deployment.yaml`

- [ ] **Step 1: Add values**

Add to `charts/karpenter-provider-hetzner/values.yaml`:

```yaml
# Required: scopes managed servers so multiple clusters can share one Hetzner project.
clusterName: ""

healthProbe:
  port: 8081
metrics:
  port: 8080
```

- [ ] **Step 2: Fail fast if clusterName is unset**

At the top of `charts/karpenter-provider-hetzner/templates/deployment.yaml`, add:

```yaml
{{- if not .Values.clusterName }}
{{- fail "clusterName is required: set --set clusterName=<your-cluster>" }}
{{- end }}
```

- [ ] **Step 3: Add the env var, ports, and probes**

In the container spec, add `CLUSTER_NAME` to `env` (after the `HCLOUD_TOKEN` block):

```yaml
            - name: CLUSTER_NAME
              value: {{ .Values.clusterName | quote }}
```

Add ports and probes (after `resources:`):

```yaml
          ports:
            - name: http-metrics
              containerPort: {{ .Values.metrics.port }}
            - name: http-health
              containerPort: {{ .Values.healthProbe.port }}
          livenessProbe:
            httpGet:
              path: /healthz
              port: http-health
            initialDelaySeconds: 30
          readinessProbe:
            httpGet:
              path: /readyz
              port: http-health
```

> Confirm the operator serves `/healthz` and `/readyz` on the health port and that the env vars Karpenter's operator reads for these ports (e.g. `HEALTH_PROBE_PORT`, `METRICS_PORT`) match; if the operator uses specific env var names, add them to `env` with the values above. Verify against `sigs.k8s.io/karpenter/pkg/operator` options before finalizing the port wiring.

- [ ] **Step 4: Lint the chart**

Run: `helm lint charts/karpenter-provider-hetzner --set clusterName=test`
Expected: `1 chart(s) linted, 0 chart(s) failed`.

- [ ] **Step 5: Render and eyeball**

Run: `helm template charts/karpenter-provider-hetzner --set clusterName=test | grep -A2 CLUSTER_NAME`
Expected: shows `CLUSTER_NAME` with value `test`.

- [ ] **Step 6: Commit**

```bash
git add charts/karpenter-provider-hetzner/values.yaml charts/karpenter-provider-hetzner/templates/deployment.yaml
git commit -m "feat: chart injects CLUSTER_NAME, adds probes and metrics/health ports"
```

---

## Final verification

- [ ] **Run the full suite**

Run: `make generate-verify && go build ./... && go test -race -count=1 ./... && golangci-lint run ./...`
Expected: all green; no generated diff.

- [ ] **Confirm spec coverage** — each spec work item maps to a task:
  1 Build → Task 1; 2 CRD → Task 2; 3 NodeClass controller → Task 10 (+conditions Task 3); 4 Create robustness → Task 6 (+errors Task 5); 5 offerings availability+pricing → Tasks 7, 8; 6 drift → Task 9; 7 tagging+config → Task 4 (+chart Task 12); 8 testing → Tasks 10, 11 (+per-task tests).

- [ ] **Update README status** — bump the "Alpha / not production-ready" note only if the team agrees the bar is met; otherwise leave for spec #4 (repo polish). Do not over-claim.
