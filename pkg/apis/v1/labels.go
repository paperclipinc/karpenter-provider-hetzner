package v1

const (
	Group   = "karpenter.hetzner.cloud"
	Version = "v1"

	LabelCPUType      = Group + "/cpu-type"
	LabelServerFamily = Group + "/server-family"
	LabelLocation     = Group + "/location"

	ProviderIDPrefix = "hcloud://"

	ServerLabelManagedBy = "karpenter.sh/managed-by"
	ServerLabelCluster   = "karpenter.sh/cluster"
	ServerLabelNodeClaim = "karpenter.sh/nodeclaim"
	ServerLabelNodePool  = "karpenter.sh/nodepool"
	ServerValueManagedBy = "karpenter"
)
