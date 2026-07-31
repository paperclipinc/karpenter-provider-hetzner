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
)
