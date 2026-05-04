// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	rdsanalyzer "github.com/openshift-kni/rds-analyzer/pkg/analyzer"
	rdstypes "github.com/openshift-kni/rds-analyzer/pkg/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
)

// ValidateRDSResult is the structured response for the kube_compare_validate_rds tool.
type ValidateRDSResult struct {
	RDSReference     *ResolveRDSResult `json:"rds_reference"`
	Comparison       json.RawMessage   `json:"comparison"`
	RDSAnalysis      string            `json:"rds_analysis,omitempty"`
	RDSAnalysisError string            `json:"rds_analysis_error,omitempty"`
}

// ValidateRDSInput defines the typed input for the kube_compare_validate_rds tool.
type ValidateRDSInput struct {
	Kubeconfig        string `json:"kubeconfig,omitempty" jsonschema:"Kubeconfig content (raw YAML or base64-encoded) for connecting to the target cluster. If omitted, uses in-cluster config."`
	Context           string `json:"context,omitempty" jsonschema:"Kubernetes context name to use from the provided kubeconfig"`
	RDSType           string `json:"rds_type" jsonschema:"RDS type to compare against: core for Telco Core RDS, ran for Telco RAN DU RDS, or hub for Telco Hub RDS"`
	OutputFormat      string `json:"output_format,omitempty" jsonschema:"Output format for the comparison results"`
	AllResources      bool   `json:"all_resources,omitempty" jsonschema:"Compare all resources of types mentioned in the reference"`
	RDSAnalysis       bool   `json:"rds_analysis,omitempty" jsonschema:"Enable RDS impact analysis of detected deviations. When enabled, fetches analysis rules from a ConfigMap on the MCP server cluster and classifies each deviation as Impacting, NotImpacting, NeedsReview, or NotADeviation."`
	RDSAnalysisFormat string `json:"rds_analysis_format,omitempty" jsonschema:"Output format for RDS analysis results: text (terminal), html (rich), or reporting (structured sections)."`
}

// ValidateRDSOutput is an empty output struct (tool returns text content).
type ValidateRDSOutput struct{}

// ValidateRDSTool returns the MCP tool definition for comparing a cluster against an RDS.
func ValidateRDSTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "kube_compare_validate_rds",
		Description: "Validate an OpenShift cluster's compliance with Red Hat Telco RDS. " +
			"Optionally classifies deviations using RDS Analyzer rules into impact categories " +
			"(Impacting, NotImpacting, NeedsReview, NotADeviation) when rds_analysis is enabled. " +
			"This is the recommended tool for RDS validation.",
		InputSchema: ValidateRDSInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

// RulesFetcher retrieves analysis rules for a given RDS type.
type RulesFetcher func(ctx context.Context, rdsType string) ([]byte, error)

const rdsAnalyzerRulesConfigMap = "rds-analyzer-rules"

// analysisFormatText is the rds-analyzer format value for plain text output.
const analysisFormatText = "text"

// AnalysisService encapsulates dependencies for RDS analysis operations.
// This enables dependency injection for testing.
type AnalysisService struct {
	FetchRules RulesFetcher
}

// NewAnalysisService creates a new AnalysisService with default implementations.
func NewAnalysisService() *AnalysisService {
	return &AnalysisService{
		FetchRules: fetchAnalysisRulesFromConfigMap,
	}
}

var defaultAnalysisService = NewAnalysisService()

// Package-level function variables for testability.
// Tests can swap these to inject fakes without changing the handler signature.
var (
	resolveRDSFunc        = ResolveRDSInternal
	validateReferenceFunc = validateReference
	runCompareFunc        = RunCompare
)

