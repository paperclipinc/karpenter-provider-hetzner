package cloudprovider

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/paperclipinc/karpenter-provider-hetzner/pkg/apis/v1alpha1"
)

// resolveUserData returns the server userData for a node class. If BootstrapRef
// is set it reads the value from the referenced Secret (taking precedence over
// inline UserData); otherwise it returns the inline UserData.
func resolveUserData(ctx context.Context, kubeClient client.Client, nodeClass *v1alpha1.HCloudNodeClass) (string, error) {
	ref := nodeClass.Spec.BootstrapRef
	if ref == nil {
		return nodeClass.Spec.UserData, nil
	}
	key := ref.Key
	if key == "" {
		key = "userData"
	}
	secret := &corev1.Secret{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, secret); err != nil {
		return "", fmt.Errorf("getting bootstrap secret %s/%s: %w", ref.Namespace, ref.Name, err)
	}
	data, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("bootstrap secret %s/%s missing key %q", ref.Namespace, ref.Name, key)
	}
	return string(data), nil
}
