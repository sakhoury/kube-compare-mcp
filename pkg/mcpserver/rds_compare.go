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

	"github.com/mark3labs/mcp-go/mcp"
)

// CompareClusterRDSResult is the structured response for the compare_cluster_rds tool.
type CompareClusterRDSResult struct {
	RDSReference *RDSReferenceResult `json:"rds_reference"`
	Comparison   json.RawMessage     `json:"comparison"`
}

// CompareClusterRDSTool returns the MCP tool definition for comparing a cluster against an RDS.
func CompareClusterRDSTool() mcp.Tool {
	return mcp.NewTool(
		"compare_cluster_rds",
		mcp.WithDescription("Compare a Kubernetes/OpenShift cluster against a Reference Design Specification (RDS). "+
			"This tool automatically detects the cluster version, finds the appropriate RDS container reference, and "+
			"performs the comparison. When running inside an OpenShift cluster, no kubeconfig is needed - it will use "+
			"in-cluster config to compare the local cluster. Combines find_rds_reference and cluster_compare into a single operation."),
		mcp.WithString(
			"kubeconfig",
			mcp.Description("Kubeconfig content for connecting to the target cluster. "+
				"Accepts either raw YAML or base64-encoded content (auto-detected). "+
				"Optional: if not provided, uses in-cluster config to connect to the local cluster. "+
				"Note: exec-based and auth provider plugin authentication methods are not supported for security reasons."),
		),
		mcp.WithString(
			"context",
			mcp.Description("Kubernetes context name to use from the provided kubeconfig. "+
				"If not specified, uses the current-context from the kubeconfig. Only used when kubeconfig is provided."),
		),
		mcp.WithString(
			"rds_type",
			mcp.Required(),
			mcp.Description("RDS type to compare against: 'core' for Telco Core RDS or 'ran' for Telco RAN DU RDS."),
		),
		mcp.WithString(
			"output_format",
			mcp.Description("Output format for the comparison results: 'json', 'yaml', or 'junit'. Default: 'json'."),
		),
		mcp.WithBoolean(
			"all_resources",
			mcp.Description("Compare all resources of types mentioned in the reference. Default: false."),
		),
	)
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
func HandleCompareClusterRDS(ctx context.Context, req mcp.CallToolRequest) (result *mcp.CallToolResult, err error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "compare_cluster_rds")

	defer func() {
		if r := recover(); r != nil {
			stackTrace := string(debug.Stack())
			logger.Error("Panic recovered in tool handler",
				"panic", r,
				"stackTrace", stackTrace,
			)
			errMsg := fmt.Sprintf("Internal error: %v\n\nThis is a bug in kube-compare-mcp. Stack trace:\n%s", r, stackTrace)
			result = mcp.NewToolResultError(errMsg)
			err = nil
		}
	}()

	if err := ctx.Err(); err != nil {
		logger.Warn("Request canceled", "error", err)
		return mcp.NewToolResultError(formatErrorForUser(ErrContextCanceled)), nil
	}

	arguments, err := ExtractArguments(req)
	if err != nil {
		logger.Debug("Failed to extract arguments", "error", err)
		return mcp.NewToolResultError(formatErrorForUser(err)), nil
	}

	args, err := ParseRDSCompareArgs(arguments)
	if err != nil {
		logger.Debug("Failed to parse arguments", "error", err)
		return mcp.NewToolResultError(formatErrorForUser(err)), nil
	}

	logger.Debug("Parsed compare_cluster_rds arguments",
		"rdsType", args.RDSType,
		"hasKubeconfig", args.Kubeconfig != "",
		"context", args.Context,
		"outputFormat", args.OutputFormat,
		"allResources", args.AllResources,
	)

	logger.Info("Finding RDS reference for cluster")
	rdsArgs := &RDSReferenceArgs{
		Kubeconfig: args.Kubeconfig,
		Context:    args.Context,
		RDSType:    args.RDSType,
	}

	rdsResult, err := FindRDSReferenceInternal(ctx, rdsArgs)
	if err != nil {
		logger.Debug("Failed to find RDS reference", "error", err)
		return mcp.NewToolResultError(formatErrorForUser(err)), nil
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
		OutputFormat: args.OutputFormat,
		AllResources: args.AllResources,
		Kubeconfig:   args.Kubeconfig,
		Context:      args.Context,
	}

	if err := validateReference(ctx, compareArgs); err != nil {
		logger.Debug("Reference validation failed", "error", err)
		return mcp.NewToolResultError(formatErrorForUser(err)), nil
	}

	comparisonOutput, err := RunCompare(ctx, compareArgs)
	if err != nil {
		logger.Debug("Comparison failed", "error", err)
		return mcp.NewToolResultError(formatErrorForUser(err)), nil
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
		return mcp.NewToolResultError(fmt.Sprintf("Failed to format result: %v", err)), nil
	}

	duration := time.Since(start)
	logger.Info("RDS comparison completed",
		"duration", duration,
		"rdsType", args.RDSType,
		"clusterVersion", rdsResult.ClusterVersion,
		"rhelVersion", rdsResult.RHELVersion,
	)

	return mcp.NewToolResultText(string(jsonOutput)), nil
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
