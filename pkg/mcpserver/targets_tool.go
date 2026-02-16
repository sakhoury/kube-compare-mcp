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
	"k8s.io/client-go/rest"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
	"github.com/sakhoury/kube-compare-mcp/pkg/k8s"
)

// ManageTargetsInput defines the typed input for the manage_targets tool.
type ManageTargetsInput struct {
	Action  string `json:"action" jsonschema:"Action to perform: list or add or remove or discover"`
	Target  string `json:"target,omitempty" jsonschema:"For add/remove: secret_name/namespace (e.g. hub1/david). For discover: the hub kubeconfig target key."`
	Cluster string `json:"cluster,omitempty" jsonschema:"For discover: name of a specific managed cluster to discover. When omitted discovers all managed clusters from the hub."`
}

// ManageTargetsOutput is an empty output struct (tool returns text content).
type ManageTargetsOutput struct{}

// ManageTargetsTool returns the MCP tool definition for managing target clusters.
func ManageTargetsTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "manage_targets",
		Description: "Manage target cluster kubeconfigs for use by all tools. Actions: " +
			"'add' registers a secret-backed target (target=secret_name/namespace). " +
			"'list' shows all registered targets. 'remove' unregisters a target. " +
			"'discover' connects to an ACM hub (target=hub key) and extracts managed cluster kubeconfigs. " +
			"Optionally set cluster=name to discover a specific cluster or omit to discover all. " +
			"Discovered clusters are registered by name (e.g. kubeconfig='cnfdf04'). " +
			"Any registered target key can be passed as the kubeconfig parameter to any tool. " +
			"If a cluster is not found in the target list always try discover before asking the user.",
		InputSchema: ManageTargetsInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(false),
		},
	}
}

// HandleManageTargets is the MCP tool handler for the manage_targets tool.
func HandleManageTargets(ctx context.Context, req *mcp.CallToolRequest, input ManageTargetsInput) (*mcp.CallToolResult, ManageTargetsOutput, error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "manage_targets", "action", input.Action)

	defer func() {
		if r := recover(); r != nil {
			stackTrace := string(debug.Stack())
			logger.Error("Panic recovered in tool handler",
				"panic", r,
				"stackTrace", stackTrace,
			)
		}
	}()

	var result *mcp.CallToolResult

	switch input.Action {
	case "list":
		result = handleListTargets(logger, start)
	case "add":
		result = handleAddTarget(ctx, logger, start, input)
	case "remove":
		result = handleRemoveTarget(logger, start, input)
	case "discover":
		result = handleDiscoverTargets(ctx, logger, start, input)
	default:
		return newToolResultError(fmt.Sprintf(
			"Unknown action '%s'. Valid actions are: list, add, remove, discover.", input.Action,
		)), ManageTargetsOutput{}, nil
	}

	return result, ManageTargetsOutput{}, nil
}

// handleListTargets returns all registered target clusters.
func handleListTargets(logger *slog.Logger, start time.Time) *mcp.CallToolResult {
	targets := defaultTargetStore.List()

	type listResult struct {
		Count   int          `json:"count"`
		Targets []TargetInfo `json:"targets"`
		Usage   string       `json:"usage"`
	}

	result := listResult{
		Count:   len(targets),
		Targets: targets,
		Usage:   "Pass a target key (e.g. 'my-secret/my-namespace') as the kubeconfig parameter to any tool.",
	}

	jsonOutput, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err))
	}

	logger.Info("Listed target clusters", "count", len(targets), "duration", time.Since(start))
	return newToolResultText(string(jsonOutput))
}

// handleAddTarget registers a new target cluster after validating the secret exists.
func handleAddTarget(ctx context.Context, logger *slog.Logger, start time.Time, input ManageTargetsInput) *mcp.CallToolResult {
	secretName, namespace, err := parseTargetKey(input.Target)
	if err != nil {
		return newToolResultError(err.Error())
	}

	// Validate the secret exists on the local cluster.
	if err := validateTargetSecret(ctx, logger, secretName, namespace); err != nil {
		return newToolResultError(fmt.Sprintf(
			"Failed to validate secret %s/%s: %v", secretName, namespace, err,
		))
	}

	key := defaultTargetStore.Add(secretName, namespace)

	type addResult struct {
		Status string `json:"status"`
		Key    string `json:"key"`
		Usage  string `json:"usage"`
	}

	result := addResult{
		Status: "added",
		Key:    key,
		Usage:  fmt.Sprintf("Use kubeconfig='%s' in any tool to target this cluster.", key),
	}

	jsonOutput, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err))
	}

	logger.Info("Added target cluster",
		"key", key,
		"secret", secretName,
		"namespace", namespace,
		"duration", time.Since(start),
	)
	return newToolResultText(string(jsonOutput))
}

// handleRemoveTarget unregisters a target cluster.
func handleRemoveTarget(logger *slog.Logger, start time.Time, input ManageTargetsInput) *mcp.CallToolResult {
	secretName, namespace, err := parseTargetKey(input.Target)
	if err != nil {
		return newToolResultError(err.Error())
	}

	key := targetKey(secretName, namespace)
	existed := defaultTargetStore.Remove(key)

	type removeResult struct {
		Status string `json:"status"`
		Key    string `json:"key"`
	}

	status := "removed"
	if !existed {
		status = "not_found"
	}

	result := removeResult{
		Status: status,
		Key:    key,
	}

	jsonOutput, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err))
	}

	logger.Info("Removed target cluster", "key", key, "existed", existed, "duration", time.Since(start))
	return newToolResultText(string(jsonOutput))
}

