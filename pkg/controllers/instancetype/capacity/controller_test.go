package capacity

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
)

// recordedCall captures what the controller handed the provider.
type recordedCall struct {
	serverType string
	nodeClass  string
	observed   resource.Quantity
}

type fakeRecorder struct{ calls []recordedCall }

func (f *fakeRecorder) Record(serverType string, nodeClass *apiv1.HCloudNodeClass, observed resource.Quantity) bool {
	name := ""
	if nodeClass != nil {
		name = nodeClass.Name
	}
	f.calls = append(f.calls, recordedCall{serverType: serverType, nodeClass: name, observed: observed})
	return true
}

func testNodeClass() *apiv1.HCloudNodeClass {
	return &apiv1.HCloudNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "default"},
		Status: apiv1.HCloudNodeClassStatus{
			ResolvedImages: []apiv1.ResolvedImage{{Architecture: "x86", ImageID: 42}},
		},
	}
}

func testNodePool(group, kind string) *karpv1.NodePool {
	return &karpv1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "workers"},
		Spec: karpv1.NodePoolSpec{
			Template: karpv1.NodeClaimTemplate{
				Spec: karpv1.NodeClaimTemplateSpec{
					NodeClassRef: &karpv1.NodeClassReference{Name: "default", Group: group, Kind: kind},
				},
			},
		},
	}
}

// testNode is a registered Karpenter-owned node reporting a real cx53's capacity.
func testNode() *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "worker-1",
			Labels: map[string]string{
				corev1.LabelInstanceTypeStable: "cx53",
				karpv1.NodePoolLabelKey:        "workers",
				karpv1.NodeRegisteredLabelKey:  "true",
			},
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("32089152Ki"),
			},
		},
	}
}

func reconcileNode(t *testing.T, node *corev1.Node, objs ...client.Object) *fakeRecorder {
	t.Helper()
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)

	all := append([]client.Object{node}, objs...)
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(all...).Build()

	rec := &fakeRecorder{}
	c := NewController(kube, rec)
	if _, err := c.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: node.Name},
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return rec
}

func TestReconcile_RecordsRegisteredNodeCapacity(t *testing.T) {
	rec := reconcileNode(t, testNode(), testNodePool(apiv1.Group, "HCloudNodeClass"), testNodeClass())

	if len(rec.calls) != 1 {
		t.Fatalf("expected 1 recorded observation, got %d", len(rec.calls))
	}
	got := rec.calls[0]
	if got.serverType != "cx53" {
		t.Errorf("expected server type cx53, got %q", got.serverType)
	}
	if got.nodeClass != "default" {
		t.Errorf("expected node class default, got %q", got.nodeClass)
	}
	if got.observed.String() != "32089152Ki" {
		t.Errorf("expected the node's reported capacity, got %s", got.observed.String())
	}
}

// An unregistered node has not finished joining, so its reported capacity is not
// yet trustworthy.
func TestReconcile_IgnoresUnregisteredNode(t *testing.T) {
	node := testNode()
	delete(node.Labels, karpv1.NodeRegisteredLabelKey)

	rec := reconcileNode(t, node, testNodePool(apiv1.Group, "HCloudNodeClass"), testNodeClass())
	if len(rec.calls) != 0 {
		t.Errorf("expected no observation from an unregistered node, got %d", len(rec.calls))
	}
}

// Nodes Karpenter does not own tell us nothing about a Hetzner server type as
// this provider builds it.
func TestReconcile_IgnoresNodeWithoutNodePool(t *testing.T) {
	node := testNode()
	delete(node.Labels, karpv1.NodePoolLabelKey)

	rec := reconcileNode(t, node)
	if len(rec.calls) != 0 {
		t.Errorf("expected no observation from a node with no node pool, got %d", len(rec.calls))
	}
}

// A node pool pointing at another provider's node class is not ours to measure.
//
// The HCloudNodeClass is seeded deliberately: without it the lookup would 404
// and the test would pass whether or not the group and kind are checked at all.
func TestReconcile_IgnoresForeignNodeClass(t *testing.T) {
	rec := reconcileNode(t, testNode(), testNodePool("karpenter.k8s.aws", "EC2NodeClass"), testNodeClass())
	if len(rec.calls) != 0 {
		t.Errorf("expected no observation for a foreign node class, got %d", len(rec.calls))
	}
}

func TestReconcile_IgnoresNodeWithoutInstanceType(t *testing.T) {
	node := testNode()
	delete(node.Labels, corev1.LabelInstanceTypeStable)

	rec := reconcileNode(t, node, testNodePool(apiv1.Group, "HCloudNodeClass"), testNodeClass())
	if len(rec.calls) != 0 {
		t.Errorf("expected no observation without an instance type, got %d", len(rec.calls))
	}
}

// A node reporting no memory is reporting an absence of data, not a very small
// machine; recording it would strand every pod on that type.
func TestReconcile_IgnoresZeroCapacity(t *testing.T) {
	node := testNode()
	node.Status.Capacity = corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("0")}

	rec := reconcileNode(t, node, testNodePool(apiv1.Group, "HCloudNodeClass"), testNodeClass())
	if len(rec.calls) != 0 {
		t.Errorf("expected no observation for zero capacity, got %d", len(rec.calls))
	}
}

// A missing node pool or node class is a race with deletion, not an error worth
// retrying: the node is on its way out.
func TestReconcile_ToleratesMissingNodePool(t *testing.T) {
	rec := reconcileNode(t, testNode())
	if len(rec.calls) != 0 {
		t.Errorf("expected no observation when the node pool is gone, got %d", len(rec.calls))
	}
}

// A deleted node must not error the reconcile loop.
func TestReconcile_ToleratesMissingNode(t *testing.T) {
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).Build()
	rec := &fakeRecorder{}
	c := NewController(kube, rec)

	if _, err := c.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: "gone"},
	}); err != nil {
		t.Fatalf("expected no error for a missing node, got %v", err)
	}
	if len(rec.calls) != 0 {
		t.Errorf("expected no observation, got %d", len(rec.calls))
	}
}
