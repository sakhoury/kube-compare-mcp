// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("Server", func() {

	Describe("NewServer", func() {
		It("creates a valid MCP server with version", func() {
			s := mcpserver.NewServer("1.0.0")
			Expect(s).NotTo(BeNil())
		})

		It("accepts any version string", func() {
			s := mcpserver.NewServer("dev")
			Expect(s).NotTo(BeNil())
		})
	})

	Describe("ClusterCompareTool", func() {
		var tool = mcpserver.ClusterCompareTool()

		It("has the correct name", func() {
			Expect(tool.Name).To(Equal("cluster_compare"))
		})

		It("has a description", func() {
			Expect(tool.Description).NotTo(BeEmpty())
		})
	})

	Describe("FindRDSReferenceTool", func() {
		var tool = mcpserver.FindRDSReferenceTool()

		It("has the correct name", func() {
			Expect(tool.Name).To(Equal("find_rds_reference"))
		})

		It("has a description", func() {
			Expect(tool.Description).NotTo(BeEmpty())
		})
	})

	Describe("CompareClusterRDSTool", func() {
		var tool = mcpserver.CompareClusterRDSTool()

		It("has the correct name", func() {
			Expect(tool.Name).To(Equal("compare_cluster_rds"))
		})

		It("has a description", func() {
			Expect(tool.Description).NotTo(BeEmpty())
		})
	})

	Describe("Constants", func() {
		It("defines server name", func() {
			Expect(mcpserver.ServerName).To(Equal("kube-compare-mcp"))
		})

		It("defines RDS type constants", func() {
			Expect(mcpserver.RDSTypeCore).To(Equal("core"))
			Expect(mcpserver.RDSTypeRAN).To(Equal("ran"))
		})
	})

	Describe("Default implementations", func() {
		It("DefaultRegistry is not nil", func() {
			Expect(mcpserver.DefaultRegistry).NotTo(BeNil())
		})

		It("DefaultClusterFactory is not nil", func() {
			Expect(mcpserver.DefaultClusterFactory).NotTo(BeNil())
		})

		It("NewReferenceService creates a valid service", func() {
			service := mcpserver.NewReferenceService()
			Expect(service).NotTo(BeNil())
			Expect(service.Registry).NotTo(BeNil())
			Expect(service.ClusterFactory).NotTo(BeNil())
		})

		It("NewCompareService creates a valid service", func() {
			service := mcpserver.NewCompareService()
			Expect(service).NotTo(BeNil())
			Expect(service.HTTPClient).NotTo(BeNil())
			Expect(service.Registry).NotTo(BeNil())
		})
	})

	Describe("Service methods", func() {
		Context("ReferenceService", func() {
			It("exposes Registry field", func() {
				service := mcpserver.NewReferenceService()
				Expect(service.Registry).NotTo(BeNil())
			})

			It("exposes ClusterFactory field", func() {
				service := mcpserver.NewReferenceService()
				Expect(service.ClusterFactory).NotTo(BeNil())
			})
		})

		Context("CompareService", func() {
			It("exposes HTTPClient field", func() {
				service := mcpserver.NewCompareService()
				Expect(service.HTTPClient).NotTo(BeNil())
			})

			It("exposes Registry field", func() {
				service := mcpserver.NewCompareService()
				Expect(service.Registry).NotTo(BeNil())
			})
		})
	})
})
