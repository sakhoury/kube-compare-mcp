// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ValidateRDSResult is the structured response for the kube_compare_validate_rds tool.
type ValidateRDSResult struct {
	RDSReference *ResolveRDSResult `json:"rds_reference"`
	Comparison   json.RawMessage   `json:"comparison"`
}

// ValidateRDSInput defines the typed input for the kube_compare_validate_rds tool.
type ValidateRDSInput struct {
	Kubeconfig   string `json:"kubeconfig,omitempty" jsonschema:"Kubeconfig content (raw YAML or base64-encoded) for connecting to the target cluster. If omitted, uses in-cluster config."`
	Context      string `json:"context,omitempty" jsonschema:"Kubernetes context name to use from the provided kubeconfig"`
	RDSType      string `json:"rds_type" jsonschema:"RDS type to compare against: core for Telco Core RDS or ran for Telco RAN DU RDS"`
	OutputFormat string `json:"output_format,omitempty" jsonschema:"Output format for the comparison results"`
	AllResources bool   `json:"all_resources,omitempty" jsonschema:"Compare all resources of types mentioned in the reference"`
}

// ValidateRDSOutput is an empty output struct (tool returns text content).
type ValidateRDSOutput struct{}

// ValidateRDSTool returns the MCP tool definition for comparing a cluster against an RDS.
func ValidateRDSTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "kube_compare_validate_rds",
		Description: "Validate an OpenShift cluster's compliance with Red Hat Telco RDS. This is the recommended tool for RDS validation.",
		InputSchema: ValidateRDSInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

// ValidateRDSArgs holds the parsed arguments for the kube_compare_validate_rds operation.
type ValidateRDSArgs struct {
	Kubeconfig   string
	Context      string
	RDSType      string
	OutputFormat string
	AllResources bool
}

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
	)

	logger.Info("Finding RDS reference for cluster")
	rdsArgs := &ResolveRDSArgs{
		Kubeconfig: kubeconfig,
		Context:    input.Context,
		RDSType:    input.RDSType,
	}

	rdsResult, err := ResolveRDSInternal(ctx, rdsArgs)
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

	logger.Info("Starting cluster comparison", "reference", rdsResult.Reference)
	compareArgs := &CompareArgs{
		Reference:    rdsResult.Reference,
		OutputFormat: input.OutputFormat,
		AllResources: input.AllResources,
		Kubeconfig:   kubeconfig,
		Context:      input.Context,
	}

	if err := validateReference(ctx, compareArgs); err != nil {
		logger.Debug("Reference validation failed", "error", err)
		return newToolResultError(formatErrorForUser(err)), ValidateRDSOutput{}, nil
	}

	comparisonOutput, err := RunCompare(ctx, compareArgs)
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
