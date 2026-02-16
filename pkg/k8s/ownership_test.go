// SPDX-License-Identifier: Apache-2.0

package k8s_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
	"github.com/sakhoury/kube-compare-mcp/pkg/k8s"
)

var _ = Describe("ACM Ownership Analysis", func() {
	Describe("AnalyzeOwnership", func() {
		It("should detect ArgoCD managed fields", func() {
			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "frontend",
						"namespace": "production",
					},
				},
			}
			resource.SetManagedFields([]metav1.ManagedFieldsEntry{
				{
					Manager:   "argocd-controller",
					Operation: metav1.ManagedFieldsOperationApply,
				},
			})

			result := k8s.AnalyzeOwnership(resource)
			Expect(result.HasExternalOwner).To(BeTrue())
			Expect(result.Owners).NotTo(BeEmpty())
			Expect(result.Owners[0].Manager).To(Equal("argocd-controller"))
			Expect(result.FixTarget).To(ContainSubstring("ArgoCD"))
		})

		It("should detect Helm annotations", func() {
			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Service",
					"metadata": map[string]interface{}{
						"name":      "my-service",
						"namespace": "default",
						"annotations": map[string]interface{}{
							"meta.helm.sh/release-name": "my-release",
						},
					},
				},
			}

			result := k8s.AnalyzeOwnership(resource)
			Expect(result.HasExternalOwner).To(BeTrue())
			Expect(result.FixTarget).To(ContainSubstring("Helm"))
		})

		It("should detect Flux annotations", func() {
			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "backend",
						"namespace": "production",
						"annotations": map[string]interface{}{
							"kustomize.toolkit.fluxcd.io/name": "my-kustomization",
						},
					},
				},
			}

			result := k8s.AnalyzeOwnership(resource)
			Expect(result.HasExternalOwner).To(BeTrue())
			Expect(result.FixTarget).To(ContainSubstring("Flux"))
		})

		It("should detect controller ownerReferences", func() {
			isController := true
			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Pod",
					"metadata": map[string]interface{}{
						"name":      "my-pod-abc123",
						"namespace": "default",
						"ownerReferences": []interface{}{
							map[string]interface{}{
								"apiVersion": "apps/v1",
								"kind":       "ReplicaSet",
								"name":       "my-deployment-abc123",
								"controller": isController,
								"uid":        "test-uid",
							},
						},
					},
				},
			}

			result := k8s.AnalyzeOwnership(resource)
			Expect(result.HasExternalOwner).To(BeTrue())
			Expect(result.Owners).NotTo(BeEmpty())
			found := false
			for _, owner := range result.Owners {
				if owner.Type == "owner_reference" && owner.Controller {
					found = true
					break
				}
			}
			Expect(found).To(BeTrue())
		})

		It("should report no external owner for vanilla resources", func() {
			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "my-config",
						"namespace": "default",
					},
				},
			}

			result := k8s.AnalyzeOwnership(resource)
			Expect(result.HasExternalOwner).To(BeFalse())
			Expect(result.FixTarget).To(BeEmpty())
		})

		It("should detect managed-by label for known tools", func() {
			resource := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "my-config",
						"namespace": "default",
						"labels": map[string]interface{}{
							"app.kubernetes.io/managed-by": "Helm",
						},
					},
				},
			}

			result := k8s.AnalyzeOwnership(resource)
			Expect(result.HasExternalOwner).To(BeTrue())
		})
	})

	Describe("KindToResource", func() {
		DescribeTable("converts kinds to resources",
			func(kind, expected string) {
				result := acm.KindToResource(kind)
				Expect(result).To(Equal(expected))
			},
			Entry("Deployment", "Deployment", "deployments"),
			Entry("Pod", "Pod", "pods"),
			Entry("Service", "Service", "services"),
			Entry("Policy", "Policy", "policies"),
			Entry("NetworkPolicy", "NetworkPolicy", "networkpolicies"),
			Entry("ConfigMap", "ConfigMap", "configmaps"),
			Entry("Ingress", "Ingress", "ingresses"),
			Entry("StorageClass", "StorageClass", "storageclasses"),
		)
	})

	Describe("ParseAPIVersion", func() {
		DescribeTable("parses API versions",
			func(apiVersion, expectedGroup, expectedVersion string) {
				group, version := acm.ParseAPIVersion(apiVersion)
				Expect(group).To(Equal(expectedGroup))
				Expect(version).To(Equal(expectedVersion))
			},
			Entry("core v1", "v1", "", "v1"),
			Entry("apps/v1", "apps/v1", "apps", "v1"),
			Entry("networking.k8s.io/v1", "networking.k8s.io/v1", "networking.k8s.io", "v1"),
			Entry("policy.open-cluster-management.io/v1", "policy.open-cluster-management.io/v1", "policy.open-cluster-management.io", "v1"),
		)
	})
})