// HandleValidateRDS is the MCP tool handler for the kube_compare_validate_rds tool.
// It uses typed input via the ValidateRDSInput struct.
func HandleValidateRDS(ctx context.Context, req *mcp.CallToolRequest, input ValidateRDSInput) (toolResult *mcp.CallToolResult, validateOutput ValidateRDSOutput, toolErr error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "kube_compare_validate_rds")

	// Handle panics
	defer func() {
		if r := recover(); r != nil {
			stackTrace := string(debug.Stack())
			logger.Error("Panic recovered in tool handler",
				"panic", r,
				"stackTrace", stackTrace,
			)
			toolResult = newToolResultError(fmt.Sprintf("Internal error: %v", r))
		}
	}()

	if err := ctx.Err(); err != nil {
		logger.Warn("Request canceled", "error", err)
		return newToolResultError(formatErrorForUser(ErrContextCanceled)), ValidateRDSOutput{}, nil
	}

	// Validate context requires kubeconfig
	if input.Context != "" && input.Kubeconfig == "" {
		err := NewValidationError("context",
			"'context' parameter requires 'kubeconfig' to also be provided",
			"Provide a kubeconfig along with the context name")
		logger.Debug("Validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), ValidateRDSOutput{}, nil
	}

	// Note: SDK validates enum constraint, so RDSType is already lowercase ("core" or "ran")

	// Auto-detect and process kubeconfig format
	kubeconfigData, err := DecodeOrParseKubeconfig(input.Kubeconfig)
	if err != nil {
		logger.Debug("Failed to parse kubeconfig", "error", err)
		return newToolResultError(formatErrorForUser(err)), ValidateRDSOutput{}, nil
	}

	var kubeconfig string
	if kubeconfigData != nil {
		// Convert to base64 for internal storage
		kubeconfig = base64.StdEncoding.EncodeToString(kubeconfigData)
		logger.Debug("Kubeconfig auto-detected and processed", "size", len(kubeconfigData))
	}

	logger.Debug("Parsed kube_compare_validate_rds arguments",
		"rdsType", input.RDSType,
		"hasKubeconfig", kubeconfig != "",
		"context", input.Context,
		"outputFormat", input.OutputFormat,
		"allResources", input.AllResources,
		"rdsAnalysis", input.RDSAnalysis,
		"rdsAnalysisFormat", input.RDSAnalysisFormat,
	)

	logger.Info("Finding RDS reference for cluster")
	rdsArgs := &ResolveRDSArgs{
		Kubeconfig: kubeconfig,
		Context:    input.Context,
		RDSType:    input.RDSType,
	}

	rdsResult, err := resolveRDSFunc(ctx, rdsArgs)
	if err != nil {
		logger.Debug("Failed to find RDS reference", "error", err)
		return newToolResultError(formatErrorForUser(err)), ValidateRDSOutput{}, nil
	}

	logger.Info("Found RDS reference",
		"reference", rdsResult.Reference,
		"clusterVersion", rdsResult.ClusterVersion,
		"rhelVersion", rdsResult.RHELVersion,
		"validated", rdsResult.Validated,
	)

	// When analysis is enabled, JSON output is required, if the client requested a different output
	// throw a validation error.
	if input.RDSAnalysis && input.OutputFormat != "" && input.OutputFormat != "json" {
		err := NewValidationError("output_format",
			fmt.Sprintf("RDS analysis requires JSON output format, but '%s' was requested", input.OutputFormat),
			"Either omit output_format (defaults to JSON) or set it to 'json' when using rds_analysis")
		return newToolResultError(formatErrorForUser(err)), ValidateRDSOutput{}, nil
	}

	logger.Info("Starting cluster comparison", "reference", rdsResult.Reference)
	compareArgs := &CompareArgs{
		Reference:    rdsResult.Reference,
		OutputFormat: input.OutputFormat,
		AllResources: input.AllResources,
		Kubeconfig:   kubeconfig,
		Context:      input.Context,
	}

	if err := validateReferenceFunc(ctx, compareArgs); err != nil {
		logger.Debug("Reference validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), ValidateRDSOutput{}, nil
	}

	comparisonOutput, err := runCompareFunc(ctx, compareArgs)
	if err != nil {
		logger.Debug("Comparison failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), ValidateRDSOutput{}, nil
	}

	var comparisonJSON json.RawMessage
	if json.Valid([]byte(comparisonOutput)) {
		comparisonJSON = json.RawMessage(comparisonOutput)
	} else {
		jsonBytes, _ := json.Marshal(comparisonOutput)
		comparisonJSON = json.RawMessage(jsonBytes)
	}

	combinedResult := ValidateRDSResult{
		RDSReference: rdsResult,
		Comparison:   comparisonJSON,
	}

	if input.RDSAnalysis {
		analysisFormat := input.RDSAnalysisFormat
		if analysisFormat == "" {
			analysisFormat = DefaultRDSAnalysisFormat
		}
		analysisOutput, analysisErr := RunRDSAnalysis(ctx, comparisonOutput, input.RDSType, rdsResult.ClusterVersion, analysisFormat, defaultAnalysisService.FetchRules)
		if analysisErr != nil {
			logger.Warn("RDS analysis failed (non-fatal)", "error", analysisErr)
			combinedResult.RDSAnalysisError = formatErrorForUser(fmt.Errorf("%w: %w", ErrRDSAnalysisFailed, analysisErr))
		} else {
			combinedResult.RDSAnalysis = analysisOutput
		}
	}

	jsonOutput, err := json.MarshalIndent(combinedResult, "", "  ")
	if err != nil {
		logger.Error("Failed to marshal result", "error", err)
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err)), ValidateRDSOutput{}, nil
	}

	duration := time.Since(start)
	logger.Info("RDS comparison completed",
		"duration", duration,
		"rdsType", input.RDSType,
		"clusterVersion", rdsResult.ClusterVersion,
		"rhelVersion", rdsResult.RHELVersion,
	)

	return newToolResultText(string(jsonOutput)), ValidateRDSOutput{}, nil
}

