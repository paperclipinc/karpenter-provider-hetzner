package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes/scheme"
)

var (
	SchemeGroupVersion = schema.GroupVersion{Group: Group, Version: Version}
	SchemeBuilder      = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme        = SchemeBuilder.AddToScheme
)

func addKnownTypes(s *runtime.Scheme) error {
	s.AddKnownTypes(SchemeGroupVersion, &HCloudNodeClass{}, &HCloudNodeClassList{})
	metav1.AddToGroupVersion(s, SchemeGroupVersion)
	return nil
}

func init() {
	// Register into the client-go global scheme that the Karpenter operator's
	// manager is built from, so the HCloudNodeClass controller can watch the type
	// at runtime. (The blank import of this package in main.go triggers this.)
	utilruntime.Must(AddToScheme(scheme.Scheme))
}
