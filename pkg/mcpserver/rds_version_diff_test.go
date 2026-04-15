// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("RDS version diff", func() {
	Describe("HandleRDSVersionDiff", func() {
		It("returns error when old_version_url is missing", func() {
			ctx := context.Background()
			req := NewMCPRequest(nil)
			input := mcpserver.RDSVersionDiffInput{
				NewVersionURL: "https://github.com/openshift-kni/telco-reference/tree/konflux-telco-core-rds-4-20/telco-ran/configuration",
			}
			result, _, err := mcpserver.HandleRDSVersionDiff(ctx, req, input)
			Expect(err).To(BeNil())
			Expect(result).NotTo(BeNil())
			Expect(result.IsError).To(BeTrue())
			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(textContent.Text).To(ContainSubstring("old_version_url"))
		})
		It("returns error when new_version_url is missing", func() {
			ctx := context.Background()
			req := NewMCPRequest(nil)
			input := mcpserver.RDSVersionDiffInput{
				OldVersionURL: "https://github.com/openshift-kni/telco-reference/tree/konflux-telco-core-rds-4-18/telco-ran/configuration",
			}
			result, _, err := mcpserver.HandleRDSVersionDiff(ctx, req, input)
			Expect(err).To(BeNil())
			Expect(result).NotTo(BeNil())
			Expect(result.IsError).To(BeTrue())
			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(textContent.Text).To(ContainSubstring("new_version_url"))
		})
		It("returns error when old_version_url has unsupported scheme", func() {
			ctx := context.Background()
			req := NewMCPRequest(nil)
			input := mcpserver.RDSVersionDiffInput{
				OldVersionURL: "ftp://example.com/archive.zip",
				NewVersionURL: "https://github.com/openshift-kni/telco-reference/tree/konflux-telco-core-rds-4-20/telco-ran/configuration",
			}
			result, _, err := mcpserver.HandleRDSVersionDiff(ctx, req, input)
			Expect(err).To(BeNil())
			Expect(result).NotTo(BeNil())
			Expect(result.IsError).To(BeTrue())
			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(textContent.Text).To(ContainSubstring("old_version_url"))
		})
		It("returns error when new_version_url is invalid", func() {
			ctx := context.Background()
			req := NewMCPRequest(nil)
			input := mcpserver.RDSVersionDiffInput{
				OldVersionURL: "https://github.com/openshift-kni/telco-reference/tree/konflux-telco-core-rds-4-18/telco-ran/configuration",
				NewVersionURL: "not-a-url",
			}
			result, _, err := mcpserver.HandleRDSVersionDiff(ctx, req, input)
			Expect(err).To(BeNil())
			Expect(result).NotTo(BeNil())
			Expect(result.IsError).To(BeTrue())
			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(textContent.Text).To(ContainSubstring("new_version_url"))
		})
	})
})
