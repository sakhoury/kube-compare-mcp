// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("Errors", func() {

	Describe("CompareError", func() {
		It("formats error with operation and underlying error", func() {
			err := mcpserver.NewCompareError("test-op", errors.New("underlying error"), "")
			Expect(err.Error()).To(ContainSubstring("test-op"))
			Expect(err.Error()).To(ContainSubstring("underlying error"))
		})

		It("includes details when provided", func() {
			err := mcpserver.NewCompareError("test-op", errors.New("base error"), "helpful details")
			Expect(err.Error()).To(ContainSubstring("test-op"))
			Expect(err.Error()).To(ContainSubstring("base error"))
			Expect(err.Error()).To(ContainSubstring("helpful details"))
		})

		It("supports error unwrapping", func() {
			baseErr := errors.New("base error")
			err := mcpserver.NewCompareError("test-op", baseErr, "")
			Expect(errors.Unwrap(err)).To(Equal(baseErr))
		})

		It("allows checking wrapped error types", func() {
			err := mcpserver.NewCompareError("test-op", mcpserver.ErrReferenceNotFound, "")
			Expect(errors.Is(err, mcpserver.ErrReferenceNotFound)).To(BeTrue())
		})
	})

	Describe("ValidationError", func() {
		It("formats error with field and message", func() {
			err := mcpserver.NewValidationError("field_name", "is invalid", "")
			Expect(err.Error()).To(ContainSubstring("field_name"))
			Expect(err.Error()).To(ContainSubstring("is invalid"))
		})

		It("includes hint when provided", func() {
			err := mcpserver.NewValidationError("param", "missing value", "provide a value")
			Expect(err.Error()).To(ContainSubstring("param"))
			Expect(err.Error()).To(ContainSubstring("missing value"))
			Expect(err.Error()).To(ContainSubstring("provide a value"))
		})
	})

	Describe("SecurityError", func() {
		It("formats error with code and message", func() {
			err := mcpserver.NewSecurityError("sec-code", "security issue", "")
			Expect(err.Error()).To(ContainSubstring("sec-code"))
			Expect(err.Error()).To(ContainSubstring("security issue"))
		})

		It("includes hint when provided", func() {
			err := mcpserver.NewSecurityError("auth-blocked", "exec auth not allowed", "use token auth")
			Expect(err.Error()).To(ContainSubstring("auth-blocked"))
			Expect(err.Error()).To(ContainSubstring("exec auth not allowed"))
			Expect(err.Error()).To(ContainSubstring("use token auth"))
		})
	})

	Describe("formatErrorForUser", func() {
		DescribeTable("formats known error types",
			func(err error, expectedSubstring string) {
				result := mcpserver.FormatErrorForUser(err)
				Expect(result).To(ContainSubstring(expectedSubstring))
			},
			Entry("CompareError", mcpserver.NewCompareError("op", errors.New("base"), "details"), "details"),
			Entry("ValidationError", mcpserver.NewValidationError("field", "msg", "hint"), "field"),
			Entry("SecurityError", mcpserver.NewSecurityError("code", "msg", "hint"), "code"),
			Entry("ErrReferenceNotFound", mcpserver.ErrReferenceNotFound, "not found"),
			Entry("ErrLocalPathNotSupported", mcpserver.ErrLocalPathNotSupported, "not supported"),
			Entry("ErrRemoteUnreachable", mcpserver.ErrRemoteUnreachable, "unreachable"),
			Entry("ErrOCIImageNotFound", mcpserver.ErrOCIImageNotFound, "not found"),
			Entry("ErrClusterConnection", mcpserver.ErrClusterConnection, "connect"),
			Entry("ErrContextCanceled", mcpserver.ErrContextCanceled, "canceled"),
			Entry("ErrSecurityViolation", mcpserver.ErrSecurityViolation, "security"),
			Entry("ErrExecAuthBlocked", mcpserver.ErrExecAuthBlocked, "not allowed"),
			Entry("ErrAuthProviderBlocked", mcpserver.ErrAuthProviderBlocked, "not allowed"),
		)

		It("returns error message for unknown errors", func() {
			unknownErr := errors.New("some unknown error")
			result := mcpserver.FormatErrorForUser(unknownErr)
			Expect(result).To(Equal("some unknown error"))
		})

		It("returns empty string for nil error", func() {
			result := mcpserver.FormatErrorForUser(nil)
			Expect(result).To(BeEmpty())
		})
	})

	Describe("Error variables", func() {
		It("defines all expected sentinel errors", func() {
			Expect(mcpserver.ErrInvalidArguments).NotTo(BeNil())
			Expect(mcpserver.ErrReferenceNotFound).NotTo(BeNil())
			Expect(mcpserver.ErrLocalPathNotSupported).NotTo(BeNil())
			Expect(mcpserver.ErrRemoteUnreachable).NotTo(BeNil())
			Expect(mcpserver.ErrOCIImageNotFound).NotTo(BeNil())
			Expect(mcpserver.ErrClusterConnection).NotTo(BeNil())
			Expect(mcpserver.ErrComparisonFailed).NotTo(BeNil())
			Expect(mcpserver.ErrContextCanceled).NotTo(BeNil())
			Expect(mcpserver.ErrSecurityViolation).NotTo(BeNil())
			Expect(mcpserver.ErrExecAuthBlocked).NotTo(BeNil())
			Expect(mcpserver.ErrAuthProviderBlocked).NotTo(BeNil())
		})

		DescribeTable("sentinel errors have meaningful messages",
			func(err error, expectedMsg string) {
				Expect(err.Error()).To(ContainSubstring(expectedMsg))
			},
			Entry("ErrInvalidArguments", mcpserver.ErrInvalidArguments, "invalid"),
			Entry("ErrReferenceNotFound", mcpserver.ErrReferenceNotFound, "not found"),
			Entry("ErrLocalPathNotSupported", mcpserver.ErrLocalPathNotSupported, "not supported"),
			Entry("ErrRemoteUnreachable", mcpserver.ErrRemoteUnreachable, "unreachable"),
			Entry("ErrOCIImageNotFound", mcpserver.ErrOCIImageNotFound, "not found"),
			Entry("ErrClusterConnection", mcpserver.ErrClusterConnection, "connect"),
			Entry("ErrContextCanceled", mcpserver.ErrContextCanceled, "canceled"),
		)
	})

	Describe("Error message formatting", func() {
		It("CompareError includes operation in message", func() {
			err := mcpserver.NewCompareError("test-operation", errors.New("inner"), "details")
			Expect(err.Error()).To(ContainSubstring("test-operation"))
		})

		It("ValidationError includes field name", func() {
			err := mcpserver.NewValidationError("field_name", "bad value", "use good value")
			Expect(err.Error()).To(ContainSubstring("field_name"))
		})

		It("SecurityError includes code", func() {
			err := mcpserver.NewSecurityError("SEC001", "blocked", "hint")
			Expect(err.Error()).To(ContainSubstring("SEC001"))
		})
	})
})
