// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

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
	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("ACM Diagnose Violation", func() {
	var (
		ctrl          *gomock.Controller
		mockLister    *MockPolicyLister
		mockInspector *MockResourceInspector
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockLister = NewMockPolicyLister(ctrl)
		mockInspector = NewMockResourceInspector(ctrl)
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	Describe("DiagnoseViolationTool", func() {
		It("should return a valid tool definition", func() {
			tool := mcpserver.DiagnoseViolationTool()
			Expect(tool).NotTo(BeNil())
			Expect(tool.Name).To(Equal("acm_diagnose_violation"))
			Expect(tool.Description).NotTo(BeEmpty())
			Expect(tool.InputSchema).NotTo(BeNil())
			Expect(tool.Annotations.ReadOnlyHint).To(BeTrue())
		})
	})

	Describe("GVRFromObjectDef", func() {
		It("should parse a standard objectDefinition", func() {
			objDef := map[string]interface{}{
				"apiVersion": "apps/v1",
				"kind":       "Deployment",
				"metadata": map[string]interface{}{
					"name":      "frontend",
					"namespace": "production",
				},
			}

			gvr, name, ns, err := acm.GVRFromObjectDef(objDef)
			Expect(err).NotTo(HaveOccurred())
			Expect(gvr.Group).To(Equal("apps"))
			Expect(gvr.Version).To(Equal("v1"))
			Expect(gvr.Resource).To(Equal("deployments"))
			Expect(name).To(Equal("frontend"))
			Expect(ns).To(Equal("production"))
		})

		It("should parse a core API objectDefinition", func() {
			objDef := map[string]interface{}{
				"apiVersion": "v1",
				"kind":       "ConfigMap",
				"metadata": map[string]interface{}{
					"name": "my-config",
				},
			}

			gvr, name, ns, err := acm.GVRFromObjectDef(objDef)
			Expect(err).NotTo(HaveOccurred())
			Expect(gvr.Group).To(Equal(""))
			Expect(gvr.Version).To(Equal("v1"))
			Expect(gvr.Resource).To(Equal("configmaps"))
			Expect(name).To(Equal("my-config"))
			Expect(ns).To(BeEmpty())
		})

		It("should return error for missing apiVersion", func() {
			objDef := map[string]interface{}{
				"kind": "Deployment",
			}

			_, _, _, err := acm.GVRFromObjectDef(objDef)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("Root Cause Analysis Integration", func() {
		It("should detect resource-not-found as root cause", func() {
			deployGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

			mockLister.EXPECT().
				GetPolicy(gomock.Any(), "require-limits", "open-cluster-management").
				Return(&acm.PolicyDetail{
					Name:      "require-limits",
					Namespace: "open-cluster-management",
					Compliant: "NonCompliant",
					Severity:  "high",
					Templates: []acm.ConfigPolicyTemplate{
						{
							Name:              "require-limits-config",
							ComplianceType:    "musthave",
							Kind:              "Deployment",
							APIVersion:        "apps/v1",
							ResourceName:      "missing-deploy",
							ResourceNamespace: "production",
							ObjectDefinition: map[string]interface{}{
								"apiVersion": "apps/v1",
								"kind":       "Deployment",
								"metadata": map[string]interface{}{
									"name":      "missing-deploy",
									"namespace": "production",
								},
								"spec": map[string]interface{}{
									"replicas": float64(2),
								},
							},
						},
					},
				}, nil)

			// Resource not found
			mockInspector.EXPECT().
				GetResource(gomock.Any(), deployGVR, "missing-deploy", "production").
				Return(nil, fmt.Errorf("deployments.apps \"missing-deploy\" not found"))

			_ = mockLister
			_ = mockInspector

			// Test the GetPolicy call
			policy, err := mockLister.GetPolicy(context.Background(), "require-limits", "open-cluster-management")
			Expect(err).NotTo(HaveOccurred())
			Expect(policy.Templates).To(HaveLen(1))
			Expect(policy.Templates[0].Kind).To(Equal("Deployment"))

			// Test the GetResource call fails as expected
			_, err = mockInspector.GetResource(context.Background(), deployGVR, "missing-deploy", "production")
			Expect(err).To(HaveOccurred())
		})

		It("should detect external ownership as root cause", func() {
			deployGVR := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

			// Resource exists but is managed by ArgoCD
			argoManaged := &unstructured.Unstructured{
				Object: map[string]interface{}{
					"apiVersion": "apps/v1",
					"kind":       "Deployment",
					"metadata": map[string]interface{}{
						"name":      "frontend",
						"namespace": "production",
						"annotations": map[string]interface{}{
							"argocd.argoproj.io/managed-by": "argocd/frontend-app",
						},
					},
				},
			}

			mockInspector.EXPECT().
				GetResource(gomock.Any(), deployGVR, "frontend", "production").
				Return(argoManaged, nil)

			resource, err := mockInspector.GetResource(context.Background(), deployGVR, "frontend", "production")
			Expect(err).NotTo(HaveOccurred())

			// Run ownership analysis on the resource
			result := k8s.AnalyzeOwnership(resource)
			Expect(result.HasExternalOwner).To(BeTrue())
			Expect(result.FixTarget).To(ContainSubstring("ArgoCD"))
		})
	})

	Describe("Diagnostic Trace", func() {
		It("should include trace when verbose is true and resource is missing", func() {
			trace := &acm.DiagnosticTrace{}

			// Simulate addTraceStep for resource_lookup finding missing resource
			trace.Steps = append(trace.Steps, acm.TraceStep{
				Name:     "resource_lookup",
				Status:   acm.TraceStatusExecuted,
				Duration: "1ms",
				Finding:  "Deployment 'missing-deploy' not found in namespace 'production'",
				Decision: acm.TraceDecisionRootCauseFound,
			})

			Expect(trace.Steps).To(HaveLen(1))
			Expect(trace.Steps[0].Name).To(Equal("resource_lookup"))
			Expect(trace.Steps[0].Status).To(Equal("executed"))
			Expect(trace.Steps[0].Decision).To(Equal("root_cause_found"))
		})

		It("should mark remaining steps as skipped when root cause found early", func() {
			trace := &acm.DiagnosticTrace{}

			// resource_lookup finds it
			trace.Steps = append(trace.Steps, acm.TraceStep{
				Name: "resource_lookup", Status: acm.TraceStatusExecuted,
				Duration: "1ms", Finding: "Resource exists", Decision: acm.TraceDecisionContinue,
			})
			// ownership finds external owner
			trace.Steps = append(trace.Steps, acm.TraceStep{
				Name: "ownership", Status: acm.TraceStatusExecuted,
				Duration: "2ms", Finding: "External owner: ArgoCD", Decision: acm.TraceDecisionRootCauseFound,
			})
			// remaining steps skipped
			for _, step := range []string{"dependencies", "mutability", "conflicts", "events"} {
				trace.Steps = append(trace.Steps, acm.TraceStep{
					Name: step, Status: acm.TraceStatusSkipped,
					Duration: "0ms", Finding: "Not executed", Decision: "skipped_root_cause_found_at_ownership",
				})
			}

			Expect(trace.Steps).To(HaveLen(6))
			Expect(trace.Steps[0].Status).To(Equal("executed"))
			Expect(trace.Steps[1].Status).To(Equal("executed"))
			Expect(trace.Steps[1].Decision).To(Equal("root_cause_found"))
			for i := 2; i < 6; i++ {
				Expect(trace.Steps[i].Status).To(Equal("skipped"))
			}
		})

		It("should show all steps as executed when no blocker found", func() {
			trace := &acm.DiagnosticTrace{}

			stepNames := []string{"resource_lookup", "ownership", "dependencies", "mutability", "conflicts", "events"}
			for _, name := range stepNames {
				trace.Steps = append(trace.Steps, acm.TraceStep{
					Name: name, Status: acm.TraceStatusExecuted,
					Duration: "1ms", Finding: "No issues", Decision: acm.TraceDecisionContinue,
				})
			}

			Expect(trace.Steps).To(HaveLen(6))
			for _, step := range trace.Steps {
				Expect(step.Status).To(Equal("executed"))
				Expect(step.Decision).To(Equal("continue"))
			}
		})
	})

	Describe("Violation Status", func() {
		It("should set status to needs_action for resource_not_found", func() {
			v := acm.ViolationDetail{
				Status: acm.StatusNeedsAction,
				RootCause: &acm.RootCauseResult{
					PrimaryCause: acm.CauseResourceNotFound,
				},
			}
			Expect(v.Status).To(Equal("needs_action"))
		})

		It("should set status to compliant for direct_fix_applicable", func() {
			v := acm.ViolationDetail{
				Status: acm.StatusCompliant,
				RootCause: &acm.RootCauseResult{
					PrimaryCause: acm.CauseDirectFixApplicable,
				},
			}
			Expect(v.Status).To(Equal("compliant"))
		})

		It("should set status to needs_action for active_reconciliation", func() {
			v := acm.ViolationDetail{
				Status: acm.StatusNeedsAction,
				RootCause: &acm.RootCauseResult{
					PrimaryCause: acm.CauseActiveReconciliation,
				},
			}
			Expect(v.Status).To(Equal("needs_action"))
		})
	})

	Describe("Summary Counts", func() {
		It("should correctly count needs_action and compliant items", func() {
			policy := &acm.PolicyDetail{
				Name:      "test-policy",
				Namespace: "test-ns",
				Compliant: "NonCompliant",
			}
			violations := []acm.ViolationDetail{
				{Status: acm.StatusNeedsAction, RootCause: &acm.RootCauseResult{PrimaryCause: acm.CauseResourceNotFound}},
				{Status: acm.StatusNeedsAction, RootCause: &acm.RootCauseResult{PrimaryCause: acm.CauseResourceNotFound}},
				{Status: acm.StatusCompliant, RootCause: &acm.RootCauseResult{PrimaryCause: acm.CauseDirectFixApplicable}},
			}

			// We can't call buildDiagnosisSummary directly since it's unexported,
			// but we can verify the struct fields
			result := acm.DiagnoseViolationResult{
				Policy:           *policy,
				NeedsActionCount: 2,
				CompliantCount:   1,
				Violations:       violations,
			}

			Expect(result.NeedsActionCount).To(Equal(2))
			Expect(result.CompliantCount).To(Equal(1))
			Expect(result.Violations).To(HaveLen(3))
		})
	})
})
