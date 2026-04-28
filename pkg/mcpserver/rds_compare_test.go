// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/sakhoury/kube-compare-mcp/pkg/mcpserver"
)

const testAnalysisRulesYAML = `
version: "1.0"
description: "Test Rules for MCP Integration"

settings:
  default_impact: "NeedsReview"
  default_severity: "MEDIUM"

label_annotation_rules:
  labels: []
  annotations: []
  default_impact: "NotADeviation"
  default_comment: "Labels and annotations are acceptable"

rules:
  - id: "R001-test"
    description: "Test rule"
    match:
      crName: "*"
    conditions:
      - type: "ExpectedFound"
        contains: "name:"
        impact: "NotImpacting"
        comment: "Name changes are acceptable"
`

const testComparisonJSON = `{
  "Summary": {
    "NumMissing": 0,
    "NumDiffCRs": 1,
    "TotalCRs": 5,
    "UnmatchedCRS": [],
    "MetadataHash": "abc123",
    "patchedCRs": 0
  },
  "Diffs": [
    {
      "CRName": "v1_ConfigMap_default_test",
      "CorrelatedTemplate": "test/TestCR.yaml",
      "DiffOutput": "-  name: expected\n+  name: actual",
      "description": "Test CR"
    }
  ]
}`

var _ = Describe("RDSCompareHandler", func() {

	Describe("ValidateRDSTool", func() {
		var tool = mcpserver.ValidateRDSTool()

		It("has the correct name", func() {
			Expect(tool.Name).To(Equal("kube_compare_validate_rds"))
		})

		It("has a description", func() {
			Expect(tool.Description).NotTo(BeEmpty())
		})
	})

	Describe("ValidateRDSArgs struct", func() {
		It("can be created with all fields", func() {
			args := mcpserver.ValidateRDSArgs{
				Kubeconfig:        "base64data",
				Context:           "my-context",
				RDSType:           "core",
				OutputFormat:      "yaml",
				AllResources:      true,
				RDSAnalysis:       true,
				RDSAnalysisFormat: "html",
			}
			Expect(args.Kubeconfig).To(Equal("base64data"))
			Expect(args.Context).To(Equal("my-context"))
			Expect(args.RDSType).To(Equal("core"))
			Expect(args.OutputFormat).To(Equal("yaml"))
			Expect(args.AllResources).To(BeTrue())
			Expect(args.RDSAnalysis).To(BeTrue())
			Expect(args.RDSAnalysisFormat).To(Equal("html"))
		})
	})

	Describe("ValidateRDSInputSchema", func() {
		var schema = mcpserver.ValidateRDSInputSchema()

		It("includes rds_analysis property", func() {
			_, ok := schema.Properties["rds_analysis"]
			Expect(ok).To(BeTrue())
		})

		It("includes rds_analysis_format property", func() {
			prop, ok := schema.Properties["rds_analysis_format"]
			Expect(ok).To(BeTrue())
			Expect(prop.Enum).To(ConsistOf("text", "html", "reporting"))
		})

		It("has html as default for rds_analysis_format", func() {
			prop := schema.Properties["rds_analysis_format"]
			Expect(prop.Default).To(Equal(json.RawMessage(`"html"`)))
		})
	})

	Describe("rulesKeyForRDSType", func() {
		It("returns core-rules.yaml for core type", func() {
			Expect(mcpserver.RulesKeyForRDSType("core")).To(Equal("core-rules.yaml"))
		})

		It("returns ran-rules.yaml for ran type", func() {
			Expect(mcpserver.RulesKeyForRDSType("ran")).To(Equal("ran-rules.yaml"))
		})
	})

	Describe("RunRDSAnalysis", func() {
		validFetcher := func(_ context.Context, _ string) ([]byte, error) {
			return []byte(testAnalysisRulesYAML), nil
		}

		Context("with valid rules and comparison JSON", func() {
			It("produces non-empty text output", func() {
				output, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "text", validFetcher)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).NotTo(BeEmpty())
			})

			It("produces HTML output", func() {
				output, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "html", validFetcher)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).To(ContainSubstring("<!DOCTYPE html>"))
			})

			It("produces reporting output", func() {
				output, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "reporting", validFetcher)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).NotTo(BeEmpty())
			})
		})

		Context("with invalid comparison JSON", func() {
			It("returns an error", func() {
				_, err := mcpserver.RunRDSAnalysis(context.Background(), "not valid json", "core", "4.20.0", "html", validFetcher)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to parse comparison JSON"))
			})
		})

		Context("with rules fetch failure", func() {
			It("returns an error", func() {
				failingFetcher := func(_ context.Context, _ string) ([]byte, error) {
					return nil, fmt.Errorf("configmap not found")
				}
				_, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "html", failingFetcher)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to fetch analysis rules"))
			})
		})
	})
})
