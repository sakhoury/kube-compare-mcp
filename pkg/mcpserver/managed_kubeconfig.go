// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

// Kubeconfig-related GVR constants used by secret reading and managed cluster
// kubeconfig extraction. These stay in mcpserver as generic infrastructure.
var (
	secretGVR = schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}

	clusterDeploymentGVR = schema.GroupVersionResource{
		Group:    "hive.openshift.io",
		Version:  "v1",
		Resource: "clusterdeployments",
	}
)

// ExtractManagedClusterKubeconfig reads the admin kubeconfig for a managed cluster
// from the hub. It first looks up the Hive ClusterDeployment CR to find the
// adminKubeconfigSecretRef, then reads the referenced Secret.
//
// If the ClusterDeployment is not found, it falls back to reading a well-known
// secret name (<clusterName>-admin-kubeconfig) directly.
//
// Returns the rest.Config, a human-readable source description, and any error.
func ExtractManagedClusterKubeconfig(ctx context.Context, inspector acm.ResourceInspector, clusterName string) (*rest.Config, string, error) {
	logger := slog.Default()

	// Strategy 1: Read ClusterDeployment to find the secret ref.
	secretName, source, err := kubeconfigSecretFromClusterDeployment(ctx, logger, inspector, clusterName)
	if err == nil && secretName != "" {
		config, err := ReadKubeconfigSecret(ctx, inspector, secretName, clusterName)
		if err != nil {
			return nil, "", fmt.Errorf("found ClusterDeployment but failed to read secret %s/%s: %w", clusterName, secretName, err)
		}
		return config, source, nil
	}
	logger.Debug("ClusterDeployment lookup failed, trying well-known secret name",
		"cluster", clusterName,
		"error", err,
	)

	// Strategy 2: Fallback to well-known secret name.
	fallbackName := clusterName + "-admin-kubeconfig"
	config, err := ReadKubeconfigSecret(ctx, inspector, fallbackName, clusterName)
	if err != nil {
		return nil, "", fmt.Errorf("kubeconfig secret not found for cluster %s (tried ClusterDeployment and fallback secret %s/%s): %w",
			clusterName, clusterName, fallbackName, err)
	}

	source = fmt.Sprintf("Secret %s/%s (fallback)", clusterName, fallbackName)
	logger.Info("Extracted managed cluster kubeconfig via fallback secret",
		"cluster", clusterName,
		"secret", fallbackName,
	)
	return config, source, nil
}

// kubeconfigSecretFromClusterDeployment reads the ClusterDeployment for the given
// cluster and extracts the adminKubeconfigSecretRef name.
func kubeconfigSecretFromClusterDeployment(ctx context.Context, logger *slog.Logger, inspector acm.ResourceInspector, clusterName string) (string, string, error) {
	cd, err := inspector.GetResource(ctx, clusterDeploymentGVR, clusterName, clusterName)
	if err != nil {
		return "", "", fmt.Errorf("clusterDeployment %s/%s not found: %w", clusterName, clusterName, err)
	}

	secretName, found, err := unstructured.NestedString(cd.Object,
		"spec", "clusterMetadata", "adminKubeconfigSecretRef", "name")
	if err != nil || !found || secretName == "" {
		return "", "", fmt.Errorf("clusterDeployment %s/%s missing spec.clusterMetadata.adminKubeconfigSecretRef.name", clusterName, clusterName)
	}

	source := fmt.Sprintf("ClusterDeployment %s/%s", clusterName, secretName)
	logger.Info("Found kubeconfig secret ref in ClusterDeployment",
		"cluster", clusterName,
		"secretRef", secretName,
	)
	return secretName, source, nil
}

// ReadKubeconfigSecret reads a Secret and parses the kubeconfig data into a rest.Config.
func ReadKubeconfigSecret(ctx context.Context, inspector acm.ResourceInspector, secretName, namespace string) (*rest.Config, error) {
	secret, err := inspector.GetResource(ctx, secretGVR, secretName, namespace)
	if err != nil {
		return nil, fmt.Errorf("secret %s/%s not found: %w", namespace, secretName, err)
	}

	// Secret data values are stored as base64-encoded strings in the unstructured object.
	dataRaw, found, err := unstructured.NestedMap(secret.Object, "data")
	if err != nil || !found {
		return nil, fmt.Errorf("secret %s/%s has no data field", namespace, secretName)
	}

	kubeconfigB64, ok := dataRaw["kubeconfig"].(string)
	if !ok || kubeconfigB64 == "" {
		return nil, fmt.Errorf("secret %s/%s missing 'kubeconfig' key in data", namespace, secretName)
	}

	// Decode the base64-encoded kubeconfig.
	kubeconfigBytes, err := base64.StdEncoding.DecodeString(kubeconfigB64)
	if err != nil {
		return nil, fmt.Errorf("failed to base64-decode kubeconfig from secret %s/%s: %w", namespace, secretName, err)
	}

	// Parse the kubeconfig into a rest.Config.
	config, err := clientcmd.RESTConfigFromKubeConfig(kubeconfigBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig from secret %s/%s: %w", namespace, secretName, err)
	}

	return config, nil
}
