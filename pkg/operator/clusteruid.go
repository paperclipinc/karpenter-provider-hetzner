package operator

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// clusterUIDNamespace is the namespace whose UID identifies the cluster.
// kube-system is created once at cluster bootstrap and never recreated, so its
// UID is unique per cluster and stable for the cluster's lifetime. This is the
// conventional way to derive a cluster identity in Kubernetes, and needs no
// state of our own.
const clusterUIDNamespace = "kube-system"

// ClusterUID returns a stable identifier for this cluster.
//
// It exists because CLUSTER_NAME is operator-supplied and nothing enforces
// uniqueness. Two clusters sharing a name in one Hetzner project each read the
// other's servers as their own, which was harmless while the label only scoped
// listings and is not now that unclaimed servers are deleted.
//
// Pass an uncached reader when calling before the manager's cache has started.
func ClusterUID(ctx context.Context, reader client.Reader) (string, error) {
	ns := &corev1.Namespace{}
	if err := reader.Get(ctx, types.NamespacedName{Name: clusterUIDNamespace}, ns); err != nil {
		return "", fmt.Errorf("reading the %s namespace to derive the cluster UID: %w", clusterUIDNamespace, err)
	}
	uid := string(ns.UID)
	if uid == "" {
		// Refuse rather than fall back to the name alone: a silently empty UID
		// would reinstate the collision this exists to prevent.
		return "", fmt.Errorf("the %s namespace has no UID", clusterUIDNamespace)
	}
	return uid, nil
}
