// SPDX-License-Identifier: Apache-2.0

package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
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

		It("returns hub-rules.yaml for hub type", func() {
			Expect(mcpserver.RulesKeyForRDSType("hub")).To(Equal("hub-rules.yaml"))
		})
	})

	Describe("RunRDSAnalysis", func() {
		validFetcher := func(_ context.Context, _ string) ([]byte, error) {
			return []byte(testAnalysisRulesYAML), nil
		}

		Context("with valid rules and comparison JSON", func() {
			It("produces text output containing the diff CR name and impact classification", func() {
				output, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "text", validFetcher)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).To(ContainSubstring("v1_ConfigMap_default_test"))
				Expect(output).To(ContainSubstring("R001-test"))
			})

			It("produces HTML output containing the diff CR name", func() {
				output, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "html", validFetcher)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).To(ContainSubstring("<!DOCTYPE html>"))
				Expect(output).To(ContainSubstring("v1_ConfigMap_default_test"))
			})

			It("produces reporting output containing the diff CR name", func() {
				output, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "reporting", validFetcher)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).To(ContainSubstring("v1_ConfigMap_default_test"))
			})
		})

		Context("with unrecognized format", func() {
			It("falls back to HTML for empty format", func() {
				output, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "", validFetcher)
				Expect(err).NotTo(HaveOccurred())
				Expect(output).To(ContainSubstring("<!DOCTYPE html>"))
			})
		})

		Context("with empty cluster version", func() {
			It("returns an error", func() {
				_, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "", "html", validFetcher)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("cluster version is required"))
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
					return nil, errors.New("configmap not found")
				}
				_, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "html", failingFetcher)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to fetch analysis rules"))
			})
		})

		Context("with malformed rules YAML", func() {
			It("returns an error", func() {
				badFetcher := func(_ context.Context, _ string) ([]byte, error) {
					return []byte("not: [valid: yaml"), nil
				}
				_, err := mcpserver.RunRDSAnalysis(context.Background(), testComparisonJSON, "core", "4.20.0", "html", badFetcher)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("failed to initialize analyzer"))
			})
		})
	})

	Describe("AnalysisService dependency injection", func() {
		It("allows injecting a custom RulesFetcher", func() {
			service := &mcpserver.AnalysisService{
				FetchRules: func(_ context.Context, _ string) ([]byte, error) {
					return []byte(testAnalysisRulesYAML), nil
				},
			}
			Expect(service.FetchRules).NotTo(BeNil())

			// Verify the injected fetcher works through RunRDSAnalysis
			output, err := mcpserver.RunRDSAnalysis(
				context.Background(), testComparisonJSON, "core", "4.20.0", "text", service.FetchRules,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).NotTo(BeEmpty())
		})

		It("allows injecting a failing RulesFetcher for error path testing", func() {
			service := &mcpserver.AnalysisService{
				FetchRules: func(_ context.Context, _ string) ([]byte, error) {
					return nil, errors.New("simulated configmap not found")
				},
			}

			_, err := mcpserver.RunRDSAnalysis(
				context.Background(), testComparisonJSON, "core", "4.20.0", "html", service.FetchRules,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("failed to fetch analysis rules"))
		})

		It("default service uses production fetcher", func() {
			service := mcpserver.NewAnalysisService()
			Expect(service.FetchRules).NotTo(BeNil())
		})
	})

	Describe("ValidateRDSResult error field separation", func() {
		It("populates rds_analysis on success and leaves rds_analysis_error empty", func() {
			result := mcpserver.ValidateRDSResult{
				Comparison:  json.RawMessage(`{}`),
				RDSAnalysis: "<html>analysis output</html>",
			}
			jsonBytes, err := json.Marshal(result)
			Expect(err).NotTo(HaveOccurred())

			var parsed map[string]any
			Expect(json.Unmarshal(jsonBytes, &parsed)).To(Succeed())

			Expect(parsed).To(HaveKey("rds_analysis"))
			Expect(parsed["rds_analysis"]).To(ContainSubstring("analysis output"))
			Expect(parsed).NotTo(HaveKey("rds_analysis_error"))
		})

		It("populates rds_analysis_error on failure and leaves rds_analysis empty", func() {
			result := mcpserver.ValidateRDSResult{
				Comparison:       json.RawMessage(`{}`),
				RDSAnalysisError: "RDS deviation analysis could not be completed.",
			}
			jsonBytes, err := json.Marshal(result)
			Expect(err).NotTo(HaveOccurred())

			var parsed map[string]any
			Expect(json.Unmarshal(jsonBytes, &parsed)).To(Succeed())

			Expect(parsed).To(HaveKey("rds_analysis_error"))
			Expect(parsed["rds_analysis_error"]).To(ContainSubstring("could not be completed"))
			Expect(parsed).NotTo(HaveKey("rds_analysis"))
		})

		It("uses formatErrorForUser for analysis errors via ErrRDSAnalysisFailed", func() {
			wrappedErr := fmt.Errorf("%w: %w", mcpserver.ErrRDSAnalysisFailed, errors.New("configmap not found"))
			formatted := mcpserver.FormatErrorForUser(wrappedErr)
			Expect(formatted).To(ContainSubstring("could not be completed"))
			Expect(formatted).To(ContainSubstring("in-cluster"))
			Expect(formatted).NotTo(ContainSubstring("configmap not found"))
		})
	})
})