// parseTargetKey splits a "secret_name/namespace" string into its components.
func parseTargetKey(target string) (secretName, namespace string, err error) {
	if target == "" {
		return "", "", fmt.Errorf("target is required for add and remove (format: secret_name/namespace)")
	}
	parts := strings.SplitN(target, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid target format '%s': expected secret_name/namespace (e.g. hub1/david)", target)
	}
	return parts[0], parts[1], nil
}

// validateTargetSecret checks that the secret exists on the local cluster and
// contains a 'kubeconfig' data key.
func validateTargetSecret(ctx context.Context, logger *slog.Logger, secretName, namespace string) error {
	inClusterConfig, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster config not available: %w", err)
	}

	inspector, err := acm.DefaultACMFactory.NewResourceInspector(inClusterConfig)
	if err != nil {
		return fmt.Errorf("failed to create resource inspector: %w", err)
	}

	// Use ReadKubeconfigSecret to validate the secret exists and is parseable.
	_, err = ReadKubeconfigSecret(ctx, inspector, secretName, namespace)
	if err != nil {
		return err
	}

	logger.Debug("Validated target secret",
		"secret", secretName,
		"namespace", namespace,
	)
	return nil
}

// resolveTargetSecret reads a kubeconfig from a Kubernetes secret on the local cluster.
// Used by buildACMRestConfig when the kubeconfig input matches a registered target.
func resolveTargetSecret(ctx context.Context, secretName, namespace string) (*rest.Config, error) {
	inClusterConfig, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("in-cluster config not available (needed to read target secret): %w", err)
	}

	inspector, err := acm.DefaultACMFactory.NewResourceInspector(inClusterConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create resource inspector for target secret: %w", err)
	}

	return ReadKubeconfigSecret(ctx, inspector, secretName, namespace)
}

// handleDiscoverTargets connects to an ACM hub and discovers managed cluster kubeconfigs.
// If input.Cluster is set, discovers only that cluster; otherwise discovers all.
func handleDiscoverTargets(ctx context.Context, logger *slog.Logger, start time.Time, input ManageTargetsInput) *mcp.CallToolResult {
	if input.Target == "" {
		return newToolResultError(
			"target is required for discover: set it to the hub kubeconfig target key (e.g. hub1/david)",
		)
	}

	// Resolve the hub kubeconfig.
	hubConfig, err := ResolveKubeconfig(ctx, input.Target, "", logger)
	if err != nil {
		return newToolResultError(fmt.Sprintf(
			"Failed to resolve hub kubeconfig from target '%s': %v", input.Target, err,
		))
	}

	hubInspector, err := acm.DefaultACMFactory.NewResourceInspector(hubConfig)
	if err != nil {
		return newToolResultError(fmt.Sprintf(
			"Failed to create hub inspector: %v", err,
		))
	}

	// Verify this is actually an ACM hub.
	hubInfo, _ := k8s.DetectHub(ctx, hubInspector)
	if !hubInfo.IsHub {
		return newToolResultError(fmt.Sprintf(
			"Target '%s' is not an ACM hub cluster. The discover action requires a hub.", input.Target,
		))
	}

	// Determine which clusters to discover.
	var clusterNames []string
	if input.Cluster != "" {
		clusterNames = []string{input.Cluster}
	} else {
		// List all ManagedCluster resources on the hub.
		managedClusters, listErr := hubInspector.ListResources(ctx, k8s.ManagedClusterGVR, "")
		if listErr != nil {
			return newToolResultError(fmt.Sprintf(
				"Failed to list ManagedCluster resources on hub: %v", listErr,
			))
		}
		for _, mc := range managedClusters {
			name := mc.GetName()
			// Skip the local-cluster which is the hub itself.
			if name == "local-cluster" {
				continue
			}
			clusterNames = append(clusterNames, name)
		}
	}

	if len(clusterNames) == 0 {
		return newToolResultText(`{"discovered": 0, "message": "No managed clusters found on the hub."}`)
	}

	type discoverResult struct {
		Cluster string `json:"cluster"`
		Status  string `json:"status"`
		Key     string `json:"key,omitempty"`
		Error   string `json:"error,omitempty"`
	}

	hubSource := fmt.Sprintf("discovered:%s", input.Target)
	var results []discoverResult
	registered := 0

	for _, clusterName := range clusterNames {
		config, source, extractErr := ExtractManagedClusterKubeconfig(ctx, hubInspector, clusterName)
		if extractErr != nil {
			logger.Warn("Failed to extract kubeconfig for managed cluster",
				"cluster", clusterName,
				"error", extractErr,
			)
			results = append(results, discoverResult{
				Cluster: clusterName,
				Status:  "failed",
				Error:   extractErr.Error(),
			})
			continue
		}

		key := defaultTargetStore.AddWithConfig(clusterName, config, hubSource)
		registered++
		logger.Info("Discovered and registered managed cluster",
			"cluster", clusterName,
			"key", key,
			"source", source,
		)
		results = append(results, discoverResult{
			Cluster: clusterName,
			Status:  "registered",
			Key:     key,
		})
	}

	type discoverOutput struct {
		Discovered int              `json:"discovered"`
		Registered int              `json:"registered"`
		Failed     int              `json:"failed"`
		HubTarget  string           `json:"hub_target"`
		Clusters   []discoverResult `json:"clusters"`
		Usage      string           `json:"usage"`
	}

	output := discoverOutput{
		Discovered: len(clusterNames),
		Registered: registered,
		Failed:     len(clusterNames) - registered,
		HubTarget:  input.Target,
		Clusters:   results,
		Usage:      "Use the cluster name as the kubeconfig parameter in any tool (e.g. kubeconfig='cnfdf04').",
	}

	jsonOutput, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err))
	}

	logger.Info("Discover completed",
		"hub", input.Target,
		"discovered", len(clusterNames),
		"registered", registered,
		"duration", time.Since(start),
	)
	return newToolResultText(string(jsonOutput))
}
