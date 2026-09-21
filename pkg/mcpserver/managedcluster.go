// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// managedServiceAccountName is the name of the ManagedServiceAccount CR that must
// exist in each spoke namespace on the hub to supply the bearer token used to
// authenticate to the spoke's API server.
const managedServiceAccountName = "kube-compare-mcp" // #nosec G101 -- resource name, not a credential

// managedProxyConfigurationName is the well-known name of the ManagedProxyConfiguration
// CR created by the cluster-proxy addon.
const managedProxyConfigurationName = "cluster-proxy"

// proxyUserRouteName is the name of the OpenShift Route that exposes the cluster-proxy
// user-facing endpoint on the hub cluster.
const proxyUserRouteName = "cluster-proxy-addon-user"

// defaultProxyNamespace is the default namespace for the cluster-proxy addon resources.
const defaultProxyNamespace = "open-cluster-management-addon"

// ingressOperatorNamespace is the namespace where the OpenShift ingress operator
// stores the ingress CA certificate.
const ingressOperatorNamespace = "openshift-ingress-operator"

// ingressCASecretName is the name of the secret in ingressOperatorNamespace that
// contains the hub cluster's ingress CA certificate under the "tls.crt" key.
const ingressCASecretName = "router-ca"

// k8sNameRegexp is the compiled form of k8sNamePattern, used to validate cluster
// names inside BuildRestConfigForManagedCluster independent of the MCP schema layer.
var k8sNameRegexp = regexp.MustCompile(k8sNamePattern)

