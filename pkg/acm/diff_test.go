// SPDX-License-Identifier: Apache-2.0

package acm_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

var _ = Describe("Field Diff", func() {
	Describe("ComputeFieldDiffs", func() {
		It("should return nil for nil actual resource", func() {
			desired := map[string]interface{}{"spec": map[string]interface{}{"replicas": float64(3)}}
			diffs := acm.ComputeFieldDiffs(desired, nil)
			Expect(diffs).To(BeNil())
		})

		It("should return nil for empty desired", func() {
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{"spec": map[string]interface{}{"replicas": float64(1)}},
			}
			diffs := acm.ComputeFieldDiffs(map[string]interface{}{}, actual)
			Expect(diffs).To(BeNil())
		})

		It("should detect scalar field differences", func() {
			desired := map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"spec": map[string]interface{}{
					"replicas": float64(3),
				},
			}
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"spec": map[string]interface{}{
						"replicas": float64(1),
					},
				},
			}

			diffs := acm.ComputeFieldDiffs(desired, actual)
			Expect(diffs).To(HaveLen(1))
			Expect(diffs[0].Path).To(Equal("spec.replicas"))
			Expect(diffs[0].Expected).To(Equal(float64(3)))
			Expect(diffs[0].Actual).To(Equal(float64(1)))
		})

		It("should detect missing fields in actual", func() {
			desired := map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"spec": map[string]interface{}{
					"replicas": float64(3),
					"paused":   true,
				},
			}
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"spec": map[string]interface{}{
						"replicas": float64(3),
					},
				},
			}

			diffs := acm.ComputeFieldDiffs(desired, actual)
			Expect(diffs).To(HaveLen(1))
			Expect(diffs[0].Path).To(Equal("spec.paused"))
			Expect(diffs[0].Expected).To(Equal(true))
			Expect(diffs[0].Actual).To(BeNil())
		})

		It("should detect label differences in metadata", func() {
			desired := map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]interface{}{
					"name": "openshift-logging",
					"labels": map[string]interface{}{
						"openshift.io/cluster-monitoring": "true",
					},
				},
			}
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Namespace",
					"metadata": map[string]interface{}{
						"name":   "openshift-logging",
						"labels": map[string]interface{}{},
					},
				},
			}

			diffs := acm.ComputeFieldDiffs(desired, actual)
			Expect(diffs).NotTo(BeEmpty())
			found := false
			for _, d := range diffs {
				if d.Path == "metadata.labels.openshift.io/cluster-monitoring" {
					found = true
					Expect(d.Expected).To(Equal("true"))
					Expect(d.Actual).To(BeNil())
				}
			}
			Expect(found).To(BeTrue(), "Expected to find metadata.labels diff")
		})

		It("should detect annotation differences in metadata", func() {
			desired := map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Namespace",
				"metadata": map[string]interface{}{
					"name": "openshift-logging",
					"annotations": map[string]interface{}{
						"workload.openshift.io/allowed": "management",
					},
				},
			}
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "Namespace",
					"metadata": map[string]interface{}{
						"name":        "openshift-logging",
						"annotations": map[string]interface{}{},
					},
				},
			}

			diffs := acm.ComputeFieldDiffs(desired, actual)
			Expect(diffs).NotTo(BeEmpty())
			found := false
			for _, d := range diffs {
				if d.Path == "metadata.annotations.workload.openshift.io/allowed" {
					found = true
					Expect(d.Expected).To(Equal("management"))
					Expect(d.Actual).To(BeNil())
				}
			}
			Expect(found).To(BeTrue(), "Expected to find metadata.annotations diff")
		})

		It("should detect status differences (e.g. subscription state)", func() {
			desired := map[string]interface{}{
				"apiVersion": "operators.coreos.com/v1alpha1",
				"kind":       "Subscription",
				"status": map[string]interface{}{
					"state": "AtLatestKnown",
				},
			}
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "operators.coreos.com/v1alpha1",
					"kind":       "Subscription",
					"status": map[string]interface{}{
						"state": "UpgradePending",
					},
				},
			}

			diffs := acm.ComputeFieldDiffs(desired, actual)
			Expect(diffs).NotTo(BeEmpty())
			found := false
			for _, d := range diffs {
				if d.Path == "status.state" {
					found = true
					Expect(d.Expected).To(Equal("AtLatestKnown"))
					Expect(d.Actual).To(Equal("UpgradePending"))
				}
			}
			Expect(found).To(BeTrue(), "Expected to find status.state diff")
		})

		It("should return empty diffs when resources match", func() {
			desired := map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"spec": map[string]interface{}{
					"replicas": float64(3),
				},
			}
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"spec": map[string]interface{}{
						"replicas": float64(3),
					},
				},
			}

			diffs := acm.ComputeFieldDiffs(desired, actual)
			Expect(diffs).To(BeEmpty())
		})

		It("should be sorted by path", func() {
			desired := map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"spec": map[string]interface{}{
					"replicas":                float64(3),
					"revisionHistoryLimit":    float64(5),
					"progressDeadlineSeconds": float64(600),
				},
			}
			actual := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"spec": map[string]interface{}{
						"replicas":                float64(1),
						"revisionHistoryLimit":    float64(10),
						"progressDeadlineSeconds": float64(300),
					},
				},
			}

			diffs := acm.ComputeFieldDiffs(desired, actual)
			Expect(len(diffs)).To(BeNumerically(">=", 3))
			for i := 1; i < len(diffs); i++ {
				Expect(diffs[i-1].Path < diffs[i].Path).To(BeTrue(),
					"Expected %s < %s", diffs[i-1].Path, diffs[i].Path)
			}
		})
	})
})
