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

	"github.com/sakhoury/kube-compare-mcp/pkg/k8s"
)

var namespaceGVR = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}

var _ = Describe("Namespace Health Check", func() {
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

	Describe("CheckNamespaceHealth", func() {
		It("should return nil for empty namespace (cluster-scoped)", func() {
			result := k8s.CheckNamespaceHealth(context.Background(), mockInspector, "")
			Expect(result).To(BeNil())
		})

		It("should detect a healthy Active namespace", func() {
			mockInspector.EXPECT().
				GetResource(gomock.Any(), namespaceGVR, "production", "").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Namespace",
						"metadata":   map[string]interface{}{"name": "production"},
						"status":     map[string]interface{}{"phase": "Active"},
					},
				}, nil)

			result := k8s.CheckNamespaceHealth(context.Background(), mockInspector, "production")
			Expect(result).NotTo(BeNil())
			Expect(result.Exists).To(BeTrue())
			Expect(result.Phase).To(Equal("Active"))
			Expect(result.Healthy).To(BeTrue())
		})

		It("should detect a Terminating namespace", func() {
			mockInspector.EXPECT().
				GetResource(gomock.Any(), namespaceGVR, "openshift-ptp", "").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "v1",
						"kind":       "Namespace",
						"metadata":   map[string]interface{}{"name": "openshift-ptp"},
						"status":     map[string]interface{}{"phase": "Terminating"},
					},
				}, nil)

			result := k8s.CheckNamespaceHealth(context.Background(), mockInspector, "openshift-ptp")
			Expect(result).NotTo(BeNil())
			Expect(result.Exists).To(BeTrue())
			Expect(result.Phase).To(Equal("Terminating"))
			Expect(result.Healthy).To(BeFalse())
		})

		It("should detect a missing namespace", func() {
			mockInspector.EXPECT().
				GetResource(gomock.Any(), namespaceGVR, "nonexistent", "").
				Return(nil, fmt.Errorf("namespaces \"nonexistent\" not found"))

			result := k8s.CheckNamespaceHealth(context.Background(), mockInspector, "nonexistent")
			Expect(result).NotTo(BeNil())
			Expect(result.Exists).To(BeFalse())
			Expect(result.Healthy).To(BeFalse())
		})
	})

	Describe("CheckCRDRegistered", func() {
		crdGVR := schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

		It("should return true for core API resources", func() {
			exists, _ := k8s.CheckCRDRegistered(context.Background(), mockInspector, "Deployment", "apps/v1")
			Expect(exists).To(BeTrue())
		})

		It("should return true for v1 core resources", func() {
			exists, _ := k8s.CheckCRDRegistered(context.Background(), mockInspector, "ConfigMap", "v1")
			Expect(exists).To(BeTrue())
		})

		It("should detect missing CRD", func() {
			mockInspector.EXPECT().
				GetResource(gomock.Any(), crdGVR, "ptpconfigs.ptp.openshift.io", "").
				Return(nil, fmt.Errorf("not found"))

			exists, crdName := k8s.CheckCRDRegistered(context.Background(), mockInspector, "PtpConfig", "ptp.openshift.io/v1")
			Expect(exists).To(BeFalse())
			Expect(crdName).To(Equal("ptpconfigs.ptp.openshift.io"))
		})

		It("should detect installed CRD", func() {
			mockInspector.EXPECT().
				GetResource(gomock.Any(), crdGVR, "subscriptions.operators.coreos.com", "").
				Return(&unstructured.Unstructured{
					Object: map[string]interface{}{
						"apiVersion": "apiextensions.k8s.io/v1",
						"kind":       "CustomResourceDefinition",
						"metadata":   map[string]interface{}{"name": "subscriptions.operators.coreos.com"},
					},
				}, nil)

			exists, crdName := k8s.CheckCRDRegistered(context.Background(), mockInspector, "Subscription", "operators.coreos.com/v1alpha1")
			Expect(exists).To(BeTrue())
			Expect(crdName).To(Equal("subscriptions.operators.coreos.com"))
		})
	})

	Describe("CheckRBACVisibility", func() {
		It("should return true when access is allowed", func() {
			gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
			mockInspector.EXPECT().
				CheckAccess(gomock.Any(), "get", gvr, "production").
				Return(true, nil)

			allowed, err := k8s.CheckRBACVisibility(context.Background(), mockInspector, gvr, "production")
			Expect(err).NotTo(HaveOccurred())
			Expect(allowed).To(BeTrue())
		})

		It("should return false when access is denied", func() {
			gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
			mockInspector.EXPECT().
				CheckAccess(gomock.Any(), "get", gvr, "restricted-ns").
				Return(false, nil)

			allowed, err := k8s.CheckRBACVisibility(context.Background(), mockInspector, gvr, "restricted-ns")
			Expect(err).NotTo(HaveOccurred())
			Expect(allowed).To(BeFalse())
		})
	})
})
