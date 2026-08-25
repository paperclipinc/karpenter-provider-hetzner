package pricehealth

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/zapr"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
)

const testNodePool = "default"

// fakeCatalogue answers per NodePool, the way the real cloud provider does: each
// pool sees only the instance types its NodeClass locations allow.
type fakeCatalogue struct {
	byNodePool map[string][]*karpcp.InstanceType
	err        error
	asked      []string
}

func (f *fakeCatalogue) GetInstanceTypes(_ context.Context, np *karpv1.NodePool) ([]*karpcp.InstanceType, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.asked = append(f.asked, np.Name)
	return f.byNodePool[np.Name], nil
}

// offering builds one priced offering for a zone -- the shape this provider
// actually builds, one per pricing location.
func offering(zone string, price float64) *karpcp.Offering {
	return &karpcp.Offering{
		Requirements: scheduling.NewRequirements(
			scheduling.NewRequirement(karpv1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, karpv1.CapacityTypeOnDemand),
			scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
		),
		Price:     price,
		Available: true,
	}
}

// catalogue is cx43 priced in the given zones, scoped to the default NodePool.
func catalogue(offerings ...*karpcp.Offering) map[string][]*karpcp.InstanceType {
	return map[string][]*karpcp.InstanceType{
		testNodePool: {{Name: "cx43", Offerings: offerings}},
	}
}

func nbg1Catalogue() map[string][]*karpcp.InstanceType {
	return catalogue(offering("nbg1", 0.0242))
}

// nodePool builds a NodePool this provider serves.
func nodePool(name string) *karpv1.NodePool {
	np := &karpv1.NodePool{ObjectMeta: metav1.ObjectMeta{Name: name}}
	np.Spec.Template.Spec.NodeClassRef = &karpv1.NodeClassReference{
		Group: apiv1.Group,
		Kind:  "HCloudNodeClass",
		Name:  "default",
	}
	return np
}

// node builds a registered Karpenter-owned node with the labels core prices from.
func node(name, instanceType, zone string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				karpv1.NodePoolLabelKey:        testNodePool,
				karpv1.NodeRegisteredLabelKey:  "true",
				corev1.LabelInstanceTypeStable: instanceType,
				corev1.LabelTopologyZone:       zone,
				karpv1.CapacityTypeLabelKey:    karpv1.CapacityTypeOnDemand,
			},
		},
	}
}

func newFakeClient(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(objs...).Build()
}

// newTestController wires a controller over the default NodePool plus the given
// nodes.
func newTestController(types map[string][]*karpcp.InstanceType, nodes ...client.Object) *Controller {
	objs := append([]client.Object{nodePool(testNodePool)}, nodes...)
	return NewController(newFakeClient(objs...), &fakeCatalogue{byNodePool: types})
}

// metricValue returns the current value of a single-series metric family.
func metricValue(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := crmetrics.Registry.(prometheus.Gatherer).Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		var total float64
		for _, m := range mf.GetMetric() {
			if g := m.GetGauge(); g != nil {
				total += g.GetValue()
			}
			if c := m.GetCounter(); c != nil {
				total += c.GetValue()
			}
		}
		return total
	}
	return 0
}

func scan(t *testing.T, c *Controller) []unpricedNode {
	t.Helper()
	unresolved, err := c.scan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return unresolved
}

func TestReconcile_HealthyNodeResolves(t *testing.T) {
	c := newTestController(nbg1Catalogue(), node("worker", "cx43", "nbg1"))

	if unresolved := scan(t, c); len(unresolved) != 0 {
		t.Errorf("a correctly labelled node failed to price: %v", unresolved)
	}
}

// The failure this exists to catch. hcloud-cloud-controller-manager labels
// nodes with the legacy datacenter by default, while offerings are keyed on the
// location, so the lookup misses and Karpenter prices the node at zero.
func TestReconcile_DatacenterZoneDoesNotResolve(t *testing.T) {
	c := newTestController(nbg1Catalogue(), node("worker", "cx43", "nbg1-dc3"))

	unresolved := scan(t, c)
	if len(unresolved) != 1 || unresolved[0].Node != "worker" {
		t.Errorf("expected the datacenter-labelled node flagged, got %v", unresolved)
	}
}

