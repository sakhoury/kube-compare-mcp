// SPDX-License-Identifier: Apache-2.0

package acm_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

var _ = Describe("ACM Conflict Detection", func() {
	var (
		ctrl          *gomock.Controller
		mockInspector *MockResourceInspector
		mockLister    *MockPolicyLister
		testResource  *unstructured.Unstructured
		testGVR       schema.GroupVersionResource
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockInspector = NewMockResourceInspector(ctrl)
		mockLister = NewMockPolicyLister(ctrl)
		testGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
		testResource = &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name":      "frontend",
					"namespace": "production",
				},
			},
		}
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	Describe("DetectConflicts", func() {
		It("should detect matching validating webhooks", func() {
			mockInspector.EXPECT().
				ListValidatingWebhooks(gomock.Any()).
				Return([]acm.WebhookInfo{
					{
						ConfigName:  "my-validator",
						WebhookName: "validate.example.com",
						Type:        "validating",
						FailPolicy:  "Fail",
						Rules: []acm.WebhookRule{
							{
								APIGroups:   []string{"apps"},
								APIVersions: []string{"v1"},
								Resources:   []string{"deployments"},
								Operations:  []string{"CREATE", "UPDATE"},
							},
						},
					},
				}, nil)

			mockInspector.EXPECT().
				ListMutatingWebhooks(gomock.Any()).
				Return(nil, nil)

			// Gatekeeper not installed
			mockInspector.EXPECT().
				ListResources(gomock.Any(), gomock.Any(), "").
				Return(nil, fmt.Errorf("not found")).
				AnyTimes()

			// ACM policy list for cross-policy check
			mockLister.EXPECT().
				ListPolicies(gomock.Any(), "").
				Return(nil, nil)

			conflicts := acm.DetectConflicts(context.Background(), mockInspector, mockLister, testResource, testGVR)
			Expect(conflicts).NotTo(BeEmpty())

			found := false
			for _, c := range conflicts {
				if c.Type == "validating_webhook" {
					found = true
					Expect(c.Name).To(ContainSubstring("validate.example.com"))
					break
				}
			}
			Expect(found).To(BeTrue())
		})

		It("should not flag webhooks that don't match the resource", func() {
			mockInspector.EXPECT().
				ListValidatingWebhooks(gomock.Any()).
				Return([]acm.WebhookInfo{
					{
						ConfigName:  "pod-validator",
						WebhookName: "validate-pods.example.com",
						Type:        "validating",
						FailPolicy:  "Fail",
						Rules: []acm.WebhookRule{
							{
								APIGroups:   []string{""},
								APIVersions: []string{"v1"},
								Resources:   []string{"pods"},
								Operations:  []string{"CREATE"},
							},
						},
					},
				}, nil)

			mockInspector.EXPECT().
				ListMutatingWebhooks(gomock.Any()).
				Return(nil, nil)

			mockInspector.EXPECT().
				ListResources(gomock.Any(), gomock.Any(), "").
				Return(nil, fmt.Errorf("not found")).
				AnyTimes()

			mockLister.EXPECT().
				ListPolicies(gomock.Any(), "").
				Return(nil, nil)

			conflicts := acm.DetectConflicts(context.Background(), mockInspector, mockLister, testResource, testGVR)
			for _, c := range conflicts {
				Expect(c.Type).NotTo(Equal("validating_webhook"))
			}
		})

		It("should handle webhook listing errors gracefully", func() {
			mockInspector.EXPECT().
				ListValidatingWebhooks(gomock.Any()).
				Return(nil, fmt.Errorf("forbidden"))

			mockInspector.EXPECT().
				ListMutatingWebhooks(gomock.Any()).
				Return(nil, fmt.Errorf("forbidden"))

			mockInspector.EXPECT().
				ListResources(gomock.Any(), gomock.Any(), "").
				Return(nil, fmt.Errorf("not found")).
				AnyTimes()

			mockLister.EXPECT().
				ListPolicies(gomock.Any(), "").
				Return(nil, nil)

			// Should not panic, just return empty
			conflicts := acm.DetectConflicts(context.Background(), mockInspector, mockLister, testResource, testGVR)
			Expect(conflicts).To(BeEmpty())
		})

		It("should detect wildcard webhook rules", func() {
			mockInspector.EXPECT().
				ListValidatingWebhooks(gomock.Any()).
				Return([]acm.WebhookInfo{
					{
						ConfigName:  "catch-all",
						WebhookName: "catch-all.example.com",
						Type:        "validating",
						FailPolicy:  "Ignore",
						Rules: []acm.WebhookRule{
							{
								APIGroups:   []string{"*"},
								APIVersions: []string{"*"},
								Resources:   []string{"*"},
								Operations:  []string{"*"},
							},
						},
					},
				}, nil)

			mockInspector.EXPECT().
				ListMutatingWebhooks(gomock.Any()).
				Return(nil, nil)

			mockInspector.EXPECT().
				ListResources(gomock.Any(), gomock.Any(), "").
				Return(nil, fmt.Errorf("not found")).
				AnyTimes()

			mockLister.EXPECT().
				ListPolicies(gomock.Any(), "").
				Return(nil, nil)

			conflicts := acm.DetectConflicts(context.Background(), mockInspector, mockLister, testResource, testGVR)
			found := false
			for _, c := range conflicts {
				if c.Type == "validating_webhook" && c.Name == "catch-all/catch-all.example.com" {
					found = true
					break
				}
			}
			Expect(found).To(BeTrue())
		})
	})
})
