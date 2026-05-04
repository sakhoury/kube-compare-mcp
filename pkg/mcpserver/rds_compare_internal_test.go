// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// testRulesYAML is a minimal rules file that produces deterministic analysis output.
const testRulesYAML = `
version: "1.0"
description: "Test Rules for HandleValidateRDS integration"

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

// testComparisonOutput is a valid kube-compare JSON report used by the mock runCompareFunc.
const testComparisonOutput = `{
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

var _ = Describe("HandleValidateRDS integration", func() {
	var (
		origResolveRDS      func(ctx context.Context, args *ResolveRDSArgs) (*ResolveRDSResult, error)
		origValidateRef     func(ctx context.Context, args *CompareArgs) error
		origRunCompare      func(ctx context.Context, args *CompareArgs) (string, error)
		origFetchRules      RulesFetcher
	)

	BeforeEach(func() {
		// Save originals
		origResolveRDS = resolveRDSFunc
		origValidateRef = validateReferenceFunc
		origRunCompare = runCompareFunc
		origFetchRules = defaultAnalysisService.FetchRules

		// Install default fakes: resolve returns a valid result, validate is a no-op,
		// and compare returns valid JSON.
		resolveRDSFunc = func(_ context.Context, _ *ResolveRDSArgs) (*ResolveRDSResult, error) {
			return &ResolveRDSResult{
				ClusterVersion: "4.20.0",
				RHELVersion:    "9.4",
				RDSType:        "core",
				Reference:      "https://example.com/metadata.yaml",
				Validated:      true,
			}, nil
		}
		validateReferenceFunc = func(_ context.Context, _ *CompareArgs) error {
			return nil
		}
		runCompareFunc = func(_ context.Context, _ *CompareArgs) (string, error) {
			return testComparisonOutput, nil
		}
	})

	AfterEach(func() {
		// Restore originals
		resolveRDSFunc = origResolveRDS
		validateReferenceFunc = origValidateRef
		runCompareFunc = origRunCompare
		defaultAnalysisService.FetchRules = origFetchRules
	})

	Context("with RDS analysis enabled and successful rules fetch", func() {
		BeforeEach(func() {
			defaultAnalysisService.FetchRules = func(_ context.Context, _ string) ([]byte, error) {
				return []byte(testRulesYAML), nil
			}
		})

		It("includes rds_analysis in the response", func() {
			input := ValidateRDSInput{
				RDSType:     "core",
				RDSAnalysis: true,
			}
			result, _, err := HandleValidateRDS(context.Background(), nil, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeFalse())

			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())

			var parsed ValidateRDSResult
			Expect(json.Unmarshal([]byte(textContent.Text), &parsed)).To(Succeed())

			Expect(parsed.RDSAnalysis).NotTo(BeEmpty())
			Expect(parsed.RDSAnalysisError).To(BeEmpty())
			Expect(parsed.Comparison).NotTo(BeEmpty())
			Expect(parsed.RDSReference).NotTo(BeNil())
			Expect(parsed.RDSReference.ClusterVersion).To(Equal("4.20.0"))
		})

		It("defaults analysis format to html when not specified", func() {
			input := ValidateRDSInput{
				RDSType:     "core",
				RDSAnalysis: true,
				// RDSAnalysisFormat left empty -- should default to "html"
			}
			result, _, err := HandleValidateRDS(context.Background(), nil, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeFalse())

			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())

			var parsed ValidateRDSResult
			Expect(json.Unmarshal([]byte(textContent.Text), &parsed)).To(Succeed())

			Expect(parsed.RDSAnalysis).To(ContainSubstring("<!DOCTYPE html>"))
		})

		It("respects explicit analysis format", func() {
			input := ValidateRDSInput{
				RDSType:           "core",
				RDSAnalysis:       true,
				RDSAnalysisFormat: "text",
			}
			result, _, err := HandleValidateRDS(context.Background(), nil, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeFalse())

			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())

			var parsed ValidateRDSResult
			Expect(json.Unmarshal([]byte(textContent.Text), &parsed)).To(Succeed())

			Expect(parsed.RDSAnalysis).NotTo(BeEmpty())
			Expect(parsed.RDSAnalysis).NotTo(ContainSubstring("<!DOCTYPE html>"))
		})
	})

	Context("with RDS analysis enabled but rules fetch fails (non-fatal)", func() {
		BeforeEach(func() {
			defaultAnalysisService.FetchRules = func(_ context.Context, _ string) ([]byte, error) {
				return nil, errors.New("configmap not found")
			}
		})

		It("returns comparison results with analysis error, not a tool error", func() {
			input := ValidateRDSInput{
				RDSType:     "core",
				RDSAnalysis: true,
			}
			result, _, err := HandleValidateRDS(context.Background(), nil, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeFalse(), "analysis failure should be non-fatal")

			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())

			var parsed ValidateRDSResult
			Expect(json.Unmarshal([]byte(textContent.Text), &parsed)).To(Succeed())

			// Comparison output should still be present
			Expect(parsed.Comparison).NotTo(BeEmpty())
			Expect(parsed.RDSReference).NotTo(BeNil())

			// Analysis error should be populated, analysis output should be empty
			Expect(parsed.RDSAnalysisError).NotTo(BeEmpty())
			Expect(parsed.RDSAnalysisError).To(ContainSubstring("could not be completed"))
			Expect(parsed.RDSAnalysis).To(BeEmpty())
		})
	})

	Context("with RDS analysis disabled", func() {
		It("does not include rds_analysis or rds_analysis_error in the response", func() {
			input := ValidateRDSInput{
				RDSType:     "core",
				RDSAnalysis: false,
			}
			result, _, err := HandleValidateRDS(context.Background(), nil, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeFalse())

			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())

			// Parse as raw map to check field absence (omitempty behavior)
			var parsed map[string]any
			Expect(json.Unmarshal([]byte(textContent.Text), &parsed)).To(Succeed())

			Expect(parsed).NotTo(HaveKey("rds_analysis"))
			Expect(parsed).NotTo(HaveKey("rds_analysis_error"))

			// Comparison and RDS reference should still be present
			Expect(parsed).To(HaveKey("comparison"))
			Expect(parsed).To(HaveKey("rds_reference"))
		})
	})

	Context("with RDS analysis enabled and non-JSON output format", func() {
		It("rejects non-JSON output format when analysis is enabled", func() {
			input := ValidateRDSInput{
				RDSType:      "core",
				RDSAnalysis:  true,
				OutputFormat: "yaml",
			}
			result, _, err := HandleValidateRDS(context.Background(), nil, input)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.IsError).To(BeTrue())

			textContent, ok := result.Content[0].(*mcp.TextContent)
			Expect(ok).To(BeTrue())
			Expect(textContent.Text).To(ContainSubstring("JSON"))
		})
	})
})
