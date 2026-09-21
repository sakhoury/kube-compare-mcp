// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
)

const testSpokeURL = "https://spoke1.example.com:6443"
const testMSAToken = "fake-msa-bearer-token"
const testProxyRouteHost = "cluster-proxy-addon-user.apps.hub.example.com"

var testCABytes = []byte("FAKE-CA-DATA")

// testIngressCABytes is a real self-signed EC CA certificate used to represent the
// hub cluster's ingress CA in tests. Must be a valid PEM certificate.
var testIngressCABytes = []byte(`-----BEGIN CERTIFICATE-----
MIIBhzCCAS2gAwIBAgIUcjuQCoUpcMBS/FfG8kqoSVqdN1gwCgYIKoZIzj0EAwIw
GDEWMBQGA1UEAwwNZmFrZS1wcm94eS1jYTAgFw0yNjA5MjEwOTQyNTdaGA8yMTI2
MDgyODA5NDI1N1owGDEWMBQGA1UEAwwNZmFrZS1wcm94eS1jYTBZMBMGByqGSM49
AgEGCCqGSM49AwEHA0IABMPovQburol+AE3DfpW89cYzzlAdSN4X8S3FZ/q594rp
GVQ3J4tZsD94eJvfv126Lr8s6Bac43/IlFXDaC6gHTGjUzBRMB0GA1UdDgQWBBTu
1A9WpXXWavwRTSn4wCcCeX6w1DAfBgNVHSMEGDAWgBTu1A9WpXXWavwRTSn4wCcC
eX6w1DAPBgNVHRMBAf8EBTADAQH/MAoGCCqGSM49BAMCA0gAMEUCIG6yrnof8/lr
0nE30Xl/u96YoEkNfmMyC5eNrKb+WbtpAiEAmn+QL1oCmFfy8YJB6CRTKT2R8bXr
AVbe7TZEVQdHolo=
-----END CERTIFICATE-----
`)

var managedClusterGVRToListKind = map[schema.GroupVersionResource]string{
	{Group: "cluster.open-cluster-management.io", Version: "v1", Resource: "managedclusters"}:               "ManagedClusterList",
	{Group: "", Version: "v1", Resource: "secrets"}:                                                           "SecretList",
	{Group: "proxy.open-cluster-management.io", Version: "v1alpha1", Resource: "managedproxyconfigurations"}: "ManagedProxyConfigurationList",
	{Group: "route.openshift.io", Version: "v1", Resource: "routes"}:                                         "RouteList",
}

// newFakeIngressCASecret creates the openshift-ingress-operator/router-ca secret used
// to verify the TLS certificate of the cluster-proxy-addon-user Route.
func newFakeIngressCASecret() *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      ingressCASecretName,
				"namespace": ingressOperatorNamespace,
			},
			"data": map[string]any{
				"tls.crt": base64.StdEncoding.EncodeToString(testIngressCABytes),
			},
		},
	}
}

func newHubTestFakeDynamicClient(objects ...runtime.Object) dynamic.Interface {
	scheme := runtime.NewScheme()
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, managedClusterGVRToListKind, objects...)
}

func newFakeManagedCluster(name, url, caBundleB64 string) *unstructured.Unstructured {
	clientConfig := map[string]any{"url": url}
	if caBundleB64 != "" {
		clientConfig["caBundle"] = caBundleB64
	}
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "cluster.open-cluster-management.io/v1",
			"kind":       "ManagedCluster",
			"metadata": map[string]any{
				"name": name,
			},
			"spec": map[string]any{
				"managedClusterClientConfigs": []any{clientConfig},
			},
		},
	}
}

// newFakeMSATokenSecret creates a hub Secret that mimics the token secret provisioned by
// the managed-serviceaccount addon for a spoke cluster.
func newFakeMSATokenSecret(clusterName, token string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      managedServiceAccountName,
				"namespace": clusterName,
			},
			"data": map[string]any{
				"token": base64.StdEncoding.EncodeToString([]byte(token)),
			},
		},
	}
}

// newFakeManagedProxyConfiguration creates a ManagedProxyConfiguration CR that points
// the cluster-proxy addon to the given namespace.
func newFakeManagedProxyConfiguration(namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "proxy.open-cluster-management.io/v1alpha1",
			"kind":       "ManagedProxyConfiguration",
			"metadata": map[string]any{
				"name": managedProxyConfigurationName,
			},
			"spec": map[string]any{
				"proxyServer": map[string]any{
					"namespace": namespace,
				},
			},
		},
	}
}

