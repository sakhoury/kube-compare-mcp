// SPDX-License-Identifier: Apache-2.0

package k8s_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
	"github.com/sakhoury/kube-compare-mcp/pkg/k8s"
)

var (
	crdGVR = schema.GroupVersionResource{
		Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions",
	}
	managedClusterGVR = k8s.ManagedClusterGVR
)

var _ = Describe("Hub Detection", func() {
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

	Describe("DetectHub", func() {
		It("should detect a non-hub cluster when MultiClusterHub CRD is absent", func() {
			mockInspector.EXPECT().
				GetResource(gomock.Any(), crdGVR, "multiclusterhubs.operator.open-cluster-management.io", "").
				Return(nil, fmt.Errorf("not found"))

			info, diag := k8s.DetectHub(context.Background(), mockInspector)
			Expect(info.IsHub).To(BeFalse())
			Expect(diag.IsHub).To(BeFalse())
		})

		It("should detect a hub cluster when MultiClusterHub CRD is present", func() {
			mockInspector.EXPECT().
				GetResource(gomock.Any(), crdGVR, "multiclusterhubs.operator.open-cluster-management.io", "").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apiextensions.k8s.io/v1",
						"kind":       "CustomResourceDefinition",
						"metadata":   map[string]interface{}{"name": "multiclusterhubs.operator.open-cluster-management.io"},
					},
				}, nil)

			info, diag := k8s.DetectHub(context.Background(), mockInspector)
			Expect(info.IsHub).To(BeTrue())
			Expect(diag.IsHub).To(BeTrue())
		})
	})

	Describe("ExtractManagedClusters", func() {
		It("should extract non-compliant clusters from policy status", func() {
			policy := &acm.PolicyDetail{
				Name:      "test-policy",
				Namespace: "ztp-common",
				AffectedClusters: []acm.ClusterStatus{
					{Name: "cluster1", Compliant: "NonCompliant"},
					{Name: "cluster2", Compliant: "Compliant"},
					{Name: "cluster3", Compliant: "NonCompliant"},
				},
			}

			clusters := k8s.ExtractManagedClusters(context.Background(), mockInspector, policy)
			Expect(clusters).To(ConsistOf("cluster1", "cluster3"))
		})

		It("should extract clusters with empty compliance (treat as non-compliant)", func() {
			policy := &acm.PolicyDetail{
				Name:      "test-policy",
				Namespace: "ztp-common",
				AffectedClusters: []acm.ClusterStatus{
					{Name: "cluster1", Compliant: ""},
				},
			}

			clusters := k8s.ExtractManagedClusters(context.Background(), mockInspector, policy)
			Expect(clusters).To(ConsistOf("cluster1"))
		})

		It("should detect namespace matching a ManagedCluster when status is empty", func() {
			policy := &acm.PolicyDetail{
				Name:      "test-policy",
				Namespace: "cnfdg4",
			}

			mockInspector.EXPECT().
				GetResource(gomock.Any(), managedClusterGVR, "cnfdg4", "").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "cluster.open-cluster-management.io/v1",
						"kind":       "ManagedCluster",
						"metadata":   map[string]interface{}{"name": "cnfdg4"},
					},
				}, nil)

			clusters := k8s.ExtractManagedClusters(context.Background(), mockInspector, policy)
			Expect(clusters).To(ConsistOf("cnfdg4"))
		})

		It("should return nil when no clusters can be determined", func() {
			policy := &acm.PolicyDetail{
				Name:      "test-policy",
				Namespace: "ztp-common",
			}

			mockInspector.EXPECT().
				GetResource(gomock.Any(), managedClusterGVR, "ztp-common", "").
				Return(nil, fmt.Errorf("not found"))

			clusters := k8s.ExtractManagedClusters(context.Background(), mockInspector, policy)
			Expect(clusters).To(BeNil())
		})
	})
})