// Disabling the CCM's zone label leaves nodes Karpenter never registers with no
// zone at all -- created by the very workaround recommended for the case above,
// and permanent, unlike the datacenter labels.
func TestReconcile_MissingZoneDoesNotResolve(t *testing.T) {
	n := node("worker", "cx43", "")
	delete(n.Labels, corev1.LabelTopologyZone)
	c := newTestController(nbg1Catalogue(), n)

	if unresolved := scan(t, c); len(unresolved) != 1 {
		t.Errorf("expected the zone-less node flagged, got %v", unresolved)
	}
}

// An instance type absent from the catalogue cannot be priced either -- the same
// consolidation failure, a different cause.
func TestReconcile_UnknownInstanceTypeDoesNotResolve(t *testing.T) {
	c := newTestController(nbg1Catalogue(), node("worker", "cx99", "nbg1"))

	if unresolved := scan(t, c); len(unresolved) != 1 {
		t.Errorf("expected the unknown-type node flagged, got %v", unresolved)
	}
}

// Karpenter never prices against the whole catalogue: it asks the cloud provider
// per NodePool, which filters to that NodeClass's locations. A node in a zone
// the pool no longer covers prices at zero for core, so checking an unfiltered
// catalogue would report it healthy -- the exact silence this controller exists
// to break.
func TestReconcile_ZoneOutsideTheNodePoolsCatalogueDoesNotResolve(t *testing.T) {
	types := &fakeCatalogue{byNodePool: nbg1Catalogue()}
	c := NewController(newFakeClient(nodePool(testNodePool), node("worker", "cx43", "fsn1")), types)

	unresolved := scan(t, c)
	if len(unresolved) != 1 || unresolved[0].Zone != "fsn1" {
		t.Errorf("expected the out-of-catalogue node flagged, got %v", unresolved)
	}
	// Pin the mechanism, not just the outcome: the catalogue has to be requested
	// per NodePool. A global lookup would resolve offerings core has filtered away.
	if len(types.asked) != 1 || types.asked[0] != testNodePool {
		t.Errorf("catalogue requested for %v, want exactly [%s]", types.asked, testNodePool)
	}
}

// A matching offering is not enough. Consolidation is disabled by the price being
// zero, and hcloud can return a server type whose pricing does not parse, which
// the catalogue keeps as an offering priced at zero.
func TestReconcile_ZeroPricedOfferingDoesNotResolve(t *testing.T) {
	c := newTestController(catalogue(offering("nbg1", 0)), node("worker", "cx43", "nbg1"))

	if unresolved := scan(t, c); len(unresolved) != 1 {
		t.Errorf("expected the zero-priced node flagged, got %v", unresolved)
	}
}

// Control-plane and other unmanaged nodes are not Karpenter's to price.
func TestReconcile_IgnoresNodesKarpenterDoesNotOwn(t *testing.T) {
	n := node("master", "cx43", "nbg1-dc3")
	delete(n.Labels, karpv1.NodePoolLabelKey)
	c := newTestController(nbg1Catalogue(), n)

	if unresolved := scan(t, c); len(unresolved) != 0 {
		t.Errorf("flagged a node Karpenter does not own: %v", unresolved)
	}
}

// Until registration completes Karpenter prices from the NodeClaim's labels, and
// the Node's zone label is written by that same registration. Counting nodes
// mid-handshake would hold the gauge permanently non-zero on a scaling cluster
// and make the alert worthless.
func TestReconcile_IgnoresUnregisteredNode(t *testing.T) {
	n := node("worker", "cx43", "")
	delete(n.Labels, corev1.LabelTopologyZone)
	delete(n.Labels, karpv1.NodeRegisteredLabelKey)
	c := newTestController(nbg1Catalogue(), n)

	if unresolved := scan(t, c); len(unresolved) != 0 {
		t.Errorf("flagged a node that has not finished registering: %v", unresolved)
	}
}

// A second Karpenter provider in the same cluster also labels its nodes with
// karpenter.sh/nodepool. Those nodes are not ours to price, and flagging them
// would alarm permanently on something this operator cannot fix.
func TestReconcile_IgnoresAnotherProvidersNodePool(t *testing.T) {
	foreignPool := &karpv1.NodePool{ObjectMeta: metav1.ObjectMeta{Name: "foreign"}}
	foreignPool.Spec.Template.Spec.NodeClassRef = &karpv1.NodeClassReference{
		Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "default",
	}
	n := node("worker", "cx43", "us-east-1a")
	n.Labels[karpv1.NodePoolLabelKey] = "foreign"

	c := NewController(
		newFakeClient(nodePool(testNodePool), foreignPool, n),
		&fakeCatalogue{byNodePool: nbg1Catalogue()},
	)

	if unresolved := scan(t, c); len(unresolved) != 0 {
		t.Errorf("flagged a node belonging to another provider: %v", unresolved)
	}
}

