// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"context"
	"encoding/base64"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var (
	clusterDeploymentGVR = schema.GroupVersionResource{
		Group: "hive.openshift.io", Version: "v1", Resource: "clusterdeployments",
	}
	secretGVR = schema.GroupVersionResource{
		Group: "", Version: "v1", Resource: "secrets",
	}
)

// testKubeconfig is a minimal valid kubeconfig for testing.
const testKubeconfig = `apiVersion: v1
clusters:
- cluster:
    server: https://api.test-cluster.example.com:6443
    insecure-skip-tls-verify: true
  name: test-cluster
contexts:
- context:
    cluster: test-cluster
    user: admin
  name: admin
current-context: admin
kind: Config
users:
- name: admin
  user:
    token: test-token-12345
`

var _ = Describe("Managed Cluster Kubeconfig Extraction", func() {
	var (
		ctrl          *gomock.Controller
		mockInspector *MockResourceInspector
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockInspector = NewMockResourceInspector(ctrl)
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	Describe("ExtractManagedClusterKubeconfig", func() {
		It("should extract kubeconfig via ClusterDeployment secret ref", func() {
			kubeconfigB64 := base64.StdEncoding.EncodeToString([]byte(testKubeconfig))

			// Mock: ClusterDeployment found with adminKubeconfigSecretRef
			mockInspector.EXPECT().
				GetResource(gomock.Any(), clusterDeploymentGVR, "cnfdg4", "cnfdg4").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "hive.openshift.io/v1",
						"kind":       "ClusterDeployment",
						"metadata":   map[string]interface{}{"name": "cnfdg4", "namespace": "cnfdg4"},
						"spec": map[string]interface{}{
							"clusterMetadata": map[string]interface{}{
								"adminKubeconfigSecretRef": map[string]interface{}{
									"name": "cnfdg4-admin-kubeconfig",
								},
							},
						},
					},
				}, nil)

			// Mock: Secret found with kubeconfig data
			mockInspector.EXPECT().
				GetResource(gomock.Any(), secretGVR, "cnfdg4-admin-kubeconfig", "cnfdg4").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Secret",
						"metadata":   map[string]interface{}{"name": "cnfdg4-admin-kubeconfig", "namespace": "cnfdg4"},
						"data": map[string]interface{}{
							"kubeconfig": kubeconfigB64,
						},
					},
				}, nil)

			config, source, err := mcpserver.ExtractManagedClusterKubeconfig(context.Background(), mockInspector, "cnfdg4")
			Expect(err).NotTo(HaveOccurred())
			Expect(config).NotTo(BeNil())
			Expect(config.Host).To(Equal("https://api.test-cluster.example.com:6443"))
			Expect(source).To(Equal("ClusterDeployment cnfdg4/cnfdg4-admin-kubeconfig"))
		})

		It("should fall back to well-known secret name when ClusterDeployment is not found", func() {
			kubeconfigB64 := base64.StdEncoding.EncodeToString([]byte(testKubeconfig))

			// Mock: ClusterDeployment not found
			mockInspector.EXPECT().
				GetResource(gomock.Any(), clusterDeploymentGVR, "mycluster", "mycluster").
				Return(nil, fmt.Errorf("not found"))

			// Mock: Fallback secret found
			mockInspector.EXPECT().
				GetResource(gomock.Any(), secretGVR, "mycluster-admin-kubeconfig", "mycluster").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Secret",
						"metadata":   map[string]interface{}{"name": "mycluster-admin-kubeconfig", "namespace": "mycluster"},
						"data": map[string]interface{}{
							"kubeconfig": kubeconfigB64,
						},
					},
				}, nil)

			config, source, err := mcpserver.ExtractManagedClusterKubeconfig(context.Background(), mockInspector, "mycluster")
			Expect(err).NotTo(HaveOccurred())
			Expect(config).NotTo(BeNil())
			Expect(config.Host).To(Equal("https://api.test-cluster.example.com:6443"))
			Expect(source).To(Equal("Secret mycluster/mycluster-admin-kubeconfig (fallback)"))
		})

		It("should return error when neither ClusterDeployment nor fallback secret exist", func() {
			// Mock: ClusterDeployment not found
			mockInspector.EXPECT().
				GetResource(gomock.Any(), clusterDeploymentGVR, "missing", "missing").
				Return(nil, fmt.Errorf("not found"))

			// Mock: Fallback secret not found either
			mockInspector.EXPECT().
				GetResource(gomock.Any(), secretGVR, "missing-admin-kubeconfig", "missing").
				Return(nil, fmt.Errorf("not found"))

			config, source, err := mcpserver.ExtractManagedClusterKubeconfig(context.Background(), mockInspector, "missing")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("kubeconfig secret not found for cluster missing"))
			Expect(config).To(BeNil())
			Expect(source).To(BeEmpty())
		})

		It("should return error when ClusterDeployment found but secret is missing kubeconfig key", func() {
			// Mock: ClusterDeployment found with adminKubeconfigSecretRef
			mockInspector.EXPECT().
				GetResource(gomock.Any(), clusterDeploymentGVR, "broken", "broken").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "hive.openshift.io/v1",
						"kind":       "ClusterDeployment",
						"metadata":   map[string]interface{}{"name": "broken", "namespace": "broken"},
						"spec": map[string]interface{}{
							"clusterMetadata": map[string]interface{}{
								"adminKubeconfigSecretRef": map[string]interface{}{
									"name": "broken-admin-kubeconfig",
								},
							},
						},
					},
				}, nil)

			// Mock: Secret exists but has no kubeconfig key
			mockInspector.EXPECT().
				GetResource(gomock.Any(), secretGVR, "broken-admin-kubeconfig", "broken").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Secret",
						"metadata":   map[string]interface{}{"name": "broken-admin-kubeconfig", "namespace": "broken"},
						"data": map[string]interface{}{
							"other-key": "some-value",
						},
					},
				}, nil)

			config, source, err := mcpserver.ExtractManagedClusterKubeconfig(context.Background(), mockInspector, "broken")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("missing 'kubeconfig' key"))
			Expect(config).To(BeNil())
			Expect(source).To(BeEmpty())
		})

		It("should return error when ClusterDeployment is missing adminKubeconfigSecretRef", func() {
			// Mock: ClusterDeployment found but without clusterMetadata
			mockInspector.EXPECT().
				GetResource(gomock.Any(), clusterDeploymentGVR, "noref", "noref").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "hive.openshift.io/v1",
						"kind":       "ClusterDeployment",
						"metadata":   map[string]interface{}{"name": "noref", "namespace": "noref"},
						"spec": map[string]interface{}{
							"clusterName": "noref",
						},
					},
				}, nil)

			// Mock: Fallback secret not found either
			mockInspector.EXPECT().
				GetResource(gomock.Any(), secretGVR, "noref-admin-kubeconfig", "noref").
				Return(nil, fmt.Errorf("not found"))

			config, source, err := mcpserver.ExtractManagedClusterKubeconfig(context.Background(), mockInspector, "noref")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("kubeconfig secret not found"))
			Expect(config).To(BeNil())
			Expect(source).To(BeEmpty())
		})
	})
})
