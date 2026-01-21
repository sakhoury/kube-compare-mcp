// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

var _ = Describe("RDSCompareHandler", func() {

	Describe("ParseRDSCompareArgs", func() {
		DescribeTable("valid arguments",
			func(args map[string]interface{}, expectedType, expectedFormat string, expectedAllRes bool) {
				result, err := mcpserver.ParseRDSCompareArgs(args)
				Expect(err).NotTo(HaveOccurred())
				Expect(result.RDSType).To(Equal(expectedType))
				Expect(result.OutputFormat).To(Equal(expectedFormat))
				Expect(result.AllResources).To(Equal(expectedAllRes))
			},
			Entry("core RDS with defaults",
				map[string]interface{}{"rds_type": "core"},
				"core", "json", false),
			Entry("ran RDS with all options",
				map[string]interface{}{
					"rds_type":      "ran",
					"output_format": "yaml",
					"all_resources": true,
				},
				"ran", "yaml", true),
			Entry("RDS type case insensitive - CORE",
				map[string]interface{}{"rds_type": "CORE"},
				"core", "json", false),
			Entry("RDS type case insensitive - RAN",
				map[string]interface{}{"rds_type": "RAN"},
				"ran", "json", false),
			Entry("junit output format",
				map[string]interface{}{
					"rds_type":      "core",
					"output_format": "junit",
				},
				"core", "junit", false),
			Entry("in-cluster mode (no kubeconfig)",
				map[string]interface{}{"rds_type": "core"},
				"core", "json", false),
		)

		DescribeTable("error cases",
			func(args map[string]interface{}, errContains string) {
				_, err := mcpserver.ParseRDSCompareArgs(args)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring(errContains))
			},
			Entry("missing rds_type",
				map[string]interface{}{},
				"rds_type"),
			Entry("invalid rds_type",
				map[string]interface{}{"rds_type": "invalid"},
				"invalid"),
			Entry("invalid output format",
				map[string]interface{}{
					"rds_type":      "core",
					"output_format": "xml",
				},
				"format"),
			Entry("empty arguments (missing rds_type)",
				map[string]interface{}{},
				"rds_type"),
			Entry("kubeconfig wrong type",
				map[string]interface{}{
					"rds_type":   "core",
					"kubeconfig": 12345,
				},
				"kubeconfig"),
			Entry("rds_type wrong type",
				map[string]interface{}{
					"rds_type": []string{"core"},
				},
				"rds_type"),
			Entry("all_resources wrong type",
				map[string]interface{}{
					"rds_type":      "core",
					"all_resources": "yes",
				},
				"all_resources"),
		)

		It("uses default output format json", func() {
			args, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
				"rds_type": "core",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(args.OutputFormat).To(Equal("json"))
		})

		It("accepts yaml format", func() {
			args, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
				"rds_type":      "core",
				"output_format": "yaml",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(args.OutputFormat).To(Equal("yaml"))
		})

		It("accepts kubeconfig and context", func() {
			args, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
				"rds_type":   "core",
				"kubeconfig": EncodeKubeconfig(ValidKubeconfig),
				"context":    "test-context",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(args.Kubeconfig).NotTo(BeEmpty())
			Expect(args.Context).To(Equal("test-context"))
		})

		Describe("kubeconfig format auto-detection", func() {
			It("accepts raw YAML kubeconfig", func() {
				args, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
					"rds_type":   "core",
					"kubeconfig": ValidKubeconfig,
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(args.Kubeconfig).NotTo(BeEmpty())
			})

			It("accepts base64-encoded kubeconfig", func() {
				args, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
					"rds_type":   "core",
					"kubeconfig": EncodeKubeconfig(ValidKubeconfig),
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(args.Kubeconfig).NotTo(BeEmpty())
			})

			It("stores base64-encoded kubeconfig internally for both formats", func() {
				rawYAMLArgs, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
					"rds_type":   "core",
					"kubeconfig": ValidKubeconfig,
				})
				Expect(err).NotTo(HaveOccurred())

				base64Args, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
					"rds_type":   "core",
					"kubeconfig": EncodeKubeconfig(ValidKubeconfig),
				})
				Expect(err).NotTo(HaveOccurred())

				Expect(rawYAMLArgs.Kubeconfig).NotTo(BeEmpty())
				Expect(base64Args.Kubeconfig).NotTo(BeEmpty())
			})

			It("sets empty kubeconfig for in-cluster mode", func() {
				args, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
					"rds_type": "core",
				})
				Expect(err).NotTo(HaveOccurred())
				Expect(args.Kubeconfig).To(BeEmpty())
			})

			It("rejects invalid kubeconfig content", func() {
				_, err := mcpserver.ParseRDSCompareArgs(map[string]interface{}{
					"rds_type":   "core",
					"kubeconfig": "this is not valid kubeconfig!!!",
				})
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("unable to parse"))
			})
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
