package reporting

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubeSecretReader reads secrets from the Kubernetes API.
type KubeSecretReader struct {
	Client client.Client
}

func (r *KubeSecretReader) ReadSecret(ctx context.Context, namespace, name, key string) (string, error) {
	var secret corev1.Secret
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &secret); err != nil {
		return "", fmt.Errorf("getting secret %s/%s: %w", namespace, name, err)
	}
	data, ok := secret.Data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, namespace, name)
	}
	return string(data), nil
}
