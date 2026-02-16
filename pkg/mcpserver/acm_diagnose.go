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
	"github.com/sakhoury/kube-compare-mcp/pkg/k8s"
)

// DiagnoseViolationInput defines the typed input for the acm_diagnose_violation tool.
type DiagnoseViolationInput struct {
	Kubeconfig      string `json:"kubeconfig,omitempty" jsonschema:"Kubeconfig for the ACM hub cluster. Point this at the hub and the tool will automatically extract the managed cluster kubeconfig from hub secrets. Accepts a registered target key (secret_name/namespace) or base64 or raw YAML. When omitted uses in-cluster config."`
	Context         string `json:"context,omitempty" jsonschema:"Optional. Kubernetes context name to use from the provided kubeconfig. Ignored when kubeconfig is omitted."`
	PolicyName      string `json:"policy_name" jsonschema:"Name of the ACM policy to diagnose"`
	PolicyNamespace string `json:"policy_namespace,omitempty" jsonschema:"Namespace of the ACM policy (searches all namespaces if omitted)"`
	ManagedCluster  string `json:"managed_cluster,omitempty" jsonschema:"Name of the managed/spoke cluster to inspect. The tool connects to this cluster automatically through the hub. When omitted the tool auto-detects the cluster from the policy status."`
	Verbose         *bool  `json:"verbose,omitempty" jsonschema:"Optional. Controls whether a diagnostic_trace is included for each violation showing which RCA steps ran and the decision at each step. Defaults to false."`
}

// DiagnoseViolationOutput is an empty output struct (tool returns text content).
type DiagnoseViolationOutput struct{}

// DiagnoseViolationTool returns the MCP tool definition for deep violation analysis.
func DiagnoseViolationTool() *mcp.Tool {
	return &mcp.Tool{
		Name: "acm_diagnose_violation",
		Description: "Deep root-cause analysis of a specific ACM policy violation. " +
			"Connect to the ACM **hub** cluster via the kubeconfig parameter and the tool automatically " +
			"extracts the managed cluster's kubeconfig from hub secrets (ClusterDeployment) to inspect " +
			"resources on the managed cluster directly. Use managed_cluster to specify which spoke to inspect. " +
			"Performs ownership detection / dependency validation / mutability check / conflict detection / event history " +
			"and generates remediation YAML with step-by-step instructions.",
		InputSchema: DiagnoseViolationInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: ptrBool(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptrBool(true),
		},
	}
}

// HandleDiagnoseViolation is the MCP tool handler for the acm_diagnose_violation tool.
func HandleDiagnoseViolation(ctx context.Context, req *mcp.CallToolRequest, input DiagnoseViolationInput) (*mcp.CallToolResult, DiagnoseViolationOutput, error) {
	requestID := generateRequestID()
	logger := slog.Default().With("requestID", requestID)
	start := time.Now()

	logger.Debug("Received tool request", "tool", "acm_diagnose_violation")

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
		return newToolResultError(formatErrorForUser(ErrContextCanceled)), DiagnoseViolationOutput{}, nil
	}

	if input.PolicyName == "" {
		return newToolResultError(formatErrorForUser(
			NewValidationError("policy_name", "policy_name is required", "Provide the name of the ACM policy to diagnose"),
		)), DiagnoseViolationOutput{}, nil
	}

	// Build REST config
	restConfig, err := ResolveKubeconfig(ctx, input.Kubeconfig, input.Context, logger)
	if err != nil {
		return newToolResultError(formatErrorForUser(err)), DiagnoseViolationOutput{}, nil
	}

	// Create clients
	lister, err := defaultACMService.ACMFactory.NewPolicyLister(restConfig)
	if err != nil {
		logger.Error("Failed to create policy lister", "error", err)
		return newToolResultError(formatErrorForUser(NewCompareError("acm-diagnose",
			fmt.Errorf("failed to create cluster client: %w", err),
			"Verify the kubeconfig is valid"))), DiagnoseViolationOutput{}, nil
	}

	inspector, err := defaultACMService.ACMFactory.NewResourceInspector(restConfig)
	if err != nil {
		logger.Error("Failed to create resource inspector", "error", err)
		return newToolResultError(formatErrorForUser(NewCompareError("acm-diagnose",
			fmt.Errorf("failed to create cluster client: %w", err),
			"Verify the kubeconfig is valid"))), DiagnoseViolationOutput{}, nil
	}

	// Get the policy detail
	policy, err := lister.GetPolicy(ctx, input.PolicyName, input.PolicyNamespace)
	if err != nil {
		logger.Error("Failed to get policy", "error", err)
		return newToolResultError(formatErrorForUser(err)), DiagnoseViolationOutput{}, nil
	}

	logger.Info("Diagnosing policy violation",
		"policy", policy.Name,
		"namespace", policy.Namespace,
		"compliant", policy.Compliant,
		"templates", len(policy.Templates),
	)

	// Detect if this is a hub cluster and extract managed cluster kubeconfig if so.
	inspector, inspectionMode, hubDiag := resolveInspector(ctx, logger, inspector, policy, input.ManagedCluster)

	// Analyze each template's object
	var violations []acm.ViolationDetail
	for i := range policy.Templates {
		tmpl := &policy.Templates[i]
		verbose := input.Verbose != nil && *input.Verbose // default false
		violation := diagnoseTemplate(ctx, logger, inspector, lister, tmpl, verbose)
		violations = append(violations, violation)
	}

	// Post-processing: detect cascading failures
	detectCascadingFailures(violations)

	// Build summary and counts
	summary, needsAction, compliant := buildDiagnosisSummary(policy, violations)

	result := acm.DiagnoseViolationResult{
		Policy:           *policy,
		InspectionMode:   inspectionMode,
		HubDiagnostic:    hubDiag,
		NeedsActionCount: needsAction,
		CompliantCount:   compliant,
		Violations:       violations,
		Summary:          summary,
	}

	jsonOutput, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		logger.Error("Failed to marshal result", "error", err)
		return newToolResultError(fmt.Sprintf("Failed to format result: %v", err)), DiagnoseViolationOutput{}, nil
	}

	duration := time.Since(start)
	logger.Info("Violation diagnosis completed",
		"duration", duration,
		"policy", input.PolicyName,
		"violationCount", len(violations),
	)

	return newToolResultText(string(jsonOutput)), DiagnoseViolationOutput{}, nil
}