// newFakeProxyUserRoute creates a Route that mimics the cluster-proxy-addon-user Route
// exposed by the cluster-proxy addon on the hub.
func newFakeProxyUserRoute(namespace, host string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "route.openshift.io/v1",
			"kind":       "Route",
			"metadata": map[string]any{
				"name":      proxyUserRouteName,
				"namespace": namespace,
			},
			"spec": map[string]any{
				"host": host,
			},
		},
	}
}

// withHubClient temporarily overrides newHubDynamicClient for the duration of the
// supplied function.
func withHubClient(client dynamic.Interface, err error, fn func()) {
	orig := newHubDynamicClient
	newHubDynamicClient = func() (dynamic.Interface, error) {
		return client, err
	}
	defer func() { newHubDynamicClient = orig }()
	fn()
}

var _ = Describe("BuildRestConfigForManagedCluster", func() {
	var (
		caB64 = base64.StdEncoding.EncodeToString(testCABytes)
	)

	It("builds a rest.Config using the proxy Route URL and MSA token", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		msaSec := newFakeMSATokenSecret("spoke1", testMSAToken)
		mpc := newFakeManagedProxyConfiguration(defaultProxyNamespace)
		route := newFakeProxyUserRoute(defaultProxyNamespace, testProxyRouteHost)
		ingressCA := newFakeIngressCASecret()
		fake := newHubTestFakeDynamicClient(mc, msaSec, mpc, route, ingressCA)

		var (
			cfg    *rest.Config
			outErr error
		)
		withHubClient(fake, nil, func() {
			cfg, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})

		Expect(outErr).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.Host).To(Equal("https://" + testProxyRouteHost + "/spoke1"))
		Expect(cfg.BearerToken).To(Equal(testMSAToken))
		Expect(cfg.TLSClientConfig.CAData).To(Equal(testIngressCABytes))
	})

	It("returns an error when the managed cluster name is empty", func() {
		var outErr error
		withHubClient(newHubTestFakeDynamicClient(), nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "  ")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("managed cluster name is empty"))
	})

	DescribeTable("returns an error when the managed cluster name is invalid",
		func(name string) {
			var outErr error
			withHubClient(newHubTestFakeDynamicClient(), nil, func() {
				_, outErr = BuildRestConfigForManagedCluster(context.Background(), name)
			})
			Expect(outErr).To(HaveOccurred())
			Expect(outErr.Error()).To(ContainSubstring("not a valid Kubernetes resource name"))
		},
		Entry("path traversal", "../../foo"),
		Entry("uppercase letters", "Spoke1"),
		Entry("leading hyphen", "-spoke1"),
		Entry("trailing hyphen", "spoke1-"),
		Entry("slash", "spoke/1"),
	)

	It("returns an error when the ManagedCluster resource is not found", func() {
		fake := newHubTestFakeDynamicClient()

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("ManagedCluster"))
		Expect(outErr.Error()).To(ContainSubstring("not found"))
	})

	It("returns an error when the ManagedCluster caBundle is empty", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, "")
		fake := newHubTestFakeDynamicClient(mc)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("caBundle"))
	})

	It("returns an error when the ManagedCluster has no usable URL", func() {
		mc := newFakeManagedCluster("spoke1", "", caB64)
		fake := newHubTestFakeDynamicClient(mc)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("API URL"))
	})

	It("returns an error when the ManagedServiceAccount token secret is not found", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		fake := newHubTestFakeDynamicClient(mc)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring(managedServiceAccountName))
		Expect(outErr.Error()).To(ContainSubstring("not found"))
	})

	It("returns an error when the ManagedServiceAccount token secret has no token key", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		emptyTokenSec := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata": map[string]any{
					"name":      managedServiceAccountName,
					"namespace": "spoke1",
				},
				"data": map[string]any{},
			},
		}
		fake := newHubTestFakeDynamicClient(mc, emptyTokenSec)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("token"))
	})

	It("returns an error when the ManagedProxyConfiguration is not found", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		msaSec := newFakeMSATokenSecret("spoke1", testMSAToken)
		fake := newHubTestFakeDynamicClient(mc, msaSec)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("ManagedProxyConfiguration"))
		Expect(outErr.Error()).To(ContainSubstring("not found"))
	})

	It("returns an error when the proxy Route is not found", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		msaSec := newFakeMSATokenSecret("spoke1", testMSAToken)
		mpc := newFakeManagedProxyConfiguration(defaultProxyNamespace)
		fake := newHubTestFakeDynamicClient(mc, msaSec, mpc)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring(proxyUserRouteName))
		Expect(outErr.Error()).To(ContainSubstring("not found"))
	})
})

