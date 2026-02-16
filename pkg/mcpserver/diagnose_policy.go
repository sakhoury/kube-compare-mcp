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

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

const (
	complianceCompliant    = "Compliant"
	violationOLMStuck      = "olm_stuck"
	violationMissing       = "resource_missing"
	violationDrift         = "resource_drift"
	violationUnknown       = "unknown"
	maxViolationMessageLen = 300
)

// DiagnoseACMPolicyInput defines the typed input for the diagnose_acm_policy tool.
type DiagnoseACMPolicyInput struct {
	PolicyName string `json:"policy_name" jsonschema:"required,Name of the ACM Policy to diagnose. Works with root or propagated policies."`
	Namespace  string `json:"namespace,omitempty" jsonschema:"Namespace of the Policy. If omitted the tool searches all namespaces."`
	Cluster    string `json:"cluster,omitempty" jsonschema:"Optional managed cluster name to focus the diagnosis on."`
}

// DiagnoseACMPolicyTool returns the MCP tool definition for diagnose_acm_policy.
func DiagnoseACMPolicyTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "diagnose_acm_policy",
		Description: "ALWAYS call this tool FIRST when a user asks to diagnose, debug, check, or investigate " +
			"any ACM policy, NonCompliant policy, or ztp-policies resource. " +
			"Analyzes hub-side policy data, classifies violations (resource_missing, resource_drift, " +
			"olm_stuck, crd_missing), extracts the desired state from ConfigurationPolicy templates, " +
			"and returns structured JSON with suggested_tool_call for the next step. " +
			"After calling this tool, ALWAYS execute the suggested_tool_call to complete the investigation.",
		InputSchema: DiagnoseACMPolicyInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

type diagnosis struct {
	PolicyName      string             `json:"policy_name"`
	Namespace       string             `json:"namespace"`
	ComplianceState string             `json:"compliance_state"`
	Clusters        []clusterDiagnosis `json:"clusters"`
	Summary         string             `json:"summary"`
}

type clusterDiagnosis struct {
	ClusterName     string           `json:"cluster_name"`
	ComplianceState string           `json:"compliance_state"`
	Issues          []diagnosedIssue `json:"issues"`
}

type diagnosedIssue struct {
	ViolationType     string         `json:"violation_type"`
	TemplateName      string         `json:"template_name"`
	ResourceKind      string         `json:"resource_kind,omitempty"`
	ResourceName      string         `json:"resource_name,omitempty"`
	ResourceNamespace string         `json:"resource_namespace,omitempty"`
	Message           string         `json:"message"`
	DesiredState      map[string]any `json:"desired_state,omitempty"`
	SuggestedToolCall *suggestedCall `json:"suggested_tool_call,omitempty"`
}

type suggestedCall struct {
	Server string         `json:"server"`
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
}

// HandleDiagnoseACMPolicy is the MCP handler for the diagnose_acm_policy tool.
func HandleDiagnoseACMPolicy(ctx context.Context, req *mcp.CallToolRequest, input DiagnoseACMPolicyInput) (*mcp.CallToolResult, InspectACMPolicyOutput, error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "diagnose_acm_policy", "policy", input.PolicyName)

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

	diag := buildDiagnosis(ctx, inspector, policy, resolvedNS, input.PolicyName, input.Cluster, logger)

	data, err := json.MarshalIndent(diag, "", "  ")
	if err != nil {
		return newToolResultError(fmt.Sprintf("Failed to marshal result: %v", err)), InspectACMPolicyOutput{}, nil
	}

	logger.Info("diagnose_acm_policy completed",
		"policy", input.PolicyName,
		"clusters", len(diag.Clusters),
		"elapsed", time.Since(start),
	)
	return newToolResultText(string(data)), InspectACMPolicyOutput{}, nil
}

