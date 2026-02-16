// SPDX-License-Identifier: Apache-2.0

package k8s_test

import (
	"context"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
	"github.com/sakhoury/kube-compare-mcp/pkg/k8s"
)

var _ = Describe("ACM Event Analysis", func() {
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

	Describe("AnalyzeEvents", func() {
		It("should return sorted events with warnings first", func() {
			mockInspector.EXPECT().
				ListEvents(gomock.Any(), "my-deploy", "default", "Deployment").
				Return([]acm.EventInfo{
					{Reason: "ScaleUp", Message: "Scaled up", Type: "Normal", LastSeen: "2025-01-01T10:00:00Z", Count: 1},
					{Reason: "FailedMount", Message: "Unable to mount volume", Type: "Warning", LastSeen: "2025-01-01T11:00:00Z", Count: 3},
					{Reason: "Pulled", Message: "Image pulled", Type: "Normal", LastSeen: "2025-01-01T09:00:00Z", Count: 1},
				}, nil)

			events := k8s.AnalyzeEvents(context.Background(), mockInspector, "my-deploy", "default", "Deployment")
			Expect(events).To(HaveLen(3))
			// Warnings should come first
			Expect(events[0].Type).To(Equal("Warning"))
			Expect(events[0].Reason).To(Equal("FailedMount"))
		})

		It("should return nil when no events found", func() {
			mockInspector.EXPECT().
				ListEvents(gomock.Any(), "my-deploy", "default", "Deployment").
				Return([]acm.EventInfo{}, nil)

			events := k8s.AnalyzeEvents(context.Background(), mockInspector, "my-deploy", "default", "Deployment")
			Expect(events).To(BeNil())
		})

		It("should handle event listing errors gracefully", func() {
			mockInspector.EXPECT().
				ListEvents(gomock.Any(), "my-deploy", "default", "Deployment").
				Return(nil, fmt.Errorf("connection refused"))

			events := k8s.AnalyzeEvents(context.Background(), mockInspector, "my-deploy", "default", "Deployment")
			Expect(events).To(BeNil())
		})

		It("should limit events to max 20", func() {
			var manyEvents []acm.EventInfo
			for i := 0; i < 30; i++ {
				manyEvents = append(manyEvents, acm.EventInfo{
					Reason:   fmt.Sprintf("Event-%d", i),
					Message:  fmt.Sprintf("Event message %d", i),
					Type:     "Normal",
					LastSeen: fmt.Sprintf("2025-01-01T%02d:00:00Z", i%24),
					Count:    1,
				})
			}

			mockInspector.EXPECT().
				ListEvents(gomock.Any(), "my-deploy", "default", "Deployment").
				Return(manyEvents, nil)

			events := k8s.AnalyzeEvents(context.Background(), mockInspector, "my-deploy", "default", "Deployment")
			Expect(len(events)).To(BeNumerically("<=", 20))
		})
	})
})
