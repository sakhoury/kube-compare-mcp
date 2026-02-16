// SPDX-License-Identifier: Apache-2.0

package k8s_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
	"github.com/sakhoury/kube-compare-mcp/pkg/k8s"
)

var _ = Describe("OLM Analysis", func() {
	Describe("AnalyzeSubscription", func() {
		It("should detect subscription pending manual approval", func() {
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "operators.coreos.com/v1alpha1",
					"kind":       "Subscription",
					"metadata":   map[string]interface{}{"name": "sriov-network-operator-subscription", "namespace": "openshift-sriov-network-operator"},
					"spec": map[string]interface{}{
						"installPlanApproval": "Manual",
						"name":                "sriov-network-operator",
						"channel":             "stable",
					},
					"status": map[string]interface{}{
						"state":        "UpgradePending",
						"currentCSV":   "sriov-network-operator.v4.17.1",
						"installedCSV": "sriov-network-operator.v4.17.0",
						"catalogHealth": []interface{}{
							map[string]interface{}{
								"healthy": true,
								"catalogSourceRef": map[string]interface{}{
									"name": "redhat-operators",
								},
							},
						},
					},
				},
			}

			desired := map[string]interface{}{
				"status": map[string]interface{}{
					"state": "AtLatestKnown",
				},
			}

			result := k8s.AnalyzeSubscription(actual, desired)
			Expect(result).NotTo(BeNil())
			Expect(result.InstallPlanApproval).To(Equal("Manual"))
			Expect(result.CurrentState).To(Equal("UpgradePending"))
			Expect(result.ExpectedState).To(Equal("AtLatestKnown"))
			Expect(result.CurrentCSV).To(Equal("sriov-network-operator.v4.17.1"))
			Expect(result.InstalledCSV).To(Equal("sriov-network-operator.v4.17.0"))
			Expect(result.CatalogHealth).To(Equal("healthy"))

			Expect(k8s.IsSubscriptionPendingApproval(result)).To(BeTrue())
		})

		It("should return nil root cause for healthy subscription", func() {
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "operators.coreos.com/v1alpha1",
					"kind":       "Subscription",
					"metadata":   map[string]interface{}{"name": "sriov-sub", "namespace": "openshift-sriov"},
					"spec": map[string]interface{}{
						"installPlanApproval": "Manual",
					},
					"status": map[string]interface{}{
						"state":      "AtLatestKnown",
						"currentCSV": "sriov-v4.17.0",
					},
				},
			}

			desired := map[string]interface{}{
				"status": map[string]interface{}{
					"state": "AtLatestKnown",
				},
			}

			result := k8s.AnalyzeSubscription(actual, desired)
			Expect(result).NotTo(BeNil())
			Expect(k8s.IsSubscriptionPendingApproval(result)).To(BeFalse())
			Expect(k8s.SubscriptionRootCause(result)).To(BeNil())
		})

		It("should detect unhealthy catalog source", func() {
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "operators.coreos.com/v1alpha1",
					"kind":       "Subscription",
					"metadata":   map[string]interface{}{"name": "test-sub", "namespace": "test-ns"},
					"spec": map[string]interface{}{
						"installPlanApproval": "Automatic",
					},
					"status": map[string]interface{}{
						"state": "UpgradePending",
						"catalogHealth": []interface{}{
							map[string]interface{}{
								"healthy": false,
								"catalogSourceRef": map[string]interface{}{
									"name": "redhat-operators",
								},
							},
						},
					},
				},
			}

			result := k8s.AnalyzeSubscription(actual, map[string]interface{}{})
			Expect(result).NotTo(BeNil())
			Expect(result.CatalogHealth).To(ContainSubstring("unhealthy"))

			rootCause := k8s.SubscriptionRootCause(result)
			Expect(rootCause).NotTo(BeNil())
			Expect(rootCause.PrimaryCause).To(Equal(acm.CauseMissingDependency))
		})

		It("should generate correct root cause for pending approval", func() {
			result := &acm.OLMAnalysisResult{
				InstallPlanApproval: "Manual",
				CurrentState:        "UpgradePending",
				ExpectedState:       "AtLatestKnown",
				CurrentCSV:          "op.v4.17.1",
				InstalledCSV:        "op.v4.17.0",
				CatalogHealth:       "healthy",
			}

			rootCause := k8s.SubscriptionRootCause(result)
			Expect(rootCause).NotTo(BeNil())
			Expect(rootCause.PrimaryCause).To(Equal(acm.CauseSubscriptionPendingApproval))
			Expect(rootCause.Confidence).To(Equal(acm.ConfidenceHigh))
			Expect(rootCause.Detail).To(ContainSubstring("installPlanApproval: Manual"))
			Expect(rootCause.Detail).To(ContainSubstring("UpgradePending"))
			Expect(rootCause.Detail).To(ContainSubstring("Current CSV: op.v4.17.1"))
		})
	})

	Describe("IsOLMSubscription", func() {
		It("should identify OLM Subscription", func() {
			tmpl := &acm.ConfigPolicyTemplate{
				Kind:       "Subscription",
				APIVersion: "operators.coreos.com/v1alpha1",
			}
			Expect(k8s.IsOLMSubscription(tmpl)).To(BeTrue())
		})

		It("should not match non-OLM Subscription", func() {
			tmpl := &acm.ConfigPolicyTemplate{
				Kind:       "Deployment",
				APIVersion: "apps/v1",
			}
			Expect(k8s.IsOLMSubscription(tmpl)).To(BeFalse())
		})
	})

	Describe("IsOLMOperator", func() {
		It("should identify OLM Operator", func() {
			tmpl := &acm.ConfigPolicyTemplate{
				Kind:       "Operator",
				APIVersion: "operators.coreos.com/v1alpha1",
			}
			Expect(k8s.IsOLMOperator(tmpl)).To(BeTrue())
		})
	})
})
