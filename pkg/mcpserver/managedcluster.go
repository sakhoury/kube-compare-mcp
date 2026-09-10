// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// adminKubeconfigSecretSuffix is appended to a managed cluster name to build the
// name of the secret containing that cluster's admin kubeconfig on the hub.
const adminKubeconfigSecretSuffix = "-admin-kubeconfig" // #nosec G101 -- resource name suffix, not a credential

// adminKubeconfigSecretKey is the data key inside the admin kubeconfig secret that
// holds the kubeconfig content.
const adminKubeconfigSecretKey = "kubeconfig"

// GVRs for ACM (Advanced Cluster Management) hub resources.
var (
	managedClusterGVR = schema.GroupVersionResource{
		Group:    "cluster.open-cluster-management.io",
		Version:  "v1",
		Resource: "managedclusters",
	}

	managedClusterSecretGVR = schema.GroupVersionResource{
		Group:    "",
		Version:  "v1",
		Resource: "secrets",
	}
)

// newHubDynamicClient builds a dynamic client for the ACM hub cluster.
// Production uses in-cluster config because the MCP server is expected to run on the
// hub. It is a package-level variable so tests can override it with a fake client.
var newHubDynamicClient = func() (dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, NewCompareError("hub-config",
			fmt.Errorf("failed to get in-cluster config: %w", err),
			"The 'managed_cluster' parameter requires the MCP server to be running inside the ACM hub cluster.")
	}
	return dynamic.NewForConfig(cfg)
}

// BuildRestConfigForManagedCluster assembles a rest.Config for an ACM managed (spoke)
// cluster by reading the hub's ManagedCluster resource and the spoke's admin kubeconfig
// secret. The API server URL and CA bundle come from the ManagedCluster resource; the
// client credentials come from the <name>-admin-kubeconfig secret.
func BuildRestConfigForManagedCluster(ctx context.Context, clusterName string) (*rest.Config, error) {
	logger := slog.Default()

	clusterName = strings.TrimSpace(clusterName)
	if clusterName == "" {
		return nil, NewValidationError("managed_cluster",
			"managed cluster name is empty",
			"Provide the name of an ACM managed cluster")
	}

	hub, err := newHubDynamicClient()
	if err != nil {
		return nil, err
	}

	// Read the cluster-scoped ManagedCluster resource for the API URL and CA bundle.
	mc, err := hub.Resource(managedClusterGVR).Get(ctx, clusterName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, NewValidationError("managed_cluster",
				fmt.Sprintf("ManagedCluster %q not found on the hub cluster", clusterName),
				"Verify the managed cluster name and that it is registered in ACM on this hub")
		}
		return nil, NewCompareError("managed-cluster",
			fmt.Errorf("failed to get ManagedCluster %q: %w", clusterName, err),
			"Verify the MCP server has permission to read ManagedCluster resources on the hub")
	}

	serverURL, caBundle, err := extractManagedClusterEndpoint(mc, clusterName)
	if err != nil {
		return nil, err
	}

	// Read the spoke's admin kubeconfig secret for the client credentials.
	secretName := clusterName + adminKubeconfigSecretSuffix
	sec, err := hub.Resource(managedClusterSecretGVR).Namespace(clusterName).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, NewValidationError("managed_cluster",
				fmt.Sprintf("secret %q not found in namespace %q on the hub cluster", secretName, clusterName),
				"Verify the managed cluster's admin kubeconfig secret exists on the hub")
		}
		return nil, NewCompareError("managed-cluster",
			fmt.Errorf("failed to get secret %q in namespace %q: %w", secretName, clusterName, err),
			"Verify the MCP server has permission to read secrets on the hub")
	}

	kubeconfigData, err := extractSecretKubeconfig(sec, secretName)
	if err != nil {
		return nil, err
	}

	// Reuse the shared secure builder: parse + security validation (blocks exec and
	// auth-provider plugins) + context resolution (empty uses current-context).
	restConfig, err := BuildSecureRestConfigFromBytes(kubeconfigData, "")
	if err != nil {
		return nil, err
	}

	// Override the endpoint and CA with the values from the ManagedCluster resource.
	restConfig.Host = serverURL
	if len(caBundle) > 0 {
		restConfig.CAData = caBundle
		restConfig.CAFile = ""
		restConfig.Insecure = false
	}

	logger.Info("Configured connection for ACM managed cluster",
		"managedCluster", clusterName,
		"host", restConfig.Host,
	)

	return restConfig, nil
}

// extractManagedClusterEndpoint returns the API server URL and decoded CA bundle from
// the first usable managedClusterClientConfigs entry (one with a non-empty URL).
func extractManagedClusterEndpoint(mc *unstructured.Unstructured, clusterName string) (serverURL string, caBundle []byte, err error) {
	configs, found, nErr := unstructured.NestedSlice(mc.Object, "spec", "managedClusterClientConfigs")
	if nErr != nil || !found || len(configs) == 0 {
		return "", nil, NewValidationError("managed_cluster",
			fmt.Sprintf("ManagedCluster %q has no managedClusterClientConfigs", clusterName),
			"The managed cluster does not expose an API URL; verify it is fully imported into ACM")
	}

	for _, entry := range configs {
		cfg, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		url, _, _ := unstructured.NestedString(cfg, "url")
		if url == "" {
			continue
		}

		caB64, _, _ := unstructured.NestedString(cfg, "caBundle")
		if caB64 != "" {
			decoded, decErr := base64.StdEncoding.DecodeString(caB64)
			if decErr != nil {
				return "", nil, NewValidationError("managed_cluster",
					fmt.Sprintf("ManagedCluster %q has an invalid caBundle", clusterName),
					"The caBundle in the ManagedCluster resource is not valid base64")
			}
			caBundle = decoded
		}
		return url, caBundle, nil
	}

	return "", nil, NewValidationError("managed_cluster",
		fmt.Sprintf("ManagedCluster %q has no usable API URL in managedClusterClientConfigs", clusterName),
		"The managed cluster does not expose an API URL; verify it is fully imported into ACM")
}

// extractSecretKubeconfig returns the raw kubeconfig bytes stored under the kubeconfig
// key of an admin kubeconfig secret read via the dynamic client (values are base64).
func extractSecretKubeconfig(sec *unstructured.Unstructured, secretName string) ([]byte, error) {
	kubeB64, found, err := unstructured.NestedString(sec.Object, "data", adminKubeconfigSecretKey)
	if err != nil || !found || kubeB64 == "" {
		return nil, NewValidationError("managed_cluster",
			fmt.Sprintf("secret %q has no %q key", secretName, adminKubeconfigSecretKey),
			"The admin kubeconfig secret does not contain a kubeconfig")
	}

	decoded, err := base64.StdEncoding.DecodeString(kubeB64)
	if err != nil {
		decoded, err = base64.URLEncoding.DecodeString(kubeB64)
		if err != nil {
			return nil, NewValidationError("managed_cluster",
				fmt.Sprintf("secret %q has an invalid %q value", secretName, adminKubeconfigSecretKey),
				"The kubeconfig stored in the admin kubeconfig secret is not valid base64")
		}
	}

	return decoded, nil
}
