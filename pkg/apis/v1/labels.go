package v1

const (
	Group   = "karpenter.hetzner.cloud"
	Version = "v1"

	LabelCPUType      = Group + "/cpu-type"
	LabelServerFamily = Group + "/server-family"
	LabelLocation     = Group + "/location"

	// LabelCSILocation is the topology domain the hcloud CSI driver uses for
	// PersistentVolume nodeAffinity and CSINode topology (see
	// hetznercloud/csi-driver#333). It is aliased to topology.kubernetes.io/zone
	// in normalizedlabels.go so Karpenter can volume-schedule PVC pods.
	LabelCSILocation = "csi.hetzner.cloud/location"

	ProviderIDPrefix = "hcloud://"

	ServerLabelManagedBy = "karpenter.sh/managed-by"
	ServerLabelCluster   = "karpenter.sh/cluster"
	ServerLabelNodeClaim = "karpenter.sh/nodeclaim"
	ServerLabelNodePool  = "karpenter.sh/nodepool"
	ServerValueManagedBy = "karpenter"

	// ServerLabelClusterUID carries the UID of this cluster's kube-system
	// namespace, which is unique per cluster and stable for its lifetime.
	//
	// ServerLabelCluster alone is not a safe ownership test: CLUSTER_NAME is
	// operator-supplied and nothing enforces uniqueness, so two clusters sharing
	// a name in one Hetzner project each see the other's servers as their own.
	// That was harmless when the label only scoped listings; it is not now that
	// unclaimed servers are deleted. A server whose UID is present and different
	// belongs to someone else. A missing UID means the server predates this
	// label, and is treated as ours so existing fleets stay managed.
	ServerLabelClusterUID = "karpenter.sh/cluster-uid"
)

// OwnedByCluster reports whether a server's labels mark it as belonging to the
// installation identified by clusterName and clusterUID.
//
// This is the single definition of ownership. It is consulted from two places
// that both act destructively on the answer -- the orphan sweep deletes, and
// adoption hands a live machine to Karpenter, which eventually terminates it --
// so the rule they apply has to be one rule. The legacy exemption below is the
// part that must not drift: it is a migration affordance that will be tightened
// once fleets have rolled, and tightening it in one caller but not the other
// would either strand every pre-UID orphan or resume cross-cluster deletion.
//
// A UID that is present and different belongs to another cluster. A missing UID
// predates the label and is treated as ours, because refusing those would strand
// every server created before it existed.
func OwnedByCluster(labels map[string]string, clusterName, clusterUID string) bool {
	if labels[ServerLabelManagedBy] != ServerValueManagedBy {
		return false
	}
	if labels[ServerLabelCluster] != clusterName {
		return false
	}
	uid := labels[ServerLabelClusterUID]
	return uid == "" || uid == clusterUID
}