// diagnoseTemplate runs the full root cause analysis and remediation generation for a single template.
func diagnoseTemplate(ctx context.Context, logger *slog.Logger, inspector acm.ResourceInspector, lister acm.PolicyLister, tmpl *acm.ConfigPolicyTemplate, verbose bool) acm.ViolationDetail {
	violation := acm.ViolationDetail{
		Template: *tmpl,
	}

	// Resolve GVR for the target resource
	gvr, resName, resNamespace, err := acm.GVRFromObjectDef(tmpl.ObjectDefinition)
	if err != nil {
		logger.Debug("Failed to resolve GVR from objectDefinition", "error", err)
		violation.Status = acm.StatusNeedsAction
		violation.RootCause = &acm.RootCauseResult{
			PrimaryCause: acm.CauseUnknown,
			Confidence:   acm.ConfidenceLow,
			Detail:       fmt.Sprintf("Could not parse objectDefinition: %v", err),
		}
		violation.Remediation = acm.GenerateRemediation(tmpl, nil, violation.RootCause, nil)
		return violation
	}

	// Override with template-level names if GVR extraction missed them
	if resName == "" {
		resName = tmpl.ResourceName
	}
	if resNamespace == "" {
		resNamespace = tmpl.ResourceNamespace
	}

	violation.Resource = &acm.ResourceInfo{
		APIVersion: tmpl.APIVersion,
		Kind:       tmpl.Kind,
		Name:       resName,
		Namespace:  resNamespace,
	}

	// Step 0: Fetch the actual resource from the cluster
	var actual *unstructured.Unstructured
	if resName != "" {
		actual, err = inspector.GetResource(ctx, gvr, resName, resNamespace)
		if err != nil {
			logger.Debug("Resource not found in cluster",
				"kind", tmpl.Kind,
				"name", resName,
				"namespace", resNamespace,
				"error", err,
			)
			violation.Resource.Exists = false
		} else {
			violation.Resource.Exists = true
		}
	}

	// Compute field-level diff when resource exists
	if actual != nil && len(tmpl.ObjectDefinition) > 0 {
		violation.DifferingFields = acm.ComputeFieldDiffs(tmpl.ObjectDefinition, actual)
	}

	// Run the root cause analysis decision tree
	var trace *acm.DiagnosticTrace
	if verbose {
		trace = &acm.DiagnosticTrace{}
	}
	rootCause := runRootCauseAnalysis(ctx, logger, inspector, lister, actual, gvr, resName, resNamespace, tmpl, trace)
	violation.RootCause = rootCause
	violation.Trace = trace

	// Set status based on root cause
	if rootCause.PrimaryCause == acm.CauseDirectFixApplicable {
		violation.Status = acm.StatusCompliant
	} else {
		violation.Status = acm.StatusNeedsAction
	}

	// Generate remediation
	var ownerResult *acm.OwnershipResult
	if actual != nil {
		ownerResult = k8s.AnalyzeOwnership(actual)
	}
	violation.Remediation = acm.GenerateRemediation(tmpl, actual, rootCause, ownerResult)

	return violation
}