// GVRs for ACM (Advanced Cluster Management) hub resources.
var (
	managedClusterGVR = schema.GroupVersionResource{
		Group:    "cluster.open-cluster-management.io",
		Version:  "v1",
		Resource: "managedclusters",
	}

	managedProxyConfigurationGVR = schema.GroupVersionResource{
		Group:    "proxy.open-cluster-management.io",
		Version:  "v1alpha1",
		Resource: "managedproxyconfigurations",
	}

	routeGVR = schema.GroupVersionResource{
		Group:    "route.openshift.io",
		Version:  "v1",
		Resource: "routes",
	}

	secretGVR = schema.GroupVersionResource{
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
// cluster using the cluster-proxy addon's user-facing Route for connectivity and a
// ManagedServiceAccount token for authentication.
//
// The spoke is accessed through:
//
//	https://<cluster-proxy-addon-user route host>/<cluster-name>
//
// The bearer token comes from the ManagedServiceAccount token secret in the spoke's
// namespace on the hub. The ManagedCluster resource is read only to verify the cluster
// is fully registered in ACM.
func BuildRestConfigForManagedCluster(ctx context.Context, clusterName string) (*rest.Config, error) {
	logger := slog.Default()

	clusterName = strings.TrimSpace(clusterName)
	if clusterName == "" {
		return nil, NewValidationError("managed_cluster",
			"managed cluster name is empty",
			"Provide the name of an ACM managed cluster")
	}
	if !k8sNameRegexp.MatchString(clusterName) {
		return nil, NewValidationError("managed_cluster",
			fmt.Sprintf("managed cluster name %q is not a valid Kubernetes resource name", clusterName),
			"Cluster names must be valid RFC 1123 DNS subdomains: lowercase letters, digits, hyphens, and dots only")
	}

	hub, err := newHubDynamicClient()
	if err != nil {
		return nil, err
	}

	// Verify the cluster-scoped ManagedCluster resource exists and is fully registered.
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

	if _, _, err = extractManagedClusterEndpoint(mc, clusterName); err != nil {
		return nil, err
	}

	// Read the ManagedServiceAccount token secret for spoke credentials.
	msaToken, err := getMSABearerToken(ctx, hub, clusterName)
	if err != nil {
		return nil, err
	}

	// Determine the namespace where the cluster-proxy addon is deployed.
	proxyNamespace, err := getProxyAddonNamespace(ctx, hub)
	if err != nil {
		return nil, err
	}

	// Read the cluster-proxy user Route to build the spoke API server URL.
	// The spoke is accessed as: https://<route-host>/<cluster-name>
	proxyHost, err := getProxyUserHost(ctx, hub, proxyNamespace)
	if err != nil {
		return nil, err
	}

	// Read the hub cluster's ingress CA to verify the proxy Route's TLS certificate.
	// The Route is served by the OpenShift ingress router whose certificate is signed by
	// the ingress operator CA, not by a publicly trusted authority.
	ingressCA, err := getIngressCA(ctx, hub)
	if err != nil {
		return nil, err
	}

	spokeURL := "https://" + proxyHost + "/" + clusterName

	restConfig := &rest.Config{
		Host:        spokeURL,
		BearerToken: msaToken,
		TLSClientConfig: rest.TLSClientConfig{
			CAData: ingressCA,
		},
	}

	logger.Info("Configured connection for ACM managed cluster via cluster-proxy",
		"managedCluster", clusterName,
		"host", restConfig.Host,
	)

	return restConfig, nil
}

// getMSABearerToken reads the bearer token from the ManagedServiceAccount token secret
// for the given spoke cluster. The secret is expected to be in the spoke's namespace on
// the hub, with the name matching managedServiceAccountName.
func getMSABearerToken(ctx context.Context, hub dynamic.Interface, clusterName string) (string, error) {
	sec, err := hub.Resource(secretGVR).Namespace(clusterName).Get(ctx, managedServiceAccountName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", NewValidationError("managed_cluster",
				fmt.Sprintf("ManagedServiceAccount token secret %q not found in namespace %q on the hub", managedServiceAccountName, clusterName),
				fmt.Sprintf("Verify the managed-serviceaccount addon is enabled and a ManagedServiceAccount named %q exists in the %q namespace on the hub", managedServiceAccountName, clusterName))
		}
		return "", NewCompareError("managed-cluster",
			fmt.Errorf("failed to get ManagedServiceAccount token secret %q in namespace %q: %w", managedServiceAccountName, clusterName, err),
			"Verify the MCP server has permission to read secrets in spoke namespaces on the hub")
	}

	tokenB64, found, err := unstructured.NestedString(sec.Object, "data", "token")
	if err != nil || !found || tokenB64 == "" {
		return "", NewValidationError("managed_cluster",
			fmt.Sprintf("ManagedServiceAccount token secret %q in namespace %q has no token key", managedServiceAccountName, clusterName),
			"The ManagedServiceAccount token has not been provisioned yet; check the managed-serviceaccount addon status")
	}

	token, decErr := base64.StdEncoding.DecodeString(tokenB64)
	if decErr != nil {
		return "", NewValidationError("managed_cluster",
			fmt.Sprintf("ManagedServiceAccount token secret %q in namespace %q has an invalid token", managedServiceAccountName, clusterName),
			"The token stored in the ManagedServiceAccount secret is not valid base64")
	}

	return string(token), nil
}

// getProxyAddonNamespace reads the ManagedProxyConfiguration CR and returns the
// namespace where the cluster-proxy addon is deployed.
func getProxyAddonNamespace(ctx context.Context, hub dynamic.Interface) (string, error) {
	mpc, err := hub.Resource(managedProxyConfigurationGVR).Get(ctx, managedProxyConfigurationName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", NewValidationError("managed_cluster",
				fmt.Sprintf("ManagedProxyConfiguration %q not found on the hub", managedProxyConfigurationName),
				"Verify the cluster-proxy addon is enabled on the hub cluster")
		}
		return "", NewCompareError("cluster-proxy",
			fmt.Errorf("failed to get ManagedProxyConfiguration %q: %w", managedProxyConfigurationName, err),
			"Verify the MCP server has permission to read ManagedProxyConfiguration resources on the hub")
	}

	namespace, _, _ := unstructured.NestedString(mpc.Object, "spec", "proxyServer", "namespace")
	if namespace == "" {
		namespace = defaultProxyNamespace
	}
	return namespace, nil
}

