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
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

var policyGVR = schema.GroupVersionResource{
	Group:    "policy.open-cluster-management.io",
	Version:  "v1",
	Resource: "policies",
}

// InspectACMPolicyInput defines the typed input for the inspect_acm_policy tool.
type InspectACMPolicyInput struct {
	PolicyName string `json:"policy_name" jsonschema:"Name of the ACM Policy (root or propagated). For propagated policies use the full dotted name e.g. ztp-common-cnfdf04.common-cnfdf04-subscriptions-policy."`
	Namespace  string `json:"namespace,omitempty" jsonschema:"Namespace of the Policy. If omitted, the tool will search all namespaces to find the policy."`
	Cluster    string `json:"cluster,omitempty" jsonschema:"Optional managed cluster name to filter violations."`
}

// InspectACMPolicyOutput is an empty output struct.
type InspectACMPolicyOutput struct{}

// InspectACMPolicyTool returns the MCP tool definition for inspect_acm_policy.
func InspectACMPolicyTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "inspect_acm_policy",
		Description: "Quick extraction of ACM policy compliance status and raw violation messages. " +
			"Use diagnose_acm_policy instead for full diagnosis with classified violations and " +
			"suggested next tool calls. This tool is for quick status checks only. " +
			"After calling this tool, follow the next_step instructions to continue investigation.",
		InputSchema: InspectACMPolicyInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

type policyInspection struct {
	PolicyName       string              `json:"policy_name"`
	Namespace        string              `json:"namespace"`
	ComplianceState  string              `json:"compliance_state"`
	AffectedClusters []clusterCompliance `json:"affected_clusters"`
	Templates        []templateInfo      `json:"templates"`
	Violations       []violation         `json:"violations"`
	NextStep         string              `json:"next_step,omitempty"`
}

type clusterCompliance struct {
	ClusterName     string `json:"cluster_name"`
	ComplianceState string `json:"compliance_state"`
}

type templateInfo struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
}