// RulesKeyForRDSType returns the ConfigMap data key for the given RDS type.
func RulesKeyForRDSType(rdsType string) string {
	return rdsType + "-rules.yaml"
}

// fetchAnalysisRulesFromConfigMap fetches RDS analysis rules from a ConfigMap
// on the MCP server cluster. Rules are loaded from in-cluster config only
// (not from user kubeconfig) so the server operator controls the compliance baseline.
func fetchAnalysisRulesFromConfigMap(ctx context.Context, rdsType string) ([]byte, error) {
	inClusterConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config not available for rules ConfigMap: %w", err)
	}

	client, err := dynamic.NewForConfig(inClusterConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dynamic client for rules ConfigMap: %w", err)
	}

	cm, err := client.Resource(configMapGVR).Namespace(DefaultReferenceConfigNamespace).Get(ctx, rdsAnalyzerRulesConfigMap, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch ConfigMap %s/%s: %w", DefaultReferenceConfigNamespace, rdsAnalyzerRulesConfigMap, err)
	}

	dataKey := RulesKeyForRDSType(rdsType)
	data, found, err := unstructured.NestedString(cm.Object, "data", dataKey)
	if err != nil {
		return nil, fmt.Errorf("failed to read key %q from ConfigMap: %w", dataKey, err)
	}
	if !found || data == "" {
		return nil, fmt.Errorf("key %q not found in ConfigMap %s/%s", dataKey, DefaultReferenceConfigNamespace, rdsAnalyzerRulesConfigMap)
	}

	return []byte(data), nil
}

// RunRDSAnalysis runs the RDS impact analysis on comparison JSON output.
func RunRDSAnalysis(ctx context.Context, comparisonJSON, rdsType, clusterVersion, analysisFormat string, fetchRules RulesFetcher) (string, error) {
	if clusterVersion == "" {
		return "", fmt.Errorf("cluster version is required for RDS analysis")
	}

	rulesData, err := fetchRules(ctx, rdsType)
	if err != nil {
		return "", fmt.Errorf("failed to fetch analysis rules: %w", err)
	}

	// rds-analyzer expects version without "v" prefix (e.g., "4.20" not "v4.20")
	ocpVersion := strings.TrimPrefix(ExtractMajorMinorVersion(clusterVersion), "v")

	analyzer, err := rdsanalyzer.NewFromBytes(rulesData, ocpVersion)
	if err != nil {
		return "", fmt.Errorf("failed to initialize analyzer: %w", err)
	}

	var report rdstypes.ValidationReport
	if err := json.Unmarshal([]byte(comparisonJSON), &report); err != nil {
		return "", fmt.Errorf("failed to parse comparison JSON: %w", err)
	}

	// Map analysis format to rds-analyzer's format and mode parameters
	var format, mode string
	switch analysisFormat {
	case "reporting":
		format = analysisFormatText
		mode = "reporting"
	case analysisFormatText:
		format = analysisFormatText
		mode = "simple"
	default:
		format = "html"
		mode = "simple"
	}

	var buf bytes.Buffer
	if err := analyzer.Analyze(&buf, report, format, mode); err != nil {
		return "", fmt.Errorf("analysis failed: %w", err)
	}

	return buf.String(), nil
}
