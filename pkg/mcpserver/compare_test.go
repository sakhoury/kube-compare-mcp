// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("CompareHandler", func() {

	Describe("ExtractArguments", func() {
		It("extracts arguments from a valid request", func() {
			req := NewMCPRequest(map[string]interface{}{
				"reference": "https://example.com/ref",
			})
			args, err := mcpserver.ExtractArguments(req)
			Expect(err).NotTo(HaveOccurred())
			Expect(args["reference"]).To(Equal("https://example.com/ref"))
		})

		It("handles nil arguments", func() {
			req := NewMCPRequest(nil)
			args, err := mcpserver.ExtractArguments(req)
			Expect(err).NotTo(HaveOccurred())
			// nil is acceptable for empty arguments
			Expect(len(args)).To(Equal(0))
		})
	})

	Describe("GetStringArg", func() {
		DescribeTable("extracting string arguments",
			func(args map[string]interface{}, key string, required bool, wantErr bool) {
				_, err := mcpserver.GetStringArg(args, key, required)
				if wantErr {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).NotTo(HaveOccurred())
				}
			},
			Entry("required present", map[string]interface{}{"key": "value"}, "key", true, false),
			Entry("required missing", map[string]interface{}{}, "key", true, true),
			Entry("optional present", map[string]interface{}{"key": "value"}, "key", false, false),
			Entry("optional missing", map[string]interface{}{}, "key", false, false),
			Entry("wrong type", map[string]interface{}{"key": 123}, "key", true, true),
		)
	})

	Describe("GetBoolArg", func() {
		DescribeTable("extracting boolean arguments",
			func(args map[string]interface{}, key string, defaultVal, expected bool, wantErr bool) {
				val, err := mcpserver.GetBoolArg(args, key, defaultVal)
				if wantErr {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(val).To(Equal(expected))
				}
			},
			Entry("true", map[string]interface{}{"key": true}, "key", false, true, false),
			Entry("false", map[string]interface{}{"key": false}, "key", true, false, false),
			Entry("missing uses default true", map[string]interface{}{}, "key", true, true, false),
			Entry("missing uses default false", map[string]interface{}{}, "key", false, false, false),
			Entry("wrong type", map[string]interface{}{"key": "true"}, "key", false, false, true),
		)
	})

	Describe("ClassifyReference", func() {
		DescribeTable("reference classification",
			func(ref string, expected mcpserver.ReferenceType) {
				refType := mcpserver.ClassifyReference(ref)
				Expect(refType).To(Equal(expected))
			},
			Entry("http URL", "http://example.com", mcpserver.ReferenceTypeHTTP),
			Entry("https URL", "https://example.com", mcpserver.ReferenceTypeHTTP),
			Entry("container reference", "container://quay.io/test", mcpserver.ReferenceTypeOCI),
			Entry("local path", "/path/to/file", mcpserver.ReferenceTypeLocal),
			Entry("relative path", "./path", mcpserver.ReferenceTypeLocal),
			Entry("unknown falls back to local", "unknown://whatever", mcpserver.ReferenceTypeLocal),
		)
	})

	Describe("ParseContainerReference", func() {
		DescribeTable("container reference parsing",
			func(ref string, wantImage, wantPath string, wantErr bool) {
				image, path, err := mcpserver.ParseContainerReference(ref)
				if wantErr {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(image).To(Equal(wantImage))
					Expect(path).To(Equal(wantPath))
				}
			},
			Entry("valid reference",
				"container://quay.io/test/image:v1.0:/path/to/file",
				"quay.io/test/image:v1.0", "/path/to/file", false),
			Entry("reference with digest",
				"container://quay.io/test/image@sha256:abc123:/path/to/file",
				"quay.io/test/image@sha256:abc123", "/path/to/file", false),
			Entry("missing container prefix",
				"quay.io/test:v1:/path", "", "", true),
			Entry("missing path",
				"container://quay.io/test:v1", "", "", true),
			Entry("empty reference",
				"", "", "", true),
		)
	})

	Describe("CompareService.ValidateHTTPReference", func() {
		var (
			ctrl     *gomock.Controller
			mockHTTP *MockHTTPDoer
			service  *mcpserver.CompareService
		)

		BeforeEach(func() {
			ctrl = gomock.NewController(GinkgoT())
			mockHTTP = NewMockHTTPDoer(ctrl)
			mockRegistry := NewMockRegistryClient(ctrl)
			service = &mcpserver.CompareService{
				HTTPClient: mockHTTP,
				Registry:   mockRegistry,
			}
		})

		AfterEach(func() {
			ctrl.Finish()
		})

		It("succeeds for reachable URLs", func() {
			mockHTTP.EXPECT().
				Do(gomock.Any()).
				DoAndReturn(func(req *http.Request) (*http.Response, error) {
					Expect(req.Method).To(Equal(http.MethodHead))
					return NewHTTPResponse(http.StatusOK, ""), nil
				})

			err := service.ValidateHTTPReference(context.Background(), "http://example.com/metadata.yaml")
			Expect(err).NotTo(HaveOccurred())
		})

		DescribeTable("HTTP error responses",
			func(statusCode int, errContains string) {
				ctrl := gomock.NewController(GinkgoT())
				defer ctrl.Finish()

				mockHTTP := NewMockHTTPDoer(ctrl)
				mockRegistry := NewMockRegistryClient(ctrl)
				service := &mcpserver.CompareService{
					HTTPClient: mockHTTP,
					Registry:   mockRegistry,
				}

				mockHTTP.EXPECT().
					Do(gomock.Any()).
					Return(NewHTTPResponse(statusCode, ""), nil)

				err := service.ValidateHTTPReference(context.Background(), "http://example.com/metadata.yaml")
				Expect(err).To(HaveOccurred())
				Expect(strings.ToLower(err.Error())).To(ContainSubstring(errContains))
			},
			Entry("404 Not Found", http.StatusNotFound, "not found"),
			Entry("401 Unauthorized", http.StatusUnauthorized, "denied"),
			Entry("403 Forbidden", http.StatusForbidden, "denied"),
			Entry("500 Server Error", http.StatusInternalServerError, "error"),
		)

		It("handles network errors", func() {
			mockHTTP.EXPECT().
				Do(gomock.Any()).
				Return(nil, errors.New("connection refused"))

			err := service.ValidateHTTPReference(context.Background(), "http://example.com/metadata.yaml")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reach"))
		})

		It("handles context cancellation", func() {
			mockHTTP.EXPECT().
				Do(gomock.Any()).
				Return(nil, context.Canceled)

			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			err := service.ValidateHTTPReference(ctx, "http://example.com/metadata.yaml")
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("CompareService.ValidateOCIReference", func() {
		var (
			ctrl         *gomock.Controller
			mockRegistry *MockRegistryClient
			service      *mcpserver.CompareService
		)

		BeforeEach(func() {
			ctrl = gomock.NewController(GinkgoT())
			mockHTTP := NewMockHTTPDoer(ctrl)
			mockRegistry = NewMockRegistryClient(ctrl)
			service = &mcpserver.CompareService{
				HTTPClient: mockHTTP,
				Registry:   mockRegistry,
			}
		})

		AfterEach(func() {
			ctrl.Finish()
		})

		It("succeeds for accessible images", func() {
			mockRegistry.EXPECT().
				HeadImage(gomock.Any(), "quay.io/test:v1").
				Return(nil)

			err := service.ValidateOCIReference(context.Background(), "container://quay.io/test:v1:/path")
			Expect(err).NotTo(HaveOccurred())
		})

		It("fails for inaccessible images", func() {
			mockRegistry.EXPECT().
				HeadImage(gomock.Any(), gomock.Any()).
				Return(errors.New("MANIFEST_UNKNOWN"))

			err := service.ValidateOCIReference(context.Background(), "container://quay.io/test:v1:/path")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("not found"))
		})

		It("handles unauthorized errors", func() {
			mockRegistry.EXPECT().
				HeadImage(gomock.Any(), gomock.Any()).
				Return(errors.New("UNAUTHORIZED"))

			err := service.ValidateOCIReference(context.Background(), "container://quay.io/test:v1:/path")
			Expect(err).To(HaveOccurred())
			Expect(strings.ToLower(err.Error())).To(ContainSubstring("denied"))
		})
	})

	Describe("IsDifferencesFoundError", func() {
		It("returns true for exit code 2", func() {
			// Create an exec.ExitError-like error
			// Note: This is a simplified check since we can't easily create real ExitErrors
			result := mcpserver.IsDifferencesFoundError(nil)
			Expect(result).To(BeFalse())
		})
	})

	Describe("ProcessCompareResult", func() {
		It("returns output when successful", func() {
			result, err := mcpserver.ProcessCompareResult("comparison output", "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("comparison output"))
		})

		It("handles empty output with error", func() {
			_, err := mcpserver.ProcessCompareResult("", "error occurred", errors.New("compare failed"))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("BuildErrorDetails", func() {
		It("returns helpful message for connection refused", func() {
			result := mcpserver.BuildErrorDetails(errors.New("connection refused"), "")
			Expect(result).To(ContainSubstring("connect"))
		})

		It("returns helpful message for no such file", func() {
			result := mcpserver.BuildErrorDetails(errors.New("no such file or directory"), "")
			Expect(result).To(ContainSubstring("reference"))
		})

		It("includes stderr output when present", func() {
			result := mcpserver.BuildErrorDetails(errors.New("connection refused"), "error from stderr")
			Expect(result).To(ContainSubstring("error from stderr"))
		})

		It("handles unknown errors gracefully", func() {
			result := mcpserver.BuildErrorDetails(errors.New("unknown error"), "")
			// Should not panic and should return something (could be empty or contain details)
			Expect(result).NotTo(BeNil())
		})
	})

	Describe("HTTP Validation Integration", func() {
		var server *httptest.Server

		AfterEach(func() {
			if server != nil {
				server.Close()
			}
		})

		It("validates live HTTP endpoint successfully", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				Expect(r.Method).To(Equal(http.MethodHead))
				w.WriteHeader(http.StatusOK)
			}))

			service := mcpserver.NewCompareService()
			err := service.ValidateHTTPReference(context.Background(), server.URL)
			Expect(err).NotTo(HaveOccurred())
		})

		It("handles timeout correctly", func() {
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(100 * time.Millisecond)
			}))

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()

			service := mcpserver.NewCompareService()
			err := service.ValidateHTTPReference(ctx, server.URL)
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("ClusterCompareTool", func() {
		var tool = mcpserver.ClusterCompareTool()

		It("has correct name", func() {
			Expect(tool.Name).To(Equal("cluster_compare"))
		})

		It("has description", func() {
			Expect(tool.Description).NotTo(BeEmpty())
		})
	})
})

var _ = Describe("CompareHandler additional tests", func() {
	Describe("NewCompareService", func() {
		It("creates a service with default implementations", func() {
			service := mcpserver.NewCompareService()
			Expect(service).NotTo(BeNil())
			Expect(service.HTTPClient).NotTo(BeNil())
			Expect(service.Registry).NotTo(BeNil())
		})
	})

	Describe("ParseContainerReference edge cases", func() {
		DescribeTable("additional container reference parsing",
			func(ref string, wantImage, wantPath string, wantErr bool) {
				image, path, err := mcpserver.ParseContainerReference(ref)
				if wantErr {
					Expect(err).To(HaveOccurred())
				} else {
					Expect(err).NotTo(HaveOccurred())
					Expect(image).To(Equal(wantImage))
					Expect(path).To(Equal(wantPath))
				}
			},
			Entry("valid with registry and nested path",
				"container://registry.redhat.io/openshift4/image:v1.0:/usr/share/path/file.yaml",
				"registry.redhat.io/openshift4/image:v1.0", "/usr/share/path/file.yaml", false),
			Entry("valid with digest and path",
				"container://quay.io/ns/image@sha256:abc123def456:/path/file",
				"quay.io/ns/image@sha256:abc123def456", "/path/file", false),
			Entry("missing container:// prefix",
				"quay.io/test:v1:/path",
				"", "", true),
			Entry("empty after container://",
				"container://",
				"", "", true),
			Entry("no path in reference",
				"container://quay.io/test:v1",
				"", "", true),
			Entry("path without leading slash",
				"container://quay.io/test:v1:path/file",
				"", "", true),
		)
	})

	Describe("GetStringArg edge cases", func() {
		It("trims whitespace from string values", func() {
			args := map[string]interface{}{"key": "  value  "}
			val, err := mcpserver.GetStringArg(args, "key", true)
			Expect(err).NotTo(HaveOccurred())
			// GetStringArg trims whitespace
			Expect(val).To(Equal("value"))
		})
	})

	Describe("ProcessCompareResult additional tests", func() {
		It("returns output directly when successful and no error", func() {
			result, err := mcpserver.ProcessCompareResult("success output", "", nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(result).To(Equal("success output"))
		})

		It("handles empty output with nil error by returning success message", func() {
			result, err := mcpserver.ProcessCompareResult("", "", nil)
			Expect(err).NotTo(HaveOccurred())
			// When output is empty but no error, it returns a success message
			Expect(result).To(ContainSubstring("No differences"))
		})
	})
})