// getProxyUserHost reads the cluster-proxy-addon-user Route and returns its external
// hostname. The spoke API server URL is constructed as https://<host>/<cluster-name>.
func getProxyUserHost(ctx context.Context, hub dynamic.Interface, namespace string) (string, error) {
	route, err := hub.Resource(routeGVR).Namespace(namespace).Get(ctx, proxyUserRouteName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", NewValidationError("managed_cluster",
				fmt.Sprintf("Route %q not found in namespace %q", proxyUserRouteName, namespace),
				"Verify the cluster-proxy addon is enabled and the cluster-proxy-addon-user Route exists on the hub")
		}
		return "", NewCompareError("cluster-proxy",
			fmt.Errorf("failed to get Route %q in namespace %q: %w", proxyUserRouteName, namespace, err),
			"Verify the MCP server has permission to read Routes in the cluster-proxy addon namespace")
	}

	host, _, _ := unstructured.NestedString(route.Object, "spec", "host")
	if host == "" {
		return "", NewValidationError("managed_cluster",
			fmt.Sprintf("Route %q in namespace %q has no spec.host", proxyUserRouteName, namespace),
			"The cluster-proxy Route has not been assigned a hostname; check the ingress controller status")
	}
	return host, nil
}

// getIngressCA reads the hub cluster's ingress CA certificate from the
// openshift-ingress-operator/router-ca secret. The ingress CA is required to verify
// the TLS certificate of the cluster-proxy-addon-user Route, which is signed by the
// OpenShift ingress operator CA rather than a publicly trusted authority.
func getIngressCA(ctx context.Context, hub dynamic.Interface) ([]byte, error) {
	sec, err := hub.Resource(secretGVR).Namespace(ingressOperatorNamespace).Get(ctx, ingressCASecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, NewValidationError("managed_cluster",
				fmt.Sprintf("ingress CA secret %q not found in namespace %q", ingressCASecretName, ingressOperatorNamespace),
				"Verify the MCP server is running on an OpenShift hub cluster and the ingress operator is healthy")
		}
		return nil, NewCompareError("cluster-proxy",
			fmt.Errorf("failed to get ingress CA secret %q: %w", ingressCASecretName, err),
			"Verify the MCP server has permission to read secrets in the openshift-ingress-operator namespace")
	}

	caB64, _, _ := unstructured.NestedString(sec.Object, "data", "tls.crt")
	if caB64 == "" {
		return nil, NewValidationError("managed_cluster",
			fmt.Sprintf("ingress CA secret %q has no tls.crt key", ingressCASecretName),
			"The OpenShift ingress CA secret is missing the CA certificate")
	}

	caData, decErr := base64.StdEncoding.DecodeString(caB64)
	if decErr != nil {
		return nil, NewValidationError("managed_cluster",
			fmt.Sprintf("ingress CA secret %q has an invalid tls.crt value", ingressCASecretName),
			"The OpenShift ingress CA certificate is not valid base64")
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caData) {
		return nil, NewValidationError("managed_cluster",
			fmt.Sprintf("ingress CA secret %q contains an invalid CA certificate", ingressCASecretName),
			"The OpenShift ingress CA certificate is not a valid PEM certificate")
	}

	// Return the raw PEM bytes; rest.Config.TLSClientConfig.CAData expects PEM.
	return caData, nil
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
		if caB64 == "" {
			return "", nil, NewValidationError("managed_cluster",
				fmt.Sprintf("ManagedCluster %q has no caBundle in managedClusterClientConfigs", clusterName),
				"The managed cluster does not expose a CA bundle; verify it is fully imported into ACM")
		}
		decoded, decErr := base64.StdEncoding.DecodeString(caB64)
		if decErr != nil {
			return "", nil, NewValidationError("managed_cluster",
				fmt.Sprintf("ManagedCluster %q has an invalid caBundle", clusterName),
				"The caBundle in the ManagedCluster resource is not valid base64")
		}
		return url, decoded, nil
	}

	return "", nil, NewValidationError("managed_cluster",
		fmt.Sprintf("ManagedCluster %q has no usable API URL in managedClusterClientConfigs", clusterName),
		"The managed cluster does not expose an API URL; verify it is fully imported into ACM")
}
