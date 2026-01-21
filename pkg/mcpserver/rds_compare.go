// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CompareClusterRDSResult is the structured response for the compare_cluster_rds tool.
type CompareClusterRDSResult struct {
	RDSReference *RDSReferenceResult `json:"rds_reference"`
	Comparison   json.RawMessage     `json:"comparison"`
}

// CompareClusterRDSInput defines the typed input for the compare_cluster_rds tool.
type CompareClusterRDSInput struct {
	Kubeconfig   string `json:"kubeconfig,omitempty" jsonschema:"Kubeconfig content for connecting to the target cluster"`
	Context      string `json:"context,omitempty" jsonschema:"Kubernetes context name to use from the provided kubeconfig"`
	RDSType      string `json:"rds_type" jsonschema:"RDS type to compare against: core for Telco Core RDS or ran for Telco RAN DU RDS"`
	OutputFormat string `json:"output_format,omitempty" jsonschema:"Output format for the comparison results: json, yaml, or junit"`
	AllResources bool   `json:"all_resources,omitempty" jsonschema:"Compare all resources of types mentioned in the reference"`
}

// CompareClusterRDSOutput is an empty output struct (tool returns text content).
type CompareClusterRDSOutput struct{}

// CompareClusterRDSTool returns the MCP tool definition for comparing a cluster against an RDS.
func CompareClusterRDSTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "compare_cluster_rds",
		Description: "Compare a Kubernetes/OpenShift cluster against a Reference Design Specification (RDS). " +
			"This tool automatically detects the cluster version, finds the appropriate RDS container reference, and " +
			"performs the comparison. When running inside an OpenShift cluster, no kubeconfig is needed - it will use " +
			"in-cluster config to compare the local cluster. Combines find_rds_reference and cluster_compare into a single operation.",
	}
}

// RDSCompareArgs holds the parsed arguments for the compare_cluster_rds operation.
type RDSCompareArgs struct {
	Kubeconfig   string
	Context      string
	RDSType      string
	OutputFormat string
	AllResources bool
}

// HandleCompareClusterRDS is the MCP tool handler for the compare_cluster_rds tool.
// It uses typed input via the CompareClusterRDSInput struct.
func HandleCompareClusterRDS(ctx context.Context, req *mcp.CallToolRequest, input CompareClusterRDSInput) (*mcp.CallToolResult, CompareClusterRDSOutput, error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "compare_cluster_rds")

	// Handle panics
	defer func() {
		if r := recover(); r != nil {
			stackTrace := string(debug.Stack())
			logger.Error("Panic recovered in tool handler",
				"panic", r,
				"stackTrace", stackTrace,
			)
		}
	}()

	if err := ctx.Err(); err != nil {
		logger.Warn("Request canceled", "error", err)
		return newToolResultError(formatErrorForUser(ErrContextCanceled)), CompareClusterRDSOutput{}, nil
	}

	// Validate and normalize RDS type
	rdsType := strings.ToLower(input.RDSType)
	if rdsType != RDSTypeCore && rdsType != RDSTypeRAN {
		err := NewValidationError("rds_type",
			fmt.Sprintf("invalid RDS type '%s'", input.RDSType),
			fmt.Sprintf("use '%s' or '%s'", RDSTypeCore, RDSTypeRAN))
		logger.Debug("Validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), CompareClusterRDSOutput{}, nil
	}

	// Apply defaults
	outputFormat := input.OutputFormat
	if outputFormat == "" {
		outputFormat = "json"
	}

	// Validate output format
	if err := ValidateOutputFormat(outputFormat); err != nil {
		logger.Debug("Invalid output format", "error", err)
		return newToolResultError(formatErrorForUser(err)), CompareClusterRDSOutput{}, nil
	}

	// Auto-detect and process kubeconfig format
	kubeconfigData, err := DecodeOrParseKubeconfig(input.Kubeconfig)
	if err != nil {
		logger.Debug("Failed to parse kubeconfig", "error", err)
		return newToolResultError(formatErrorForUser(err)), CompareClusterRDSOutput{}, nil
	}

	var kubeconfig string
	if kubeconfigData != nil {
		// Convert to base64 for internal storage
		kubeconfig = base64.StdEncoding.EncodeToString(kubeconfigData)
		logger.Debug("Kubeconfig auto-detected and processed", "size", len(kubeconfigData))
	}

	logger.Debug("Parsed compare_cluster_rds arguments",
		"rdsType", rdsType,
		"hasKubeconfig", kubeconfig != "",
		"context", input.Context,
		"outputFormat", outputFormat,
		"allResources", input.AllResources,
	)

	logger.Info("Finding RDS reference for cluster")
	rdsArgs := &RDSReferenceArgs{
		Kubeconfig: kubeconfig,
		Context:    input.Context,
		RDSType:    rdsType,
	}

	rdsResult, err := FindRDSReferenceInternal(ctx, rdsArgs)
	if err != nil {
		logger.Debug("Failed to find RDS reference", "error", err)
		return newToolResultError(formatErrorForUser(err)), CompareClusterRDSOutput{}, nil
	}

	logger.Info("Found RDS reference",
		"reference", rdsResult.Reference,
		"clusterVersion", rdsResult.ClusterVersion,
		"rhelVersion", rdsResult.RHELVersion,
		"validated", rdsResult.Validated,
	)

	logger.Info("Starting cluster comparison", "reference", rdsResult.Reference)
	compareArgs := &CompareArgs{
		Reference:    rdsResult.Reference,
		OutputFormat: outputFormat,
		AllResources: input.AllResources,
		Kubeconfig:   kubeconfig,
		Context:      input.Context,
	}

	if err := validateReference(ctx, compareArgs); err != nil {
		logger.Debug("Reference validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), CompareClusterRDSOutput{}, nil
	}

	comparisonOutput, err := RunCompare(ctx, compareArgs)
	if err != nil {
		logger.Debug("Comparison failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), CompareClusterRDSOutput{}, nil
	}

	var comparisonJSON json.RawMessage
	if json.Valid([]byte(comparisonOutput)) {
		comparisonJSON = json.RawMessage(comparisonOutput)
	} else {
		jsonBytes, _ := json.Marshal(comparisonOutput)
		comparisonJSON = json.RawMessage(jsonBytes)
	}

	combinedResult := CompareClusterRDSResult{
		RDSReference: rdsResult,
		Comparison:   comparisonJSON,
	}

	jsonOutput, err := json.MarshalIndent(combinedResult, "", "  ")
	if err != nil {
		logger.Error("Failed to marshal result", "error", err)
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err)), CompareClusterRDSOutput{}, nil
	}

	duration := time.Since(start)
	logger.Info("RDS comparison completed",
		"duration", duration,
		"rdsType", rdsType,
		"clusterVersion", rdsResult.ClusterVersion,
		"rhelVersion", rdsResult.RHELVersion,
	)

	return newToolResultText(string(jsonOutput)), CompareClusterRDSOutput{}, nil
}