// addTraceStep appends a step to the diagnostic trace if tracing is enabled (trace != nil).
func addTraceStep(trace *acm.DiagnosticTrace, name, status, duration, finding, decision string) {
	if trace == nil {
		return
	}
	trace.Steps = append(trace.Steps, acm.TraceStep{
		Name:     name,
		Status:   status,
		Duration: duration,
		Finding:  finding,
		Decision: decision,
	})
}

// skipRemainingSteps marks all remaining steps as skipped in the trace.
func skipRemainingSteps(trace *acm.DiagnosticTrace, stepNames []string, reason string) {
	if trace == nil {
		return
	}
	for _, name := range stepNames {
		addTraceStep(trace, name, acm.TraceStatusSkipped, "0ms", "Not executed", reason)
	}
}

// runRootCauseAnalysis executes the root cause analysis decision tree.
// When the resource is missing, it investigates why (namespace health, CRD existence, RBAC).
// When the resource exists, it runs the full 5-step analysis plus OLM-specific checks.
// When trace is non-nil, each step's execution details are recorded.
func runRootCauseAnalysis(ctx context.Context, logger *slog.Logger, inspector acm.ResourceInspector, lister acm.PolicyLister,
	actual *unstructured.Unstructured, gvr schema.GroupVersionResource, name, namespace string, tmpl *acm.ConfigPolicyTemplate,
	trace *acm.DiagnosticTrace) *acm.RootCauseResult {

	// Step 0: Resource existence check
	stepStart := time.Now()
	if actual == nil {
		addTraceStep(trace, "resource_lookup", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("%s '%s' not found in namespace '%s'", tmpl.Kind, name, namespace),
			acm.TraceDecisionContinue)

		// Investigate WHY the resource is missing instead of short-circuiting
		return analyzeResourceNotFound(ctx, logger, inspector, gvr, name, namespace, tmpl, trace)
	}
	addTraceStep(trace, "resource_lookup", acm.TraceStatusExecuted,
		time.Since(stepStart).String(),
		fmt.Sprintf("%s '%s' exists in cluster", tmpl.Kind, name),
		acm.TraceDecisionContinue)

	// Resource exists — check if it's an OLM Subscription with domain-specific issues
	if k8s.IsOLMSubscription(tmpl) {
		logger.Debug("Running OLM Subscription analysis", "resource", name)
		stepStart = time.Now()
		olmResult := k8s.AnalyzeSubscription(actual, tmpl.ObjectDefinition)
		if olmCause := k8s.SubscriptionRootCause(olmResult); olmCause != nil {
			addTraceStep(trace, "olm_analysis", acm.TraceStatusExecuted,
				time.Since(stepStart).String(),
				fmt.Sprintf("OLM issue: %s (state=%s, approval=%s)", olmCause.PrimaryCause, olmResult.CurrentState, olmResult.InstallPlanApproval),
				acm.TraceDecisionRootCauseFound)
			return olmCause
		}
		addTraceStep(trace, "olm_analysis", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			"No OLM-specific issues found",
			acm.TraceDecisionContinue)
	} else {
		addTraceStep(trace, "olm_analysis", acm.TraceStatusSkipped,
			"0ms", "Not an OLM Subscription", acm.TraceDecisionSkippedResourceExists)
	}

	allResourceExistsSteps := []string{"ownership", "dependencies", "mutability", "conflicts", "events"}

	// Step 1: Ownership analysis
	logger.Debug("Step 1: Checking ownership", "resource", name)
	stepStart = time.Now()
	ownerResult := k8s.AnalyzeOwnership(actual)
	if ownerResult.HasExternalOwner {
		var evidence []acm.Evidence
		ownerNames := make([]string, 0, len(ownerResult.Owners))
		for _, owner := range ownerResult.Owners {
			evidence = append(evidence, acm.Evidence{
				Type:   owner.Type,
				Detail: fmt.Sprintf("Managed by: %s", owner.Manager),
			})
			ownerNames = append(ownerNames, owner.Manager)
		}
		addTraceStep(trace, "ownership", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("External owner(s) found: %s", strings.Join(ownerNames, ", ")),
			acm.TraceDecisionRootCauseFound)
		skipRemainingSteps(trace, allResourceExistsSteps[1:], "skipped_root_cause_found_at_ownership")

		return &acm.RootCauseResult{
			PrimaryCause: acm.CauseActiveReconciliation,
			Confidence:   acm.ConfidenceHigh,
			Detail: fmt.Sprintf("Resource is managed by an external controller (%s). "+
				"Direct patches will be reverted.", ownerResult.Owners[0].Manager),
			Evidence: evidence,
		}
	}
	addTraceStep(trace, "ownership", acm.TraceStatusExecuted,
		time.Since(stepStart).String(),
		"No external owners detected",
		acm.TraceDecisionContinue)

	// Step 2: Dependency validation
	logger.Debug("Step 2: Validating dependencies", "resource", name)
	stepStart = time.Now()
	deps := k8s.ValidateDependencies(ctx, inspector, tmpl.ObjectDefinition, namespace)
	for _, dep := range deps {
		if !dep.Exists {
			addTraceStep(trace, "dependencies", acm.TraceStatusExecuted,
				time.Since(stepStart).String(),
				fmt.Sprintf("Missing dependency: %s '%s'", dep.Type, dep.Name),
				acm.TraceDecisionRootCauseFound)
			skipRemainingSteps(trace, allResourceExistsSteps[2:], "skipped_root_cause_found_at_dependencies")

			return &acm.RootCauseResult{
				PrimaryCause: acm.CauseMissingDependency,
				Confidence:   acm.ConfidenceHigh,
				Detail:       fmt.Sprintf("Missing dependency: %s/%s in namespace '%s'", dep.Type, dep.Name, dep.Namespace),
				Evidence: []acm.Evidence{
					{Type: "dependency_check", Detail: fmt.Sprintf("%s '%s' does not exist: %s", dep.Type, dep.Name, dep.Error)},
				},
			}
		}
	}
	depCount := len(deps)
	depMsg := "No dependencies to check"
	if depCount > 0 {
		depMsg = fmt.Sprintf("All %d dependencies exist", depCount)
	}
	addTraceStep(trace, "dependencies", acm.TraceStatusExecuted,
		time.Since(stepStart).String(), depMsg, acm.TraceDecisionContinue)

	// Step 3: Mutability check (dry-run patch)
	logger.Debug("Step 3: Checking mutability", "resource", name)
	stepStart = time.Now()
	patchYAML, _ := acm.ObjectDefToYAML(tmpl.ObjectDefinition)
	if patchYAML != "" {
		patchJSON, _ := json.Marshal(tmpl.ObjectDefinition)
		mutResult := k8s.CheckMutability(ctx, inspector, gvr, name, namespace, patchJSON)
		if !mutResult.Mutable {
			cause := mapMutabilityReason(mutResult.Reason)
			addTraceStep(trace, "mutability", acm.TraceStatusExecuted,
				time.Since(stepStart).String(),
				fmt.Sprintf("Patch rejected: %s", mutResult.Reason),
				acm.TraceDecisionRootCauseFound)
			skipRemainingSteps(trace, allResourceExistsSteps[3:], "skipped_root_cause_found_at_mutability")

			return &acm.RootCauseResult{
				PrimaryCause: cause,
				Confidence:   acm.ConfidenceHigh,
				Detail:       fmt.Sprintf("Patch rejected: %s", mutResult.Detail),
				Evidence: []acm.Evidence{
					{Type: "dry_run_patch", Detail: mutResult.Detail},
				},
			}
		}
		addTraceStep(trace, "mutability", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			"Dry-run patch accepted by API server",
			acm.TraceDecisionContinue)
	} else {
		addTraceStep(trace, "mutability", acm.TraceStatusSkipped,
			time.Since(stepStart).String(),
			"No patch content to test",
			acm.TraceDecisionContinue)
	}

	// Step 4: Conflict detection
	logger.Debug("Step 4: Detecting conflicts", "resource", name)
	stepStart = time.Now()
	conflicts := acm.DetectConflicts(ctx, inspector, lister, actual, gvr)
	if len(conflicts) > 0 {
		var evidence []acm.Evidence
		for _, c := range conflicts {
			evidence = append(evidence, acm.Evidence{
				Type:   c.Type,
				Detail: fmt.Sprintf("%s: %s (%s)", c.Name, c.Message, c.Enforcement),
			})
		}

		// Only flag as root cause if there are blocking (non-audit) conflicts
		for _, c := range conflicts {
			enforcement := strings.ToLower(c.Enforcement)
			if enforcement == "deny" || enforcement == "enforce" || enforcement == "fail" {
				addTraceStep(trace, "conflicts", acm.TraceStatusExecuted,
					time.Since(stepStart).String(),
					fmt.Sprintf("Blocking conflict: %s '%s' (%s)", c.Type, c.Name, c.Enforcement),
					acm.TraceDecisionRootCauseFound)
				skipRemainingSteps(trace, allResourceExistsSteps[4:], "skipped_root_cause_found_at_conflicts")

				return &acm.RootCauseResult{
					PrimaryCause: acm.CausePolicyConflict,
					Confidence:   acm.ConfidenceMedium,
					Detail:       fmt.Sprintf("Potential conflict with %s '%s'", c.Type, c.Name),
					Evidence:     evidence,
				}
			}
		}
		addTraceStep(trace, "conflicts", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("%d audit-only conflict(s) found, none blocking", len(conflicts)),
			acm.TraceDecisionContinue)
	} else {
		addTraceStep(trace, "conflicts", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			"No conflicting webhooks or policies found",
			acm.TraceDecisionContinue)
	}

	// Step 5: Event history
	logger.Debug("Step 5: Analyzing events", "resource", name)
	stepStart = time.Now()
	events := k8s.AnalyzeEvents(ctx, inspector, name, namespace, tmpl.Kind)
	warningEvents := filterWarningEvents(events)
	if len(warningEvents) > 0 {
		var evidence []acm.Evidence
		for _, e := range warningEvents {
			if len(evidence) >= 5 {
				break
			}
			evidence = append(evidence, acm.Evidence{
				Type:   "event",
				Detail: fmt.Sprintf("[%s] %s: %s (x%d)", e.Type, e.Reason, e.Message, e.Count),
			})
		}

		addTraceStep(trace, "events", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("%d warning event(s) found; most recent: %s", len(warningEvents), warningEvents[0].Reason),
			acm.TraceDecisionRootCauseFound)

		// Events provide hints but don't definitively identify root cause
		return &acm.RootCauseResult{
			PrimaryCause: acm.CauseUnknown,
			Confidence:   acm.ConfidenceLow,
			Detail:       fmt.Sprintf("Warning events found for the resource. Most recent: %s - %s", warningEvents[0].Reason, warningEvents[0].Message),
			Evidence:     evidence,
		}
	}
	addTraceStep(trace, "events", acm.TraceStatusExecuted,
		time.Since(stepStart).String(),
		"No warning events found",
		acm.TraceDecisionContinue)

	// No blockers found — direct fix should work
	return &acm.RootCauseResult{
		PrimaryCause: acm.CauseDirectFixApplicable,
		Confidence:   acm.ConfidenceHigh,
		Detail:       "No blocking issues detected. The resource can be patched directly to achieve compliance.",
	}
}

