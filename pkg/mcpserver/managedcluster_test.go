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

var testCABytes = []byte("FAKE-CA-DATA")

// testSpokeKubeconfig is a minimal, security-valid kubeconfig using token auth.
const testSpokeKubeconfig = `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://original.example.com:6443
    insecure-skip-tls-verify: true
  name: spoke
contexts:
- context:
    cluster: spoke
    user: admin
  name: spoke-ctx
current-context: spoke-ctx
users:
- name: admin
  user:
    token: spoke-token
`

var managedClusterGVRToListKind = map[schema.GroupVersionResource]string{
	{Group: "cluster.open-cluster-management.io", Version: "v1", Resource: "managedclusters"}: "ManagedClusterList",
	{Group: "", Version: "v1", Resource: "secrets"}:                                           "SecretList",
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

func newFakeAdminKubeconfigSecret(clusterName, kubeconfigB64 string) *unstructured.Unstructured {
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "v1",
			"kind":       "Secret",
			"metadata": map[string]any{
				"name":      clusterName + "-admin-kubeconfig",
				"namespace": clusterName,
			},
			"data": map[string]any{
				"kubeconfig": kubeconfigB64,
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
		caB64         = base64.StdEncoding.EncodeToString(testCABytes)
		kubeconfigB64 = base64.StdEncoding.EncodeToString([]byte(testSpokeKubeconfig))
	)

	It("builds a rest.Config using the MC endpoint and secret credentials", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		sec := newFakeAdminKubeconfigSecret("spoke1", kubeconfigB64)
		fake := newHubTestFakeDynamicClient(mc, sec)

		var (
			cfg    *rest.Config
			outErr error
		)
		withHubClient(fake, nil, func() {
			cfg, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})

		Expect(outErr).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.Host).To(Equal(testSpokeURL))
		Expect(cfg.BearerToken).To(Equal("spoke-token"))
		Expect(cfg.TLSClientConfig.CAData).To(Equal(testCABytes))
		Expect(cfg.TLSClientConfig.Insecure).To(BeFalse())
	})

	It("keeps the kubeconfig CA when the MC caBundle is empty", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, "")
		sec := newFakeAdminKubeconfigSecret("spoke1", kubeconfigB64)
		fake := newHubTestFakeDynamicClient(mc, sec)

		var (
			cfg    *rest.Config
			outErr error
		)
		withHubClient(fake, nil, func() {
			cfg, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})

		Expect(outErr).NotTo(HaveOccurred())
		Expect(cfg.Host).To(Equal(testSpokeURL))
		// caBundle empty -> we do not force CAData; the kubeconfig used insecure.
		Expect(cfg.TLSClientConfig.CAData).To(BeEmpty())
		Expect(cfg.TLSClientConfig.Insecure).To(BeTrue())
	})

	It("returns an error when the managed cluster name is empty", func() {
		var outErr error
		withHubClient(newHubTestFakeDynamicClient(), nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "  ")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("managed cluster name is empty"))
	})

	It("returns an error when the ManagedCluster resource is not found", func() {
		sec := newFakeAdminKubeconfigSecret("spoke1", kubeconfigB64)
		fake := newHubTestFakeDynamicClient(sec)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("ManagedCluster"))
		Expect(outErr.Error()).To(ContainSubstring("not found"))
	})

	It("returns an error when the admin kubeconfig secret is not found", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		fake := newHubTestFakeDynamicClient(mc)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("spoke1-admin-kubeconfig"))
		Expect(outErr.Error()).To(ContainSubstring("not found"))
	})

	It("returns an error when the ManagedCluster has no usable URL", func() {
		mc := newFakeManagedCluster("spoke1", "", caB64)
		sec := newFakeAdminKubeconfigSecret("spoke1", kubeconfigB64)
		fake := newHubTestFakeDynamicClient(mc, sec)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("API URL"))
	})

	It("returns an error when the secret has no kubeconfig key", func() {
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		sec := &unstructured.Unstructured{
			Object: map[string]any{
				"apiVersion": "v1",
				"kind":       "Secret",
				"metadata": map[string]any{
					"name":      "spoke1-admin-kubeconfig",
					"namespace": "spoke1",
				},
				"data": map[string]any{},
			},
		}
		fake := newHubTestFakeDynamicClient(mc, sec)

		var outErr error
		withHubClient(fake, nil, func() {
			_, outErr = BuildRestConfigForManagedCluster(context.Background(), "spoke1")
		})
		Expect(outErr).To(HaveOccurred())
		Expect(outErr.Error()).To(ContainSubstring("kubeconfig"))
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
	It("builds the cluster client from the managed cluster endpoint", func() {
		caB64 := base64.StdEncoding.EncodeToString(testCABytes)
		kubeconfigB64 := base64.StdEncoding.EncodeToString([]byte(testSpokeKubeconfig))
		mc := newFakeManagedCluster("spoke1", testSpokeURL, caB64)
		sec := newFakeAdminKubeconfigSecret("spoke1", kubeconfigB64)
		fake := newHubTestFakeDynamicClient(mc, sec)

		factory := &fakeClusterClientFactory{err: errors.New("stop-after-connect")}
		service := &ReferenceService{
			Registry:       DefaultRegistry,
			ClusterFactory: factory,
		}

		args := &ResolveRDSArgs{ManagedCluster: "spoke1", RDSType: RDSTypeCore}
		withHubClient(fake, nil, func() {
			// The factory returns an error, so ResolveRDS stops right after connecting.
			_, _ = service.ResolveRDS(context.Background(), args)
		})

		// Sanity: the factory was invoked with the spoke endpoint, proving the
		// managed_cluster branch was taken instead of the in-cluster fallback.
		Expect(factory.gotConfig).NotTo(BeNil())
		Expect(factory.gotConfig.Host).To(Equal(testSpokeURL))
		Expect(factory.gotConfig.TLSClientConfig.CAData).To(Equal(testCABytes))
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
