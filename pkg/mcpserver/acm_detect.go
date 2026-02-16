// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

// DetectViolationsInput defines the typed input for the acm_detect_violations tool.
type DetectViolationsInput struct {
	Kubeconfig string `json:"kubeconfig,omitempty" jsonschema:"Optional. Kubeconfig for the target cluster. Accepts a registered target key (secret_name/namespace from manage_targets) or base64-encoded kubeconfig or raw kubeconfig YAML. When omitted uses in-cluster or default config."`
	Context    string `json:"context,omitempty" jsonschema:"Kubernetes context name to use from the provided kubeconfig"`
	Namespace  string `json:"namespace,omitempty" jsonschema:"Filter policies by namespace"`
	Severity   string `json:"severity,omitempty" jsonschema:"Filter violations by minimum severity level"`
}

// DetectViolationsOutput is an empty output struct (tool returns text content).
type DetectViolationsOutput struct{}

// DetectViolationsTool returns the MCP tool definition for ACM policy violation detection.
func DetectViolationsTool() *mcp.Tool {
	return &mcp.Tool{
		Name:        "acm_detect_violations",
		Description: "Scan an ACM hub cluster for policy violations across all managed clusters. Point kubeconfig at the hub and get a summary of all non-compliant policies with severity and affected clusters. Use acm_diagnose_violation with the same hub kubeconfig and managed_cluster to deep-dive into specific violations.",
		InputSchema: DetectViolationsInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

// ACMService encapsulates dependencies for ACM operations.
type ACMService struct {
	ACMFactory acm.ACMClientFactory
}

// NewACMService creates a new ACMService with default implementations.
func NewACMService() *ACMService {
	return &ACMService{
		ACMFactory: acm.DefaultACMFactory,
	}
}

var defaultACMService = NewACMService()

// HandleDetectViolations is the MCP tool handler for the acm_detect_violations tool.
func HandleDetectViolations(ctx context.Context, req *mcp.CallToolRequest, input DetectViolationsInput) (*mcp.CallToolResult, DetectViolationsOutput, error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "acm_detect_violations")

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
		return newToolResultError(formatErrorForUser(ErrContextCanceled)), DetectViolationsOutput{}, nil
	}

	// Build REST config
	restConfig, err := ResolveKubeconfig(ctx, input.Kubeconfig, input.Context, logger)
	if err != nil {
		return newToolResultError(formatErrorForUser(err)), DetectViolationsOutput{}, nil
	}

	// Create policy lister
	lister, err := defaultACMService.ACMFactory.NewPolicyLister(restConfig)
	if err != nil {
		logger.Error("Failed to create policy lister", "error", err)
		return newToolResultError(formatErrorForUser(NewCompareError("acm-detect",
			fmt.Errorf("failed to create cluster client: %w", err),
			"Verify the kubeconfig is valid"))), DetectViolationsOutput{}, nil
	}

	// List policies
	policies, err := lister.ListPolicies(ctx, input.Namespace)
	if err != nil {
		logger.Error("Failed to list policies", "error", err)
		return newToolResultError(formatErrorForUser(err)), DetectViolationsOutput{}, nil
	}

	// Build result
	result := buildDetectResult(policies, input.Severity)

	jsonOutput, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("Failed to marshal result", "error", err)
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err)), DetectViolationsOutput{}, nil
	}

	duration := time.Since(start)
	logger.Info("ACM violation detection completed",
		"duration", duration,
		"totalPolicies", result.TotalPolicies,
		"nonCompliant", result.NonCompliant,
		"namespace", input.Namespace,
	)

	return newToolResultText(string(jsonOutput)), DetectViolationsOutput{}, nil
}

// buildDetectResult builds the detection result, filtering by severity if specified.
func buildDetectResult(policies []acm.PolicySummary, severityFilter string) *acm.DetectViolationsResult {
	result := &acm.DetectViolationsResult{
		TotalPolicies: len(policies),
	}

	severityOrder := map[string]int{
		"low":      0,
		"medium":   1,
		"high":     2,
		"critical": 3,
	}
	minSeverity := -1
	if severityFilter != "" {
		if order, ok := severityOrder[strings.ToLower(severityFilter)]; ok {
			minSeverity = order
		}
	}

	for _, p := range policies {
		switch strings.ToLower(p.Compliant) {
		case "compliant":
			result.Compliant++
		case "noncompliant":
			result.NonCompliant++

			// Apply severity filter
			if minSeverity >= 0 {
				policySeverity, ok := severityOrder[strings.ToLower(p.Severity)]
				if ok && policySeverity < minSeverity {
					continue
				}
			}
			result.Violations = append(result.Violations, p)
		default:
			result.Pending++
		}
	}

	return result
}