func buildDiagnosis(ctx context.Context, inspector acm.ResourceInspector, policy *unstructured.Unstructured, namespace, policyName, clusterFilter string, logger *slog.Logger) *diagnosis {
	diag := &diagnosis{
		PolicyName: policyName,
		Namespace:  namespace,
	}

	compliance, _, _ := unstructured.NestedString(policy.Object, "status", "compliant")
	diag.ComplianceState = compliance

	desiredStates := extractDesiredStates(policy)

	// Collect affected clusters from status.status (root policies).
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
		if state == complianceCompliant {
			continue
		}
		cd := clusterDiagnosis{
			ClusterName:     clusterName,
			ComplianceState: state,
		}
		enrichClusterDiagnosis(ctx, inspector, &cd, namespace, policyName, desiredStates, logger)
		diag.Clusters = append(diag.Clusters, cd)
	}

	// For propagated policies (no status.status), the namespace IS the cluster.
	if len(diag.Clusters) == 0 && compliance != complianceCompliant {
		ns := policy.GetNamespace()
		if ns != "" {
			cd := clusterDiagnosis{
				ClusterName:     ns,
				ComplianceState: compliance,
			}
			enrichClusterDiagnosisFromPolicy(policy, &cd, desiredStates)
			diag.Clusters = append(diag.Clusters, cd)
		}
	}

	totalIssues := 0
	for _, c := range diag.Clusters {
		totalIssues += len(c.Issues)
	}
	diag.Summary = fmt.Sprintf("Found %d issue(s) across %d non-compliant cluster(s). "+
		"Follow the suggested_tool_call in each issue to continue investigation.",
		totalIssues, len(diag.Clusters))

	return diag
}

// extractDesiredStates pulls the objectDefinition from each ConfigurationPolicy's object-templates.
func extractDesiredStates(policy *unstructured.Unstructured) map[string]map[string]any {
	states := make(map[string]map[string]any)

	templates, _, _ := unstructured.NestedSlice(policy.Object, "spec", "policy-templates")
	for _, t := range templates {
		tMap, ok := t.(map[string]any)
		if !ok {
			continue
		}
		objDef, _, _ := unstructured.NestedMap(tMap, "objectDefinition")
		if objDef == nil {
			continue
		}
		meta, _ := objDef["metadata"].(map[string]any)
		templateName, _ := meta["name"].(string)
		kind, _ := objDef["kind"].(string)
		if kind != "ConfigurationPolicy" {
			continue
		}
		spec, _ := objDef["spec"].(map[string]any)
		if spec == nil {
			continue
		}
		objectTemplates, ok := spec["object-templates"].([]any)
		if !ok {
			continue
		}
		for _, ot := range objectTemplates {
			otMap, ok := ot.(map[string]any)
			if !ok {
				continue
			}
			desired, _ := otMap["objectDefinition"].(map[string]any)
			if desired == nil {
				continue
			}
			rKind, _ := desired["kind"].(string)
			rMeta, _ := desired["metadata"].(map[string]any)
			rName, _ := rMeta["name"].(string)
			key := fmt.Sprintf("%s/%s/%s", templateName, rKind, rName)
			states[key] = desired
		}
	}
	return states
}

func enrichClusterDiagnosis(ctx context.Context, inspector acm.ResourceInspector, cd *clusterDiagnosis, rootNS, rootPolicy string, desiredStates map[string]map[string]any, logger *slog.Logger) {
	propagatedName := fmt.Sprintf("%s.%s", rootNS, rootPolicy)
	propagated, err := inspector.GetResource(ctx, policyGVR, propagatedName, cd.ClusterName)
	if err != nil {
		logger.Debug("Could not fetch propagated policy", "cluster", cd.ClusterName, "error", err)
		cd.Issues = append(cd.Issues, diagnosedIssue{
			ViolationType: "unknown",
			Message:       fmt.Sprintf("Could not fetch propagated policy %s in namespace %s", propagatedName, cd.ClusterName),
		})
		return
	}
	enrichClusterDiagnosisFromPolicy(propagated, cd, desiredStates)
}