// ParseRDSCompareArgs extracts and validates arguments from the MCP request.
func ParseRDSCompareArgs(arguments map[string]interface{}) (*RDSCompareArgs, error) {
	args := &RDSCompareArgs{
		OutputFormat: "json",
	}

	kubeconfigInput, err := GetStringArg(arguments, "kubeconfig", false)
	if err != nil {
		return nil, err
	}

	// Auto-detect kubeconfig format (raw YAML or base64-encoded)
	kubeconfigData, err := DecodeOrParseKubeconfig(kubeconfigInput)
	if err != nil {
		return nil, err
	}

	if kubeconfigData != nil {
		// Convert to base64 for internal storage (maintains compatibility with downstream functions)
		args.Kubeconfig = base64.StdEncoding.EncodeToString(kubeconfigData)
		slog.Default().Debug("Kubeconfig auto-detected and processed", "size", len(kubeconfigData))
	} else {
		slog.Default().Debug("No kubeconfig provided, will use in-cluster config")
	}

	if context, err := GetStringArg(arguments, "context", false); err != nil {
		return nil, err
	} else {
		args.Context = context
	}

	rdsType, err := GetStringArg(arguments, "rds_type", true)
	if err != nil {
		return nil, err
	}

	rdsType = strings.ToLower(rdsType)
	if rdsType != RDSTypeCore && rdsType != RDSTypeRAN {
		return nil, NewValidationError("rds_type",
			fmt.Sprintf("invalid RDS type '%s'", rdsType),
			fmt.Sprintf("use '%s' or '%s'", RDSTypeCore, RDSTypeRAN))
	}
	args.RDSType = rdsType

	if format, err := GetStringArg(arguments, "output_format", false); err != nil {
		return nil, err
	} else if format != "" {
		if err := ValidateOutputFormat(format); err != nil {
			return nil, err
		}
		args.OutputFormat = format
	}

	if allRes, err := GetBoolArg(arguments, "all_resources", false); err != nil {
		return nil, err
	} else {
		args.AllResources = allRes
	}

	return args, nil
}