// fakeClusterClientFactory records the rest.Config it receives so tests can verify
// which connection path was taken.
type fakeClusterClientFactory struct {
	gotConfig *rest.Config
	err       error
}

func (f *fakeClusterClientFactory) NewClient(config *rest.Config) (ClusterClient, error) {
	f.gotConfig = config
	return nil, f.err
}

var _ = Describe("ResolveRDS with managed_cluster", func() {
	It("builds the cluster client from the proxy Route URL with MSA credentials", func() {
		caB64 := base64.StdEncoding.EncodeToString(testCABytes)
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		msaSec := newFakeMSATokenSecret("spoke1", testMSAToken)
		mpc := newFakeManagedProxyConfiguration(defaultProxyNamespace)
		route := newFakeProxyUserRoute(defaultProxyNamespace, testProxyRouteHost)
		ingressCA := newFakeIngressCASecret()
		fake := newHubTestFakeDynamicClient(mc, msaSec, mpc, route, ingressCA)

		factory := &fakeClusterClientFactory{err: errors.New("stop-after-connect")}
		service := &ReferenceService{
			Registry:       DefaultRegistry,
			ClusterFactory: factory,
		}

		args := &ResolveRDSArgs{ManagedCluster: "spoke1", RDSType: RDSTypeCore}
		withHubClient(fake, nil, func() {
			_, _ = service.ResolveRDS(context.Background(), args)
		})

		Expect(factory.gotConfig).NotTo(BeNil())
		Expect(factory.gotConfig.Host).To(Equal("https://" + testProxyRouteHost + "/spoke1"))
		Expect(factory.gotConfig.BearerToken).To(Equal(testMSAToken))
	})
})

var _ = Describe("managed_cluster mutual exclusivity", func() {
	assertConflict := func(result *mcp.CallToolResult, err error) {
		Expect(err).NotTo(HaveOccurred())
		Expect(result.IsError).To(BeTrue())
		textContent, ok := result.Content[0].(*mcp.TextContent)
		Expect(ok).To(BeTrue())
		Expect(textContent.Text).To(ContainSubstring("managed_cluster"))
	}

	It("rejects managed_cluster with kubeconfig in cluster_diff", func() {
		result, _, err := HandleClusterDiff(context.Background(), nil, ClusterDiffInput{
			Reference:      "https://example.com/metadata.yaml",
			ManagedCluster: "spoke1",
			Kubeconfig:     "some-kubeconfig",
		})
		assertConflict(result, err)
	})

	It("rejects managed_cluster with context in cluster_diff", func() {
		result, _, err := HandleClusterDiff(context.Background(), nil, ClusterDiffInput{
			Reference:      "https://example.com/metadata.yaml",
			ManagedCluster: "spoke1",
			Context:        "ctx",
		})
		assertConflict(result, err)
	})

	It("rejects managed_cluster with kubeconfig in resolve_rds", func() {
		result, _, err := HandleResolveRDS(context.Background(), nil, ResolveRDSInput{
			RDSType:        RDSTypeCore,
			ManagedCluster: "spoke1",
			Kubeconfig:     "some-kubeconfig",
		})
		assertConflict(result, err)
	})

	It("rejects managed_cluster with kubeconfig in validate_rds", func() {
		result, _, err := HandleValidateRDS(context.Background(), nil, ValidateRDSInput{
			RDSType:        RDSTypeCore,
			ManagedCluster: "spoke1",
			Kubeconfig:     "some-kubeconfig",
		})
		assertConflict(result, err)
	})
})

var _ = Describe("managed_cluster schema", func() {
	It("is present with a pattern on the three tool schemas", func() {
		clusterDiff := ClusterDiffInputSchema()
		Expect(clusterDiff.Properties).To(HaveKey("managed_cluster"))
		Expect(clusterDiff.Properties["managed_cluster"].Pattern).To(Equal(k8sNamePattern))

		resolveRDS := ResolveRDSInputSchema()
		Expect(resolveRDS.Properties).To(HaveKey("managed_cluster"))
		Expect(resolveRDS.Properties["managed_cluster"].Pattern).To(Equal(k8sNamePattern))

		validateRDS := ValidateRDSInputSchema()
		Expect(validateRDS.Properties).To(HaveKey("managed_cluster"))
		Expect(validateRDS.Properties["managed_cluster"].Pattern).To(Equal(k8sNamePattern))
	})
})