// analyzeResourceNotFound investigates why a resource doesn't exist by checking:
//  1. Namespace health (does the target namespace exist? is it Terminating?)
//  2. CRD registration (is the API type installed on the cluster?)
//  3. RBAC visibility (can we even see this resource type?)
//  4. Events in the namespace (deletion/warning events)
func analyzeResourceNotFound(ctx context.Context, logger *slog.Logger, inspector acm.ResourceInspector,
	gvr schema.GroupVersionResource, name, namespace string, tmpl *acm.ConfigPolicyTemplate,
	trace *acm.DiagnosticTrace) *acm.RootCauseResult {

	var evidence []acm.Evidence
	evidence = append(evidence, acm.Evidence{Type: "resource_lookup", Detail: "Resource not found in cluster"})

	// Sub-step 1: Namespace health
	logger.Debug("Investigating missing resource: checking namespace health", "namespace", namespace)
	stepStart := time.Now()
	if namespace != "" {
		nsResult := k8s.CheckNamespaceHealth(ctx, inspector, namespace)
		if nsResult != nil {
			if !nsResult.Exists {
				addTraceStep(trace, "namespace_health", acm.TraceStatusExecuted,
					time.Since(stepStart).String(),
					fmt.Sprintf("Namespace '%s' does not exist", namespace),
					acm.TraceDecisionRootCauseFound)
				skipRemainingSteps(trace, []string{"crd_check", "rbac_check", "events"}, "skipped_namespace_missing")

				return &acm.RootCauseResult{
					PrimaryCause: acm.CauseNamespaceMissing,
					Confidence:   acm.ConfidenceHigh,
					Detail: fmt.Sprintf("The target namespace '%s' does not exist. "+
						"The %s '%s' cannot exist without its namespace.", namespace, tmpl.Kind, name),
					Evidence: append(evidence, acm.Evidence{
						Type:   "namespace_check",
						Detail: fmt.Sprintf("Namespace '%s' not found", namespace),
					}),
				}
			}
			if !nsResult.Healthy {
				addTraceStep(trace, "namespace_health", acm.TraceStatusExecuted,
					time.Since(stepStart).String(),
					fmt.Sprintf("Namespace '%s' is in '%s' phase", namespace, nsResult.Phase),
					acm.TraceDecisionRootCauseFound)
				skipRemainingSteps(trace, []string{"crd_check", "rbac_check", "events"}, "skipped_namespace_terminating")

				return &acm.RootCauseResult{
					PrimaryCause: acm.CauseNamespaceTerminating,
					Confidence:   acm.ConfidenceHigh,
					Detail: fmt.Sprintf("The namespace '%s' is in '%s' phase. "+
						"Resources in this namespace are being deleted and new resources cannot be created. "+
						"This typically indicates the namespace or its operator was uninstalled.",
						namespace, nsResult.Phase),
					Evidence: append(evidence, acm.Evidence{
						Type:   "namespace_check",
						Detail: fmt.Sprintf("Namespace '%s' phase: %s", namespace, nsResult.Phase),
					}),
				}
			}
			addTraceStep(trace, "namespace_health", acm.TraceStatusExecuted,
				time.Since(stepStart).String(),
				fmt.Sprintf("Namespace '%s' exists and is Active", namespace),
				acm.TraceDecisionContinue)
		}
	} else {
		addTraceStep(trace, "namespace_health", acm.TraceStatusSkipped,
			"0ms", "Cluster-scoped resource, no namespace to check", acm.TraceDecisionContinue)
	}

	// Sub-step 2: CRD registration
	logger.Debug("Investigating missing resource: checking CRD registration")
	stepStart = time.Now()
	crdExists, crdName := k8s.CheckCRDRegistered(ctx, inspector, tmpl.Kind, tmpl.APIVersion)
	if !crdExists {
		addTraceStep(trace, "crd_check", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("CRD '%s' is not installed", crdName),
			acm.TraceDecisionRootCauseFound)
		skipRemainingSteps(trace, []string{"rbac_check", "events"}, "skipped_crd_missing")

		return &acm.RootCauseResult{
			PrimaryCause: acm.CauseAPINotRegistered,
			Confidence:   acm.ConfidenceHigh,
			Detail: fmt.Sprintf("The Custom Resource Definition '%s' is not installed on this cluster. "+
				"The %s resource type is not available. The CRD must be installed first (typically by installing the corresponding operator).",
				crdName, tmpl.Kind),
			Evidence: append(evidence, acm.Evidence{
				Type:   "crd_check",
				Detail: fmt.Sprintf("CRD '%s' not found", crdName),
			}),
		}
	}
	if crdName != "" {
		addTraceStep(trace, "crd_check", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("CRD '%s' is installed", crdName),
			acm.TraceDecisionContinue)
	} else {
		addTraceStep(trace, "crd_check", acm.TraceStatusSkipped,
			time.Since(stepStart).String(),
			"Core API resource, no CRD to check",
			acm.TraceDecisionContinue)
	}

	// Sub-step 3: RBAC visibility
	logger.Debug("Investigating missing resource: checking RBAC visibility")
	stepStart = time.Now()
	allowed, err := k8s.CheckRBACVisibility(ctx, inspector, gvr, namespace)
	switch {
	case err != nil:
		addTraceStep(trace, "rbac_check", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("RBAC check failed: %v", err),
			acm.TraceDecisionContinue)
	case !allowed:
		addTraceStep(trace, "rbac_check", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("No GET permission for %s in namespace '%s'", gvr.Resource, namespace),
			acm.TraceDecisionRootCauseFound)
		skipRemainingSteps(trace, []string{"events"}, "skipped_rbac_denied")

		return &acm.RootCauseResult{
			PrimaryCause: acm.CauseRBACNotVisible,
			Confidence:   acm.ConfidenceMedium,
			Detail: fmt.Sprintf("The service account does not have permission to GET %s resources in namespace '%s'. "+
				"The resource may exist but is not visible due to RBAC restrictions.", gvr.Resource, namespace),
			Evidence: append(evidence, acm.Evidence{
				Type:   "rbac_check",
				Detail: fmt.Sprintf("GET %s/%s in '%s' denied", gvr.Group, gvr.Resource, namespace),
			}),
		}
	default:
		addTraceStep(trace, "rbac_check", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("GET permission confirmed for %s in namespace '%s'", gvr.Resource, namespace),
			acm.TraceDecisionContinue)
	}

	// Sub-step 4: Events in namespace
	logger.Debug("Investigating missing resource: checking events")
	stepStart = time.Now()
	events := k8s.AnalyzeEvents(ctx, inspector, name, namespace, tmpl.Kind)
	warningEvents := filterWarningEvents(events)
	if len(warningEvents) > 0 {
		var eventEvidence []acm.Evidence
		for _, e := range warningEvents {
			if len(eventEvidence) >= 5 {
				break
			}
			eventEvidence = append(eventEvidence, acm.Evidence{
				Type:   "event",
				Detail: fmt.Sprintf("[%s] %s: %s (x%d)", e.Type, e.Reason, e.Message, e.Count),
			})
		}
		addTraceStep(trace, "events", acm.TraceStatusExecuted,
			time.Since(stepStart).String(),
			fmt.Sprintf("%d warning event(s) found for missing resource", len(warningEvents)),
			acm.TraceDecisionRootCauseFound)

		return &acm.RootCauseResult{
			PrimaryCause: acm.CauseResourceNotFound,
			Confidence:   acm.ConfidenceMedium,
			Detail: fmt.Sprintf("The %s '%s' does not exist in namespace '%s'. "+
				"Warning events were found which may explain the absence: %s - %s",
				tmpl.Kind, name, namespace, warningEvents[0].Reason, warningEvents[0].Message),
			Evidence: append(evidence, eventEvidence...),
		}
	}
	addTraceStep(trace, "events", acm.TraceStatusExecuted,
		time.Since(stepStart).String(),
		"No warning events found for missing resource",
		acm.TraceDecisionContinue)

	// Default: genuinely missing with no additional explanation
	return &acm.RootCauseResult{
		PrimaryCause: acm.CauseResourceNotFound,
		Confidence:   acm.ConfidenceHigh,
		Detail: fmt.Sprintf("The %s '%s' does not exist in namespace '%s'. "+
			"The namespace is healthy, the API type is registered, and RBAC allows visibility. "+
			"The resource was likely never created or was deleted.",
			tmpl.Kind, name, namespace),
		Evidence: evidence,
	}
}

