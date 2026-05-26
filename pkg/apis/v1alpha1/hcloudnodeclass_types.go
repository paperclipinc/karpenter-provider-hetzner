package v1alpha1

import (
	"github.com/awslabs/operatorpkg/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:categories=karpenter,shortName=hcnc
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
}

type ImageSelector struct {
	// +kubebuilder:validation:Enum=talos;ubuntu
	Family string `json:"family"`

	// +optional
	Version string `json:"version,omitempty"`
}

type ResolvedImage struct {
	Location string `json:"location"`
	ImageID  int64  `json:"imageID"`
}

type HCloudNodeClassStatus struct {
	// +optional
	Conditions []status.Condition `json:"conditions,omitempty"`
	// +optional
	ResolvedImages []ResolvedImage `json:"resolvedImages,omitempty"`
}

var conditionTypes = status.NewReadyConditions()

func (in *HCloudNodeClass) GetConditions() []status.Condition {
	return in.Status.Conditions
}

func (in *HCloudNodeClass) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}

func (in *HCloudNodeClass) StatusConditions(opts ...status.ForOption) status.ConditionSet {
	return conditionTypes.For(in, opts...)
}

// +kubebuilder:object:root=true
type HCloudNodeClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []HCloudNodeClass `json:"items"`
}