func enrichClusterDiagnosisFromPolicy(policy *unstructured.Unstructured, cd *clusterDiagnosis, desiredStates map[string]map[string]any) {
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
		if len(history) == 0 {
			continue
		}
		hMap, ok := history[0].(map[string]any)
		if !ok {
			continue
		}
		msg, _ := hMap["message"].(string)

		issue := diagnosedIssue{
			TemplateName: templateName,
			Message:      msg,
		}

		classifyAndEnrich(&issue, msg, cd.ClusterName, templateName, desiredStates)

		if len(issue.Message) > maxViolationMessageLen {
			issue.Message = issue.Message[:maxViolationMessageLen] + "..."
		}

		cd.Issues = append(cd.Issues, issue)
	}
}

func classifyAndEnrich(issue *diagnosedIssue, msg, clusterName, templateName string, desiredStates map[string]map[string]any) {
	lower := strings.ToLower(msg)

	violationMsg := msg
	if idx := strings.Index(violationMsg, "violation - "); idx >= 0 {
		violationMsg = violationMsg[idx+len("violation - "):]
	}

	rKind, rName, rNS := parseViolationResource(violationMsg)

	issue.ResourceKind = rKind
	issue.ResourceName = rName
	issue.ResourceNamespace = rNS

	switch {
	case strings.Contains(lower, "not found"):
		issue.ViolationType = violationMissing
	case strings.Contains(lower, "not as specified"):
		issue.ViolationType = violationDrift
	case strings.Contains(lower, "installplan") || strings.Contains(lower, "clusterserviceversion"):
		issue.ViolationType = violationOLMStuck
	default:
		issue.ViolationType = violationUnknown
	}

	issue.DesiredState = matchDesiredState(desiredStates, templateName, rKind, rName)
	issue.SuggestedToolCall = buildSuggestedCall(issue, rKind, rName, rNS, clusterName, desiredStates)
}

func isOLMKind(kind string) bool {
	olmKinds := []string{
		"Subscription", "subscriptions", "subscriptions.operators.coreos.com",
		"ClusterServiceVersion", "clusterserviceversions",
		"Operator", "operators", "operators.operators.coreos.com",
		"InstallPlan", "installplans",
		"CatalogSource", "catalogsources",
	}
	for _, k := range olmKinds {
		if strings.EqualFold(kind, k) {
			return true
		}
	}
	return false
}

func buildSuggestedCall(issue *diagnosedIssue, rKind, rName, rNS, clusterName string, desiredStates map[string]map[string]any) *suggestedCall {
	isOLM := issue.ViolationType == violationOLMStuck || isOLMKind(rKind)

	switch {
	case isOLM && rName != "":
		issue.ViolationType = violationOLMStuck
		return buildOLMToolCall(rKind, rName, rNS, clusterName, desiredStates)
	case rKind != "" && rName != "":
		args := map[string]any{
			"resource":       fmt.Sprintf("%s/%s", strings.ToLower(rKind), rName),
			"cluster":        clusterName,
			"clean_metadata": true,
		}
		if rNS != "" {
			args["namespace"] = rNS
		}
		return &suggestedCall{Server: "openshift-mcp-server", Tool: "resources_get", Args: args}
	case rKind != "":
		args := map[string]any{
			"resource":       strings.ToLower(rKind),
			"cluster":        clusterName,
			"clean_metadata": true,
		}
		if rNS != "" {
			args["namespace"] = rNS
		}
		return &suggestedCall{Server: "openshift-mcp-server", Tool: "resources_list", Args: args}
	default:
		return nil
	}
}

func buildOLMToolCall(rKind, rName, rNS, clusterName string, desiredStates map[string]map[string]any) *suggestedCall {
	subName := rName
	subNS := rNS

	isOperatorCR := strings.EqualFold(rKind, "operators") ||
		strings.EqualFold(rKind, "Operator") ||
		strings.EqualFold(rKind, "operators.operators.coreos.com")
	if isOperatorCR {
		if sub := findSubscriptionInDesiredStates(desiredStates); sub != nil {
			subName = sub.name
			if sub.namespace != "" {
				subNS = sub.namespace
			}
		} else if strings.Contains(rName, ".") {
			parts := strings.SplitN(rName, ".", 2)
			subName = parts[0]
			subNS = parts[1]
		}
	}

	if subNS == "" {
		subNS = "openshift-operators"
	}
	return &suggestedCall{
		Server: "openshift-mcp-server",
		Tool:   "trace_olm_subscription",
		Args: map[string]any{
			"subscription_name":      subName,
			"subscription_namespace": subNS,
			"cluster":                clusterName,
		},
	}
}