// mapMutabilityReason maps a MutabilityResult reason to a root cause constant.
func mapMutabilityReason(reason string) string {
	switch reason {
	case "immutable_field":
		return acm.CauseImmutableField
	case "webhook_denied":
		return acm.CauseAdmissionBlocked
	case "rbac_denied":
		return acm.CauseRBACDenied
	case "quota_exceeded":
		return acm.CauseQuotaExceeded
	default:
		return acm.CauseUnknown
	}
}

// filterWarningEvents returns only Warning-type events.
func filterWarningEvents(events []acm.EventInfo) []acm.EventInfo {
	var warnings []acm.EventInfo
	for _, e := range events {
		if e.Type == "Warning" {
			warnings = append(warnings, e)
		}
	}
	return warnings
}

// buildDiagnosisSummary generates a human-readable summary of the diagnosis.
// Returns the summary string, the needs_action count, and the compliant count.
func buildDiagnosisSummary(policy *acm.PolicyDetail, violations []acm.ViolationDetail) (string, int, int) {
	if len(violations) == 0 {
		return fmt.Sprintf("Policy '%s' has no object templates to analyze.", policy.Name), 0, 0
	}

	needsAction := 0
	compliant := 0
	causeCounts := make(map[string]int)

	for _, v := range violations {
		if v.Status == acm.StatusCompliant {
			compliant++
		} else {
			needsAction++
		}
		if v.RootCause != nil {
			causeCounts[v.RootCause.PrimaryCause]++
		}
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("Policy '%s/%s' — %d of %d template(s) need action, %d already compliant.",
		policy.Namespace, policy.Name, needsAction, len(violations), compliant))

	// Group actionable causes (exclude compliant)
	for cause, count := range causeCounts {
		if cause == acm.CauseDirectFixApplicable {
			continue // Already reported as "compliant" above
		}
		parts = append(parts, fmt.Sprintf("- %d template(s): %s", count, humanReadableCause(cause)))
	}

	if compliant > 0 {
		parts = append(parts, fmt.Sprintf("- %d template(s): already compliant (no action needed)", compliant))
	}

	return strings.Join(parts, "\n"), needsAction, compliant
}

