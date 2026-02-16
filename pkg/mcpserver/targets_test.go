// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("Target Store", func() {
	var store *mcpserver.TargetStore

	BeforeEach(func() {
		store = mcpserver.NewTargetStore()
	})

	Describe("Add", func() {
		It("should add a target and return the key", func() {
			key := store.Add("my-secret", "my-namespace")
			Expect(key).To(Equal("my-secret/my-namespace"))
		})

		It("should overwrite an existing target with the same key", func() {
			store.Add("my-secret", "my-namespace")
			store.Add("my-secret", "my-namespace")
			targets := store.List()
			Expect(targets).To(HaveLen(1))
		})
	})

	Describe("Remove", func() {
		It("should remove an existing target and return true", func() {
			store.Add("my-secret", "my-namespace")
			existed := store.Remove("my-secret/my-namespace")
			Expect(existed).To(BeTrue())
			Expect(store.List()).To(BeEmpty())
		})

		It("should return false when removing a non-existent target", func() {
			existed := store.Remove("nonexistent/ns")
			Expect(existed).To(BeFalse())
		})
	})

	Describe("List", func() {
		It("should return empty list when no targets registered", func() {
			targets := store.List()
			Expect(targets).To(BeEmpty())
		})

		It("should return all targets sorted by key", func() {
			store.Add("z-secret", "ns1")
			store.Add("a-secret", "ns2")
			store.Add("m-secret", "ns3")

			targets := store.List()
			Expect(targets).To(HaveLen(3))
			Expect(targets[0].Key).To(Equal("a-secret/ns2"))
			Expect(targets[1].Key).To(Equal("m-secret/ns3"))
			Expect(targets[2].Key).To(Equal("z-secret/ns1"))
		})
	})

	Describe("Get", func() {
		It("should return the target for a valid key", func() {
			store.Add("my-secret", "my-namespace")
			target, ok := store.Get("my-secret/my-namespace")
			Expect(ok).To(BeTrue())
			Expect(target.SecretName).To(Equal("my-secret"))
			Expect(target.Namespace).To(Equal("my-namespace"))
			Expect(target.Key).To(Equal("my-secret/my-namespace"))
		})

		It("should return false for a non-existent key", func() {
			_, ok := store.Get("nonexistent/ns")
			Expect(ok).To(BeFalse())
		})
	})

	Describe("AddWithConfig", func() {
		It("should add a target with in-memory config", func() {
			key := store.AddWithConfig("cnfdf04", nil, "discovered:hub1/david")
			Expect(key).To(Equal("cnfdf04"))

			target, ok := store.Get("cnfdf04")
			Expect(ok).To(BeTrue())
			Expect(target.Key).To(Equal("cnfdf04"))
			Expect(target.Source).To(Equal("discovered:hub1/david"))
		})

		It("should overwrite an existing target", func() {
			store.AddWithConfig("cnfdf04", nil, "discovered:hub1/david")
			store.AddWithConfig("cnfdf04", nil, "discovered:hub2/david")
			targets := store.List()
			Expect(targets).To(HaveLen(1))
			Expect(targets[0].Source).To(Equal("discovered:hub2/david"))
		})
	})

	Describe("IsTargetRef", func() {
		It("should return true for a registered key", func() {
			store.Add("my-secret", "my-namespace")
			Expect(store.IsTargetRef("my-secret/my-namespace")).To(BeTrue())
		})

		It("should return false for an unregistered key", func() {
			Expect(store.IsTargetRef("nonexistent/ns")).To(BeFalse())
		})

		It("should return false for arbitrary kubeconfig text", func() {
			Expect(store.IsTargetRef("apiVersion: v1\nkind: Config")).To(BeFalse())
		})

		It("should return true for a discovered cluster key", func() {
			store.AddWithConfig("cnfdf04", nil, "discovered:hub1/david")
			Expect(store.IsTargetRef("cnfdf04")).To(BeTrue())
		})
	})
})

var _ = Describe("Manage Targets Tool", func() {
	Describe("HandleManageTargets", func() {
		// Note: The add action requires in-cluster config for secret validation,
		// so we test list and remove actions (and add with validation failure).

		It("should return empty list initially", func() {
			input := mcpserver.ManageTargetsInput{Action: "list"}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeFalse())

			// Parse the JSON output
			var output struct {
				Count   int                    `json:"count"`
				Targets []mcpserver.TargetInfo `json:"targets"`
			}
			tc, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(json.Unmarshal([]byte(tc.Text), &output)).To(Succeed())
			Expect(output.Count).To(Equal(0))
			Expect(output.Targets).To(BeEmpty())
		})

		It("should reject unknown action", func() {
			input := mcpserver.ManageTargetsInput{Action: "invalid"}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeTrue())
		})

		It("should require target for add", func() {
			input := mcpserver.ManageTargetsInput{Action: "add"}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeTrue())
		})

		It("should reject invalid target format for add", func() {
			input := mcpserver.ManageTargetsInput{Action: "add", Target: "no-slash"}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeTrue())
		})

		It("should require target for remove", func() {
			input := mcpserver.ManageTargetsInput{Action: "remove"}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeTrue())
		})

		It("should handle remove of non-existent target", func() {
			input := mcpserver.ManageTargetsInput{
				Action: "remove",
				Target: "nonexistent/ns",
			}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeFalse())

			var output struct {
				Status string `json:"status"`
				Key    string `json:"key"`
			}
			tc, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(json.Unmarshal([]byte(tc.Text), &output)).To(Succeed())
			Expect(output.Status).To(Equal("not_found"))
			Expect(output.Key).To(Equal("nonexistent/ns"))
		})

		It("should fail add when in-cluster config is not available", func() {
			input := mcpserver.ManageTargetsInput{
				Action: "add",
				Target: "my-secret/my-namespace",
			}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			// Should fail because we're not running in-cluster
			Expect(result.IsError).To(BeTrue())
		})

		It("should require target for discover", func() {
			input := mcpserver.ManageTargetsInput{Action: "discover"}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeTrue())
		})

		It("should fail discover when hub kubeconfig cannot be resolved", func() {
			input := mcpserver.ManageTargetsInput{
				Action: "discover",
				Target: "nonexistent/ns",
			}
			result, _, err := mcpserver.HandleManageTargets(context.Background(), &mcp.CallToolRequest{}, input)
			Expect(err).NotTo(HaveOccurred())
			// Should fail because the target is not registered and in-cluster is unavailable
			Expect(result.IsError).To(BeTrue())
		})
	})

	Describe("ManageTargetsTool", func() {
		It("should return a valid tool definition", func() {
			tool := mcpserver.ManageTargetsTool()
			Expect(tool.Name).To(Equal("manage_targets"))
			Expect(tool.Description).To(ContainSubstring("target"))
			Expect(tool.Annotations.ReadOnlyHint).To(BeFalse())
		})
	})
})
