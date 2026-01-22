// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("RDSCompareHandler", func() {

	Describe("CompareClusterRDSTool", func() {
		var tool = mcpserver.CompareClusterRDSTool()

		It("has the correct name", func() {
			Expect(tool.Name).To(Equal("compare_cluster_rds"))
		})

		It("has a description", func() {
			Expect(tool.Description).NotTo(BeEmpty())
		})
	})

	Describe("RDSCompareArgs struct", func() {
		It("can be created with all fields", func() {
			args := mcpserver.RDSCompareArgs{
				Kubeconfig:   "base64data",
				Context:      "my-context",
				RDSType:      "core",
				OutputFormat: "yaml",
				AllResources: true,
			}
			Expect(args.Kubeconfig).To(Equal("base64data"))
			Expect(args.Context).To(Equal("my-context"))
			Expect(args.RDSType).To(Equal("core"))
			Expect(args.OutputFormat).To(Equal("yaml"))
			Expect(args.AllResources).To(BeTrue())
		})
	})
})