// parseViolationResource extracts kind, name, and namespace from an ACM
// violation detail string. Handles two formats:
//   - "kind [name] description"        (e.g. "operators [web-terminal.openshift-web-terminal] found but not as specified")
//   - "[kind] name description"         (legacy/alternate format)
func parseViolationResource(detail string) (kind, name, ns string) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return
	}

	// Format: "kind [name] ..." or "kind found/not found ..."
	if detail[0] != '[' {
		openBracket := strings.Index(detail, "[")
		if openBracket > 0 {
			kind = strings.TrimSpace(detail[:openBracket])
			closeBracket := strings.Index(detail[openBracket:], "]")
			if closeBracket > 0 {
				name = detail[openBracket+1 : openBracket+closeBracket]
			}
		} else {
			// No brackets: "nodes found but not as specified"
			if spIdx := strings.IndexAny(detail, " \t"); spIdx > 0 {
				kind = detail[:spIdx]
			}
		}
	} else {
		// Format: "[kind] name ..."
		closeBracket := strings.Index(detail, "]")
		if closeBracket > 0 {
			kind = detail[1:closeBracket]
			rest := strings.TrimSpace(detail[closeBracket+1:])
			if idx := strings.IndexAny(rest, " \t"); idx > 0 {
				name = rest[:idx]
			} else if rest != "" {
				name = rest
			}
		}
	}

	// Extract namespace from "in namespace <ns>"
	if nsIdx := strings.Index(detail, "in namespace "); nsIdx >= 0 {
		after := detail[nsIdx+len("in namespace "):]
		if spIdx := strings.IndexAny(after, " \t;,"); spIdx > 0 {
			ns = after[:spIdx]
		} else {
			ns = after
		}
	}
	return
}

// matchDesiredState finds the best matching desired state from the extracted
// objectDefinitions. ACM violations use plural resource names ("nodes") while
// objectDefinitions use singular Kind ("Node"), so we try multiple strategies.
func matchDesiredState(desiredStates map[string]map[string]any, templateName, rKind, rName string) map[string]any {
	// Exact match: template/kind/name
	for key, desired := range desiredStates {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) == 3 && parts[0] == templateName && strings.EqualFold(parts[1], rKind) && parts[2] == rName {
			return desired
		}
	}

	// Flexible match: compare plural violation kind against singular objectDefinition kind.
	// e.g. "nodes" matches "Node", "operators" matches "Operator"
	for key, desired := range desiredStates {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) != 3 || parts[0] != templateName {
			continue
		}
		objKind := strings.ToLower(parts[1])
		vKind := strings.ToLower(rKind)
		if objKind+"s" == vKind || objKind+"es" == vKind || vKind+"s" == objKind || vKind == objKind {
			if rName == "" || parts[2] == rName {
				return desired
			}
		}
	}

	// Last resort: if there's exactly one desired state for this template, use it.
	var match map[string]any
	count := 0
	for key, desired := range desiredStates {
		parts := strings.SplitN(key, "/", 3)
		if len(parts) == 3 && parts[0] == templateName {
			match = desired
			count++
		}
	}
	if count == 1 {
		return match
	}
	return nil
}

type subscriptionRef struct {
	name      string
	namespace string
}

func findSubscriptionInDesiredStates(desiredStates map[string]map[string]any) *subscriptionRef {
	for _, desired := range desiredStates {
		kind, _ := desired["kind"].(string)
		if !strings.EqualFold(kind, "Subscription") {
			continue
		}
		meta, _ := desired["metadata"].(map[string]any)
		if meta == nil {
			continue
		}
		name, _ := meta["name"].(string)
		if name == "" {
			continue
		}
		ns, _ := meta["namespace"].(string)
		return &subscriptionRef{name: name, namespace: ns}
	}
	return nil
}