type violation struct {
	TemplateName  string `json:"template_name"`
	ClusterName   string `json:"cluster_name"`
	ViolationType string `json:"violation_type"`
	ResourceKind  string `json:"resource_kind,omitempty"`
	ResourceName  string `json:"resource_name,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	Message       string `json:"message"`
}

// HandleInspectACMPolicy is the MCP handler for the inspect_acm_policy tool.
func HandleInspectACMPolicy(ctx context.Context, req *mcp.CallToolRequest, input InspectACMPolicyInput) (*mcp.CallToolResult, InspectACMPolicyOutput, error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "inspect_acm_policy", "policy", input.PolicyName)

	defer func() {
		if r := recover(); r != nil {
			logger.Error("Panic recovered", "panic", r, "stackTrace", string(debug.Stack()))
		}
	}()

	if input.PolicyName == "" {
		return newToolResultError("'policy_name' is required"), InspectACMPolicyOutput{}, nil
	}

	restConfig, err := ResolveHubKubeconfig(logger)
	if err != nil {
		return newToolResultError(fmt.Sprintf("Failed to resolve hub kubeconfig: %s", formatErrorForUser(err))), InspectACMPolicyOutput{}, nil
	}

	inspector, err := acm.DefaultACMFactory.NewResourceInspector(restConfig)
	if err != nil {
		return newToolResultError(fmt.Sprintf("Failed to create cluster client: %v", err)), InspectACMPolicyOutput{}, nil
	}

	policy, resolvedNS, err := resolvePolicy(ctx, inspector, input.PolicyName, input.Namespace, logger)
	if err != nil {
		return newToolResultError(err.Error()), InspectACMPolicyOutput{}, nil
	}
	input.Namespace = resolvedNS

	result := parsePolicyInspection(policy, input.Cluster)
	result.PolicyName = input.PolicyName
	result.Namespace = input.Namespace

	// Enrich with propagated policy status from cluster namespaces.
	if len(result.AffectedClusters) > 0 {
		enrichViolationsFromPropagated(ctx, inspector, result)
	}

	// Add a forceful instruction telling the LLM what to do next.
	if len(result.Violations) > 0 && len(result.AffectedClusters) > 0 {
		cluster := result.AffectedClusters[0].ClusterName
		result.NextStep = fmt.Sprintf(
			"MANDATORY: You MUST continue the investigation. Do NOT stop here. Do NOT give text-only advice. "+
				"Step 1: Use openshift-mcp-server's resources_get or resources_list with cluster='%s' to inspect the violated resources on the managed cluster. "+
				"Step 2: For OLM-related violations (Subscription, CSV), call trace_olm_subscription with cluster='%s'. "+
				"Step 3: Present your findings based on the actual tool results. "+
				"NEVER suggest kubectl commands. ALWAYS use the MCP tools.",
			cluster, cluster)
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return newToolResultError(fmt.Sprintf("Failed to marshal result: %v", err)), InspectACMPolicyOutput{}, nil
	}

	logger.Info("inspect_acm_policy completed", "policy", input.PolicyName, "violations", len(result.Violations), "elapsed", time.Since(start))
	return newToolResultText(string(data)), InspectACMPolicyOutput{}, nil
}

// resolvePolicy finds a policy by name, auto-resolving the namespace if needed.
// It tries: (1) direct lookup if namespace is given, (2) cluster-wide search by name.
func resolvePolicy(ctx context.Context, inspector acm.ResourceInspector, name, namespace string, logger *slog.Logger) (*unstructured.Unstructured, string, error) {
	if namespace != "" {
		policy, err := inspector.GetResource(ctx, policyGVR, name, namespace)
		if err == nil {
			return policy, namespace, nil
		}
		logger.Info("Policy not found in given namespace, searching all namespaces", "policy", name, "namespace", namespace)
	}

	policies, err := inspector.ListResources(ctx, policyGVR, "")
	if err != nil {
		return nil, "", fmt.Errorf("failed to list policies across namespaces: %w", err)
	}
	for i := range policies {
		p := &policies[i]
		if p.GetName() == name {
			ns := p.GetNamespace()
			logger.Info("Auto-resolved policy namespace", "policy", name, "namespace", ns)
			return p, ns, nil
		}
	}

	if namespace != "" {
		return nil, "", fmt.Errorf("policy %q not found in namespace %q or any other namespace", name, namespace)
	}
	return nil, "", fmt.Errorf("policy %q not found in any namespace", name)
}

func parsePolicyInspection(policy *unstructured.Unstructured, clusterFilter string) *policyInspection {
	result := &policyInspection{}

	compliance, _, _ := unstructured.NestedString(policy.Object, "status", "compliant")
	result.ComplianceState = compliance

	// Extract templates from spec.policy-templates.
	templates, _, _ := unstructured.NestedSlice(policy.Object, "spec", "policy-templates")
	for _, t := range templates {
		tMap, ok := t.(map[string]any)
		if !ok {
			continue
		}
		objDef, _, _ := unstructured.NestedMap(tMap, "objectDefinition")
		kind, _ := objDef["kind"].(string)
		meta, _ := objDef["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		result.Templates = append(result.Templates, templateInfo{Name: name, Kind: kind})
	}

	// Extract per-cluster status from status.status (root policies).
	statusList, _, _ := unstructured.NestedSlice(policy.Object, "status", "status")
	for _, s := range statusList {
		sMap, ok := s.(map[string]any)
		if !ok {
			continue
		}
		clusterName, _ := sMap["clustername"].(string)
		state, _ := sMap["compliant"].(string)

		if clusterFilter != "" && clusterName != clusterFilter {
			continue
		}
		result.AffectedClusters = append(result.AffectedClusters, clusterCompliance{
			ClusterName:     clusterName,
			ComplianceState: state,
		})
	}

	// For propagated policies (no status.status), the namespace IS the
	// affected cluster. Detect this by checking if the policy name contains
	// a dot (root-ns.root-name pattern) and status.status is empty.
	ns := policy.GetNamespace()
	if len(result.AffectedClusters) == 0 && ns != "" {
		result.AffectedClusters = append(result.AffectedClusters, clusterCompliance{
			ClusterName:     ns,
			ComplianceState: compliance,
		})
	}

	// Extract violation details from status.details (only most recent history entry).
	details, _, _ := unstructured.NestedSlice(policy.Object, "status", "details")
	for _, d := range details {
		dMap, ok := d.(map[string]any)
		if !ok {
			continue
		}
		templateName, _ := dMap["templateMeta"].(map[string]any)["name"].(string)
		compState, _ := dMap["compliant"].(string)
		if compState == complianceCompliant {
			continue
		}
		history, _, _ := unstructured.NestedSlice(dMap, "history")
		if len(history) > 0 {
			if hMap, ok := history[0].(map[string]any); ok {
				msg, _ := hMap["message"].(string)
				v := violation{
					TemplateName: templateName,
					Message:      msg,
				}
				classifyViolationMessage(&v, msg)
				parseResourceFromMessage(&v, msg)
				result.Violations = append(result.Violations, v)
			}
		}
	}

	return result
}

func classifyViolationMessage(v *violation, msg string) {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "not found"):
		v.ViolationType = "missing"
	case strings.Contains(lower, "not as specified"):
		v.ViolationType = "drift"
	case strings.Contains(lower, "violation"):
		v.ViolationType = "violation"
	default:
		v.ViolationType = "unknown"
	}
	if len(v.Message) > maxViolationMessageLen {
		v.Message = v.Message[:maxViolationMessageLen] + "..."
	}
}

// parseResourceFromMessage tries to extract a resource kind/name from common
// ACM compliance messages like "[deployments.apps] foo in namespace bar ...".
func parseResourceFromMessage(v *violation, msg string) {
	if len(msg) < 2 || msg[0] != '[' {
		return
	}
	closeBracket := strings.Index(msg, "]")
	if closeBracket < 0 {
		return
	}
	v.ResourceKind = msg[1:closeBracket]
	rest := strings.TrimSpace(msg[closeBracket+1:])
	if idx := strings.IndexAny(rest, " \t"); idx > 0 {
		v.ResourceName = rest[:idx]
	} else if rest != "" {
		v.ResourceName = rest
	}
	if nsIdx := strings.Index(rest, "in namespace "); nsIdx >= 0 {
		after := rest[nsIdx+len("in namespace "):]
		if spIdx := strings.IndexAny(after, " \t"); spIdx > 0 {
			v.Namespace = after[:spIdx]
		} else {
			v.Namespace = after
		}
	}
}

func enrichViolationsFromPropagated(ctx context.Context, inspector acm.ResourceInspector, result *policyInspection) {
	for _, cluster := range result.AffectedClusters {
		if cluster.ComplianceState == complianceCompliant {
			continue
		}
		propagatedName := fmt.Sprintf("%s.%s", result.Namespace, result.PolicyName)
		propagated, err := inspector.GetResource(ctx, policyGVR, propagatedName, cluster.ClusterName)
		if err != nil {
			continue
		}

		details, _, _ := unstructured.NestedSlice(propagated.Object, "status", "details")
		for _, d := range details {
			dMap, ok := d.(map[string]any)
			if !ok {
				continue
			}
			templateName, _ := dMap["templateMeta"].(map[string]any)["name"].(string)
			compState, _ := dMap["compliant"].(string)
			if compState == "Compliant" {
				continue
			}
			history, _, _ := unstructured.NestedSlice(dMap, "history")
			if len(history) > 0 {
				if hMap, ok := history[0].(map[string]any); ok {
					msg, _ := hMap["message"].(string)
					v := violation{
						TemplateName: templateName,
						ClusterName:  cluster.ClusterName,
						Message:      msg,
					}
					classifyViolationMessage(&v, msg)
					parseResourceFromMessage(&v, msg)
					result.Violations = append(result.Violations, v)
				}
			}
		}
	}
}
