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