// humanReadableCause converts a root cause constant to a human-readable string.
func humanReadableCause(cause string) string {
	switch cause {
	case acm.CauseActiveReconciliation:
		return "external controller actively managing the resource"
	case acm.CauseMissingDependency:
		return "missing dependent resource"
	case acm.CauseImmutableField:
		return "immutable field (resource must be recreated)"
	case acm.CauseAdmissionBlocked:
		return "admission controller blocking the change"
	case acm.CauseRBACDenied:
		return "insufficient RBAC permissions"
	case acm.CauseQuotaExceeded:
		return "resource quota exceeded"
	case acm.CausePolicyConflict:
		return "conflicting policy or webhook"
	case acm.CauseDirectFixApplicable:
		return "direct fix applicable (no blockers found)"
	case acm.CauseResourceNotFound:
		return "resource does not exist (needs to be created)"
	case acm.CauseNamespaceTerminating:
		return "target namespace is terminating"
	case acm.CauseNamespaceMissing:
		return "target namespace does not exist"
	case acm.CauseAPINotRegistered:
		return "API type (CRD) not installed on cluster"
	case acm.CauseRBACNotVisible:
		return "resource may not be visible due to RBAC restrictions"
	case acm.CauseSubscriptionPendingApproval:
		return "OLM subscription waiting for manual InstallPlan approval"
	default:
		return "unknown (manual investigation needed)"
	}
}

