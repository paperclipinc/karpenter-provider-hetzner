package v1alpha1

import (
	"github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Status condition types for HCloudNodeClass.
const (
	ConditionTypeImagesReady    = "ImagesReady"
	ConditionTypeNetworkReady   = "NetworkReady"
	ConditionTypeResourcesReady = "ResourcesReady"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=karpenter,shortName=hcnc,scope=Cluster
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
type HCloudNodeClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              HCloudNodeClassSpec   `json:"spec,omitempty"`
	Status            HCloudNodeClassStatus `json:"status,omitempty"`
}

type HCloudNodeClassSpec struct {
	// +kubebuilder:validation:MinItems=1
	Locations []string `json:"locations"`

	ImageSelector ImageSelector `json:"imageSelector"`

	// +kubebuilder:validation:Minimum=1
	NetworkID int64 `json:"networkID"`

	// +optional
	FirewallIDs []int64 `json:"firewallIDs,omitempty"`

	// +optional
	SSHKeyIDs []int64 `json:"sshKeyIDs,omitempty"`

	// +kubebuilder:default=spread
	// +kubebuilder:validation:Enum=spread;none
	// +optional
	PlacementGroupStrategy string `json:"placementGroupStrategy,omitempty"`

	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// +optional
	UserData string `json:"userData,omitempty"`

	// EnablePublicIPv4 controls whether created servers get a public IPv4.
	// Defaults to true (Hetzner's default). Set false on private-network
	// clusters to avoid the primary-IPv4 charge.
	// +kubebuilder:default=true
	// +optional
	EnablePublicIPv4 *bool `json:"enablePublicIPv4,omitempty"`

	// EnablePublicIPv6 controls whether created servers get a public IPv6.
	// Defaults to true. Set false to drop the public IPv6 as well.
	// +kubebuilder:default=true
	// +optional
	EnablePublicIPv6 *bool `json:"enablePublicIPv6,omitempty"`
}

type ImageSelector struct {
	// +kubebuilder:validation:Enum=talos;ubuntu
	Family string `json:"family"`

	// +optional
	Version string `json:"version,omitempty"`
}

type ResolvedImage struct {
	Architecture string `json:"architecture"`
	ImageID      int64  `json:"imageID"`
}

type HCloudNodeClassStatus struct {
	// +optional
	Conditions []status.Condition `json:"conditions,omitempty"`
	// +optional
	ResolvedImages []ResolvedImage `json:"resolvedImages,omitempty"`
}

var conditionTypes = status.NewReadyConditions(ConditionTypeImagesReady, ConditionTypeNetworkReady, ConditionTypeResourcesReady)

func (in *HCloudNodeClass) GetConditions() []status.Condition {
	return in.Status.Conditions
}

func (in *HCloudNodeClass) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}

func (in *HCloudNodeClass) StatusConditions() status.ConditionSet {
	return conditionTypes.For(in)
}

// PublicIPv4Enabled reports whether public IPv4 should be enabled (default true).
func (s HCloudNodeClassSpec) PublicIPv4Enabled() bool {
	return s.EnablePublicIPv4 == nil || *s.EnablePublicIPv4
}

// PublicIPv6Enabled reports whether public IPv6 should be enabled (default true).
func (s HCloudNodeClassSpec) PublicIPv6Enabled() bool {
	return s.EnablePublicIPv6 == nil || *s.EnablePublicIPv6
}

// +kubebuilder:object:root=true
type HCloudNodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HCloudNodeClass `json:"items"`
}
