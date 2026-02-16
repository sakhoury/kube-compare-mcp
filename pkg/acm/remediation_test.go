// SPDX-License-Identifier: Apache-2.0

package acm_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

var _ = Describe("ACM Remediation Generation", func() {
	Describe("GenerateRemediation", func() {
		It("should extract YAML from policy objectDefinition for musthave", func() {
			template := &acm.ConfigPolicyTemplate{
				Name:              "require-limits",
				ComplianceType:    "musthave",
				Kind:              "LimitRange",
				APIVersion:        "v1",
				ResourceName:      "resource-limits",
				ResourceNamespace: "production",
				ObjectDefinition: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "LimitRange",
					"metadata": map[string]interface{}{
						"name":      "resource-limits",
						"namespace": "production",
					},
					"spec": map[string]interface{}{
						"limits": []interface{}{
							map[string]interface{}{
								"type": "Container",
								"default": map[string]interface{}{
									"memory": "512Mi",
									"cpu":    "500m",
								},
							},
						},
					},
				},
			}

			rootCause := &acm.RootCauseResult{
				PrimaryCause: acm.CauseDirectFixApplicable,
				Confidence:   acm.ConfidenceHigh,
			}

			plan := acm.GenerateRemediation(template, nil, rootCause, nil)
			Expect(plan).NotTo(BeNil())
			Expect(plan.PatchSource).To(Equal("policy_object_definition"))
			Expect(plan.Confidence).To(Equal("high"))
			Expect(plan.PatchYAML).To(ContainSubstring("LimitRange"))
			Expect(plan.PatchYAML).To(ContainSubstring("512Mi"))
			Expect(plan.DirectPatchWorks).To(BeTrue())
			Expect(plan.ApplyCommand).To(ContainSubstring("kubectl apply"))
		})

		It("should generate deletion remediation for mustnothave", func() {
			template := &acm.ConfigPolicyTemplate{
				Name:              "remove-default-sa",
				ComplianceType:    "mustnothave",
				Kind:              "ClusterRoleBinding",
				APIVersion:        "rbac.authorization.k8s.io/v1",
				ResourceName:      "insecure-binding",
				ResourceNamespace: "",
				ObjectDefinition: map[string]interface{}{
					"apiVersion": "rbac.authorization.k8s.io/v1",
					"kind":       "ClusterRoleBinding",
					"metadata": map[string]interface{}{
						"name": "insecure-binding",
					},
				},
			}

			rootCause := &acm.RootCauseResult{
				PrimaryCause: acm.CauseDirectFixApplicable,
			}

			plan := acm.GenerateRemediation(template, nil, rootCause, nil)
			Expect(plan).NotTo(BeNil())
			Expect(plan.PatchSource).To(Equal("deletion"))
			Expect(plan.ApplyCommand).To(ContainSubstring("kubectl delete"))
			Expect(plan.Warnings).NotTo(BeEmpty())
		})

		It("should warn when resource is externally managed", func() {
			template := &acm.ConfigPolicyTemplate{
				Name:              "require-limits",
				ComplianceType:    "musthave",
				Kind:              "Deployment",
				APIVersion:        "apps/v1",
				ResourceName:      "frontend",
				ResourceNamespace: "production",
				ObjectDefinition: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "frontend",
						"namespace": "production",
					},
					"spec": map[string]interface{}{
						"replicas": 3,
					},
				},
			}

			rootCause := &acm.RootCauseResult{
				PrimaryCause: acm.CauseActiveReconciliation,
			}
			ownerResult := &acm.OwnershipResult{
				HasExternalOwner: true,
				Owners: []acm.OwnershipConflict{
					{Manager: "argocd-controller", Type: "managed_fields"},
				},
				FixTarget: "Update the ArgoCD Application source",
			}

			plan := acm.GenerateRemediation(template, nil, rootCause, ownerResult)
			Expect(plan).NotTo(BeNil())
			Expect(plan.DirectPatchWorks).To(BeFalse())
			Expect(plan.Warnings).NotTo(BeEmpty())
			Expect(plan.ActualFix).To(ContainSubstring("ArgoCD"))
		})

		It("should generate merge patch when actual resource exists", func() {
			template := &acm.ConfigPolicyTemplate{
				Name:              "require-limits",
				ComplianceType:    "musthave",
				Kind:              "Deployment",
				APIVersion:        "apps/v1",
				ResourceName:      "frontend",
				ResourceNamespace: "production",
				ObjectDefinition: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "frontend",
						"namespace": "production",
					},
					"spec": map[string]interface{}{
						"replicas": float64(3),
						"template": map[string]interface{}{
							"spec": map[string]interface{}{
								"containers": []interface{}{
									map[string]interface{}{
										"name": "app",
										"resources": map[string]interface{}{
											"limits": map[string]interface{}{
												"memory": "512Mi",
											},
										},
									},
								},
							},
						},
					},
				},
			}

			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "frontend",
						"namespace": "production",
					},
					"spec": map[string]interface{}{
						"replicas": float64(1),
						"template": map[string]interface{}{
							"spec": map[string]interface{}{
								"containers": []interface{}{
									map[string]interface{}{
										"name":  "app",
										"image": "myapp:v1",
									},
								},
							},
						},
					},
				},
			}

			rootCause := &acm.RootCauseResult{
				PrimaryCause: acm.CauseDirectFixApplicable,
			}

			plan := acm.GenerateRemediation(template, actual, rootCause, nil)
			Expect(plan).NotTo(BeNil())
			// Should use objectDefinition (first priority) or merge patch
			Expect(plan.PatchYAML).NotTo(BeEmpty())
			Expect(plan.Confidence).To(Equal("high"))
		})

		It("should fall back to low confidence when no objectDefinition", func() {
			template := &acm.ConfigPolicyTemplate{
				Name:           "custom-check",
				ComplianceType: "musthave",
				Kind:           "CustomResource",
				APIVersion:     "custom.io/v1",
				ResourceName:   "my-cr",
				// Empty ObjectDefinition
				ObjectDefinition: map[string]interface{}{},
			}

			rootCause := &acm.RootCauseResult{
				PrimaryCause: acm.CauseUnknown,
			}

			plan := acm.GenerateRemediation(template, nil, rootCause, nil)
			Expect(plan).NotTo(BeNil())
			Expect(plan.Confidence).To(Equal("low"))
			Expect(plan.PatchSource).To(Equal("none"))
		})
	})
})