// Failing to read the catalogue must not be reported as "everything is fine".
// The gauge cannot carry that on its own -- it reads zero from process start, so
// "no broken nodes" and "never managed to look" are the same number -- so the
// scan counter is what has to move.
func TestReconcile_CatalogueErrorDoesNotReportHealthy(t *testing.T) {
	c := NewController(
		newFakeClient(nodePool(testNodePool), node("worker", "cx43", "nbg1-dc3")),
		&fakeCatalogue{err: fmt.Errorf("hcloud unavailable")},
	)

	before := metricValue(t, "karpenter_hetzner_price_health_scan_total")
	if _, err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("a catalogue failure must not fail the sweep: %v", err)
	}
	if after := metricValue(t, "karpenter_hetzner_price_health_scan_total"); after <= before {
		t.Error("a failed scan left no trace on karpenter_hetzner_price_health_scan_total")
	}
}

// The gauge is the deliverable: README tells operators to watch it. Publish it on
// every successful scan, including zero, so it falls back to healthy once the
// cause is fixed rather than latching at its worst value.
func TestReconcile_PublishesTheUnresolvedCount(t *testing.T) {
	c := newTestController(nbg1Catalogue(), node("bad", "cx43", "nbg1-dc3"))
	if _, err := c.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := metricValue(t, "karpenter_hetzner_nodes_price_unresolved"); got != 1 {
		t.Errorf("gauge = %v, want 1", got)
	}

	healthy := newTestController(nbg1Catalogue(), node("good", "cx43", "nbg1"))
	if _, err := healthy.Reconcile(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := metricValue(t, "karpenter_hetzner_nodes_price_unresolved"); got != 0 {
		t.Errorf("gauge = %v after the cause was fixed, want 0", got)
	}
}

// The whole value of this controller is that the log names the cause. The logger
// encodes arbitrary values by reflection and never consults fmt.Stringer, so
// unexported fields would render as "nodes":[{}] -- a line that looks
// informative and carries nothing, with no test failing to say so.
func TestReconcile_UnpricedNodesRenderInLogOutput(t *testing.T) {
	var buf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(&buf), zapcore.DebugLevel)
	ctx := logf.IntoContext(context.Background(), zapr.NewLogger(zap.New(core)))

	c := newTestController(nbg1Catalogue(), node("worker-abc", "cx43", "nbg1-dc3"))
	if _, err := c.Reconcile(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, want := range []string{"worker-abc", "cx43", "nbg1-dc3", "on-demand"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("log output does not name %q; got %s", want, buf.String())
		}
	}
}

// The headline cause is the default CCM configuration, which misses on every node
// at once -- so the common case is a fleet-sized list. An uncapped line is the one
// most likely to be truncated or dropped by a log pipeline, losing the examples
// that name the cause.
func TestReconcile_CapsTheNodesItNames(t *testing.T) {
	nodes := make([]client.Object, 0, maxLoggedNodes+5)
	for i := range maxLoggedNodes + 5 {
		nodes = append(nodes, node(fmt.Sprintf("worker-%d", i), "cx43", "nbg1-dc3"))
	}

	var buf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
		zapcore.AddSync(&buf), zapcore.DebugLevel)
	ctx := logf.IntoContext(context.Background(), zapr.NewLogger(zap.New(core)))

	c := newTestController(nbg1Catalogue(), nodes...)
	if _, err := c.Reconcile(ctx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got := bytes.Count(buf.Bytes(), []byte(`"instanceType"`)); got != maxLoggedNodes {
		t.Errorf("named %d nodes, want the cap of %d", got, maxLoggedNodes)
	}
	// The count stays exact even though the list is a sample.
	if !bytes.Contains(buf.Bytes(), []byte(`"count":15`)) {
		t.Errorf("log does not carry the full count; got %s", buf.String())
	}
}

func TestReconcile_KeepsCadenceOnError(t *testing.T) {
	c := NewController(newFakeClient(), &fakeCatalogue{err: fmt.Errorf("hcloud unavailable")})

	res, err := c.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("a catalogue failure must not fail the sweep: %v", err)
	}
	if res.RequeueAfter != resyncInterval {
		t.Errorf("expected the sweep to keep its cadence, got %v", res.RequeueAfter)
	}
}
