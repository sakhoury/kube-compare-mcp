// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("Schema", func() {

	Describe("ClusterCompareInputSchema", func() {
		var schema = mcpserver.ClusterCompareInputSchema()

		It("returns non-nil schema", func() {
			Expect(schema).NotTo(BeNil())
		})

		It("has output_format property with enum constraint", func() {
			prop, ok := schema.Properties["output_format"]
			Expect(ok).To(BeTrue(), "output_format property should exist")
			Expect(prop.Enum).To(ConsistOf("json", "yaml", "junit"))
		})

		It("has output_format property with default value", func() {
			prop := schema.Properties["output_format"]
			Expect(prop.Default).NotTo(BeNil())

			var defaultVal string
			err := json.Unmarshal(prop.Default, &defaultVal)
			Expect(err).NotTo(HaveOccurred())
			Expect(defaultVal).To(Equal("json"))
		})

		It("has reference property", func() {
			_, ok := schema.Properties["reference"]
			Expect(ok).To(BeTrue(), "reference property should exist")
		})
	})

	Describe("FindRDSReferenceInputSchema", func() {
		var schema = mcpserver.FindRDSReferenceInputSchema()

		It("returns non-nil schema", func() {
			Expect(schema).NotTo(BeNil())
		})

		It("has rds_type property with enum constraint", func() {
			prop, ok := schema.Properties["rds_type"]
			Expect(ok).To(BeTrue(), "rds_type property should exist")
			Expect(prop.Enum).To(ConsistOf("core", "ran"))
		})

		It("has kubeconfig property", func() {
			_, ok := schema.Properties["kubeconfig"]
			Expect(ok).To(BeTrue(), "kubeconfig property should exist")
		})

		It("has ocp_version property", func() {
			_, ok := schema.Properties["ocp_version"]
			Expect(ok).To(BeTrue(), "ocp_version property should exist")
		})
	})

	Describe("CompareClusterRDSInputSchema", func() {
		var schema = mcpserver.CompareClusterRDSInputSchema()

		It("returns non-nil schema", func() {
			Expect(schema).NotTo(BeNil())
		})

		It("has rds_type property with enum constraint", func() {
			prop, ok := schema.Properties["rds_type"]
			Expect(ok).To(BeTrue(), "rds_type property should exist")
			Expect(prop.Enum).To(ConsistOf("core", "ran"))
		})

		It("has output_format property with enum constraint", func() {
			prop, ok := schema.Properties["output_format"]
			Expect(ok).To(BeTrue(), "output_format property should exist")
			Expect(prop.Enum).To(ConsistOf("json", "yaml", "junit"))
		})

		It("has output_format property with default value", func() {
			prop := schema.Properties["output_format"]
			Expect(prop.Default).NotTo(BeNil())

			var defaultVal string
			err := json.Unmarshal(prop.Default, &defaultVal)
			Expect(err).NotTo(HaveOccurred())
			Expect(defaultVal).To(Equal("json"))
		})

		It("has kubeconfig property", func() {
			_, ok := schema.Properties["kubeconfig"]
			Expect(ok).To(BeTrue(), "kubeconfig property should exist")
		})

		It("has all_resources property", func() {
			_, ok := schema.Properties["all_resources"]
			Expect(ok).To(BeTrue(), "all_resources property should exist")
		})
	})

	Describe("Schema generation does not panic", func() {
		It("ClusterCompareInputSchema does not panic", func() {
			Expect(func() {
				_ = mcpserver.ClusterCompareInputSchema()
			}).NotTo(Panic())
		})

		It("FindRDSReferenceInputSchema does not panic", func() {
			Expect(func() {
				_ = mcpserver.FindRDSReferenceInputSchema()
			}).NotTo(Panic())
		})

		It("CompareClusterRDSInputSchema does not panic", func() {
			Expect(func() {
				_ = mcpserver.CompareClusterRDSInputSchema()
			}).NotTo(Panic())
		})
	})
})
