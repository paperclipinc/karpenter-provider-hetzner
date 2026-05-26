package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: Group, Version: Version}
	SchemeBuilder      = &scheme.Builder{GroupVersion: SchemeGroupVersion}
)

func init() {
	SchemeBuilder.Register(&HCloudNodeClass{}, &HCloudNodeClassList{})
}
