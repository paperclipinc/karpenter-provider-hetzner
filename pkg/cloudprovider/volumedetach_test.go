package cloudprovider_test

import (
	"context"
	"testing"
	"time"

	"github.com/hetznercloud/hcloud-go/v2/hcloud"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"

	apiv1 "github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/cloudprovider"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/imagefamily"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instance"
	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/providers/instancetype"
)

// buildDeleteCP wires a CloudProvider around one server and returns the fake so
// the test can see whether the server was actually destroyed.
func buildDeleteCP(t *testing.T, server *hcloud.Server) (*cloudprovider.CloudProvider, *fakeServerClient) {
	t.Helper()
	_ = apiv1.SchemeBuilder.AddToScheme(scheme.Scheme)
	nc := baselineNodeClass()
	kube := fake.NewClientBuilder().WithScheme(scheme.Scheme).WithObjects(nc).Build()
	fsc := &fakeServerClient{servers: map[int64]*hcloud.Server{server.ID: server}}
	cp := cloudprovider.NewCloudProvider(kube,
		instance.NewProvider(fsc, "test-cluster"),
		instancetype.NewProvider(&fakeServerTypeClient{}),
		imagefamily.NewProvider(&fakeImageClient{}))
	return cp, fsc
}

// terminatingClaim is a NodeClaim that core is driving through termination:
// deleted `age` ago, pointing at the given server.
func terminatingClaim(serverID int64, age time.Duration) *karpv1.NodeClaim {
	deleted := metav1.NewTime(time.Now().Add(-age))
	return &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim", DeletionTimestamp: &deleted},
		Status:     karpv1.NodeClaimStatus{ProviderID: instance.FormatProviderID(serverID)},
	}
}

func serverWithVolumes(id int64, volumes ...int64) *hcloud.Server {
	s := &hcloud.Server{ID: id, ServerType: &hcloud.ServerType{Name: "cx22"}}
	for _, v := range volumes {
		s.Volumes = append(s.Volumes, &hcloud.Volume{ID: v})
	}
	return s
}

// TestDelete_WaitsWhileVolumeStillAttached is the regression for the unclean
// detach: destroying a server while hcloud still has a volume attached tears a
// live filesystem away mid-write.
//
// Karpenter core already waits for the Kubernetes VolumeAttachment to be
// removed, but that object disappearing does not prove hcloud has finished
// detaching. Returning nil here means "still terminating" in core's contract, so
// core requeues in 5s rather than treating the node as gone.
func TestDelete_WaitsWhileVolumeStillAttached(t *testing.T) {
	server := serverWithVolumes(50, 106145787)
	cp, fsc := buildDeleteCP(t, server)

	if err := cp.Delete(context.Background(), terminatingClaim(50, 30*time.Second)); err != nil {
		t.Fatalf("expected no error while awaiting detachment, got %v", err)
	}
	if _, ok := fsc.servers[50]; !ok {
		t.Error("server was destroyed while a volume was still attached")
	}
}

// A server with nothing attached must not be delayed at all.
func TestDelete_ProceedsWhenNoVolumesAttached(t *testing.T) {
	cp, fsc := buildDeleteCP(t, serverWithVolumes(50))

	if err := cp.Delete(context.Background(), terminatingClaim(50, time.Second)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := fsc.servers[50]; ok {
		t.Error("server with no attached volumes should have been deleted immediately")
	}
}

// The wait has to be bounded. A detach that never completes would otherwise
// leave a node that can never terminate; past the bound we delete anyway, since
// destroying the server force-detaches. That degrades to today's behaviour, not
// worse than it.
func TestDelete_ProceedsOnceGraceElapsed(t *testing.T) {
	cp, fsc := buildDeleteCP(t, serverWithVolumes(50, 106145787))

	if err := cp.Delete(context.Background(), terminatingClaim(50, time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := fsc.servers[50]; ok {
		t.Error("server should have been deleted once the detachment grace elapsed")
	}
}

// Without a DeletionTimestamp there is no clock to bound the wait against, so
// waiting could never end. Fail open: behave exactly as before this change.
func TestDelete_ProceedsWhenClaimHasNoDeletionTimestamp(t *testing.T) {
	cp, fsc := buildDeleteCP(t, serverWithVolumes(50, 106145787))

	claim := &karpv1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "claim"},
		Status:     karpv1.NodeClaimStatus{ProviderID: instance.FormatProviderID(50)},
	}
	if err := cp.Delete(context.Background(), claim); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := fsc.servers[50]; ok {
		t.Error("server should have been deleted when the claim carries no deletion timestamp")
	}
}

// An already-gone server must still report NodeClaimNotFound, which is how core
// knows termination finished. The volume check must not swallow that.
func TestDelete_MissingServerStillReportsNotFound(t *testing.T) {
	cp, _ := buildDeleteCP(t, serverWithVolumes(50))

	err := cp.Delete(context.Background(), terminatingClaim(999, time.Second))
	if err == nil {
		t.Fatal("expected a NodeClaimNotFound error for a server that no longer exists")
	}
}
