// SPDX-License-Identifier: Apache-2.0

package k8s_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/k8s"
)

var _ = Describe("ACM Mutability Check", func() {
	var (
		ctrl          *gomock.Controller
		mockInspector *MockResourceInspector
		testGVR       schema.GroupVersionResource
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		mockInspector = NewMockResourceInspector(ctrl)
		testGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	})

	AfterEach(func() {
		ctrl.Finish()
	})

	Describe("CheckMutability", func() {
		It("should report mutable when dry-run succeeds", func() {
			patch := []byte(`{"spec":{"replicas":3}}`)
			mockInspector.EXPECT().
				DryRunPatch(gomock.Any(), testGVR, "my-deploy", "default", patch).
				Return(nil)

			result := k8s.CheckMutability(context.Background(), mockInspector, testGVR, "my-deploy", "default", patch)
			Expect(result.Mutable).To(BeTrue())
		})

		It("should detect immutable field rejection", func() {
			patch := []byte(`{"spec":{"selector":{"matchLabels":{"app":"new"}}}}`)
			mockInspector.EXPECT().
				DryRunPatch(gomock.Any(), testGVR, "my-deploy", "default", patch).
				Return(fmt.Errorf("spec.selector: Invalid value: field is immutable"))

			result := k8s.CheckMutability(context.Background(), mockInspector, testGVR, "my-deploy", "default", patch)
			Expect(result.Mutable).To(BeFalse())
			Expect(result.Reason).To(Equal("immutable_field"))
		})

		It("should detect webhook denial", func() {
			patch := []byte(`{"spec":{"replicas":3}}`)
			mockInspector.EXPECT().
				DryRunPatch(gomock.Any(), testGVR, "my-deploy", "default", patch).
				Return(fmt.Errorf(`admission webhook "validate.example.com" denied the request: replicas too high`))

			result := k8s.CheckMutability(context.Background(), mockInspector, testGVR, "my-deploy", "default", patch)
			Expect(result.Mutable).To(BeFalse())
			Expect(result.Reason).To(Equal("webhook_denied"))
		})

		It("should detect RBAC forbidden", func() {
			patch := []byte(`{"spec":{"replicas":3}}`)
			mockInspector.EXPECT().
				DryRunPatch(gomock.Any(), testGVR, "my-deploy", "default", patch).
				Return(fmt.Errorf("deployments.apps \"my-deploy\" is forbidden: User cannot patch"))

			result := k8s.CheckMutability(context.Background(), mockInspector, testGVR, "my-deploy", "default", patch)
			Expect(result.Mutable).To(BeFalse())
			Expect(result.Reason).To(Equal("rbac_denied"))
		})

		It("should detect quota exceeded", func() {
			patch := []byte(`{"spec":{"template":{"spec":{"containers":[{"resources":{"limits":{"memory":"100Gi"}}}]}}}}`)
			mockInspector.EXPECT().
				DryRunPatch(gomock.Any(), testGVR, "my-deploy", "default", patch).
				Return(fmt.Errorf("exceeded quota: compute-resources, requested: memory=100Gi"))

			result := k8s.CheckMutability(context.Background(), mockInspector, testGVR, "my-deploy", "default", patch)
			Expect(result.Mutable).To(BeFalse())
			Expect(result.Reason).To(Equal("quota_exceeded"))
		})

		It("should report mutable for empty patch", func() {
			result := k8s.CheckMutability(context.Background(), mockInspector, testGVR, "my-deploy", "default", nil)
			Expect(result.Mutable).To(BeTrue())
			Expect(result.Reason).To(Equal("no_patch_needed"))
		})
	})
})
