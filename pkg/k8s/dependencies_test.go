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

var _ = Describe("ACM Dependency Validation", func() {
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

	Describe("ValidateDependencies", func() {
		It("should detect missing Secret references", func() {
			objDef := map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]interface{}{
					"name":      "my-pod",
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"containers": []interface{}{
						map[string]interface{}{
							"name":  "app",
							"image": "myapp:latest",
							"env": []interface{}{
								map[string]interface{}{
									"name": "DB_PASSWORD",
									"valueFrom": map[string]interface{}{
										"secretKeyRef": map[string]interface{}{
											"name": "db-secret",
											"key":  "password",
										},
									},
								},
							},
						},
					},
				},
			}

			secretGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
			mockInspector.EXPECT().
				GetResource(gomock.Any(), secretGVR, "db-secret", "default").
				Return(nil, fmt.Errorf("not found"))

			checks := k8s.ValidateDependencies(context.Background(), mockInspector, objDef, "default")

			var secretCheck *acm.DependencyCheck
			for i := range checks {
				if checks[i].Type == "Secret" && checks[i].Name == "db-secret" {
					secretCheck = &checks[i]
					break
				}
			}
			Expect(secretCheck).NotTo(BeNil())
			Expect(secretCheck.Exists).To(BeFalse())
		})

		It("should confirm existing ConfigMap references", func() {
			objDef := map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "Pod",
				"metadata": map[string]interface{}{
					"name":      "my-pod",
					"namespace": "default",
				},
				"spec": map[string]interface{}{
					"volumes": []interface{}{
						map[string]interface{}{
							"name": "config-volume",
							"configMap": map[string]interface{}{
								"name": "app-config",
							},
						},
					},
				},
			}

			cmGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
			mockInspector.EXPECT().
				GetResource(gomock.Any(), cmGVR, "app-config", "default").
				Return(&unstructured.Unstructured{Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata":   map[string]interface{}{"name": "app-config"},
				}}, nil)

			checks := k8s.ValidateDependencies(context.Background(), mockInspector, objDef, "default")

			var cmCheck *acm.DependencyCheck
			for i := range checks {
				if checks[i].Type == "ConfigMap" && checks[i].Name == "app-config" {
					cmCheck = &checks[i]
					break
				}
			}
			Expect(cmCheck).NotTo(BeNil())
			Expect(cmCheck.Exists).To(BeTrue())
		})

		It("should handle objectDefinition with no references", func() {
			objDef := map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]interface{}{
					"name":      "simple-config",
					"namespace": "default",
				},
				"data": map[string]interface{}{
					"key": "value",
				},
			}

			checks := k8s.ValidateDependencies(context.Background(), mockInspector, objDef, "default")
			// Should only have the CRD check (which won't be added for core API groups)
			for _, check := range checks {
				Expect(check.Type).NotTo(Equal("Secret"))
				Expect(check.Type).NotTo(Equal("ConfigMap"))
			}
		})

		It("should check CRD availability for custom resources", func() {
			objDef := map[string]interface{}{
				"apiVersion": "monitoring.coreos.com/v1",
				"kind":       "ServiceMonitor",
				"metadata": map[string]interface{}{
					"name":      "my-monitor",
					"namespace": "monitoring",
				},
			}

			crdGVR := schema.GroupVersionResource{
				Group:    "apiextensions.k8s.io",
				Version:  "v1",
				Resource: "customresourcedefinitions",
			}
			mockInspector.EXPECT().
				GetResource(gomock.Any(), crdGVR, gomock.Any(), "").
				Return(nil, fmt.Errorf("not found"))

			checks := k8s.ValidateDependencies(context.Background(), mockInspector, objDef, "monitoring")

			var crdCheck *acm.DependencyCheck
			for i := range checks {
				if checks[i].Type == "CRD" {
					crdCheck = &checks[i]
					break
				}
			}
			Expect(crdCheck).NotTo(BeNil())
			Expect(crdCheck.Exists).To(BeFalse())
			Expect(crdCheck.Error).To(ContainSubstring("Custom Resource Definition not installed"))
		})
	})
})