// resolveInspector checks if the connected cluster is an ACM hub and, if so, extracts
// the managed cluster's kubeconfig from hub secrets to create a remote inspector.
// Falls back to the original inspector if hub detection fails or kubeconfig is unavailable.
// Returns the inspector, a mode string, and hub diagnostics.
func resolveInspector(ctx context.Context, logger *slog.Logger, original acm.ResourceInspector, policy *acm.PolicyDetail, explicitCluster string) (acm.ResourceInspector, string, *acm.HubDiagnostic) {
	hubInfo, hubDiag := k8s.DetectHub(ctx, original)
	if !hubInfo.IsHub {
		logger.Debug("Not a hub cluster, using direct resource inspector")
		return original, "direct", nil
	}

	// Determine which managed cluster to target.
	var targetCluster string
	if explicitCluster != "" {
		targetCluster = explicitCluster
		logger.Info("Using explicitly specified managed cluster", "cluster", targetCluster)
	} else {
		clusters := k8s.ExtractManagedClusters(ctx, original, policy)
		if len(clusters) == 0 {
			logger.Warn("Hub cluster detected but could not determine managed cluster, using direct inspector")
			return original, "direct (hub detected, no managed cluster in policy status)", hubDiag
		}
		targetCluster = clusters[0]
		if len(clusters) > 1 {
			logger.Info("Multiple non-compliant managed clusters found, inspecting first",
				"target", targetCluster,
				"all", clusters,
			)
		}
	}
	hubDiag.TargetCluster = targetCluster

	// Extract the managed cluster kubeconfig from hub secrets.
	managedConfig, source, err := ExtractManagedClusterKubeconfig(ctx, original, targetCluster)
	if err != nil {
		logger.Warn("Failed to extract managed cluster kubeconfig, falling back to hub inspector",
			"cluster", targetCluster,
			"error", err,
		)
		hubDiag.Error = err.Error()
		return original, fmt.Sprintf("direct (hub, kubeconfig not found for %s)", targetCluster), hubDiag
	}
	hubDiag.KubeconfigSource = source

	// Create a new DefaultResourceInspector targeting the managed cluster.
	remoteInspector, err := acm.DefaultACMFactory.NewResourceInspector(managedConfig)
	if err != nil {
		logger.Warn("Failed to create remote inspector from managed cluster kubeconfig",
			"cluster", targetCluster,
			"error", err,
		)
		hubDiag.Error = fmt.Sprintf("kubeconfig extracted but client creation failed: %v", err)
		return original, fmt.Sprintf("direct (hub, client error for %s)", targetCluster), hubDiag
	}

	logger.Info("Using remote inspector for managed cluster",
		"cluster", targetCluster,
		"kubeconfigSource", source,
	)

	return remoteInspector, fmt.Sprintf("remote:%s", targetCluster), hubDiag
}

// detectCascadingFailures identifies violations that are likely consequences of other
// violations rather than independent root causes. For example:
//   - OLM Operator status mismatches when the corresponding Subscription is also non-compliant
//   - Resources in a namespace that is Terminating or missing
//
// Cascading violations are annotated with CascadingFrom pointing to the primary violation.
func detectCascadingFailures(violations []acm.ViolationDetail) {
	// Build indexes for lookups
	subscriptionIssues := make(map[string]string) // namespace -> root cause description
	namespaceIssues := make(map[string]string)    // namespace -> root cause description

	for _, v := range violations {
		if v.RootCause == nil {
			continue
		}
		// Track subscription-level issues
		if k8s.IsOLMSubscription(&v.Template) && v.Status == acm.StatusNeedsAction {
			ns := v.Template.ResourceNamespace
			subscriptionIssues[ns] = fmt.Sprintf("%s/%s (%s)", v.Template.Kind, v.Template.ResourceName, v.RootCause.PrimaryCause)
		}
		// Track namespace-level issues
		if v.RootCause.PrimaryCause == acm.CauseNamespaceTerminating || v.RootCause.PrimaryCause == acm.CauseNamespaceMissing {
			if v.Template.Kind == "Namespace" {
				namespaceIssues[v.Template.ResourceName] = v.RootCause.PrimaryCause
			} else if v.Resource != nil && v.Resource.Namespace != "" {
				namespaceIssues[v.Resource.Namespace] = v.RootCause.PrimaryCause
			}
		}
	}

	// Mark cascading failures
	for i := range violations {
		v := &violations[i]
		if v.RootCause == nil || v.CascadingFrom != "" {
			continue
		}

		// OLM Operator objects cascade from Subscription issues in the same namespace
		if k8s.IsOLMOperator(&v.Template) && v.Status == acm.StatusNeedsAction {
			ns := v.Template.ResourceNamespace
			if cause, ok := subscriptionIssues[ns]; ok {
				v.CascadingFrom = cause
			}
		}

		// Resources in a Terminating/missing namespace cascade from the namespace issue
		if v.Resource != nil && v.Resource.Namespace != "" {
			if cause, ok := namespaceIssues[v.Resource.Namespace]; ok {
				// Don't mark the namespace itself as cascading
				if v.Template.Kind != "Namespace" {
					v.CascadingFrom = fmt.Sprintf("Namespace '%s' is %s", v.Resource.Namespace, cause)
				}
			}
		}
	}
}
