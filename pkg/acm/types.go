// SPDX-License-Identifier: Apache-2.0

package acm

import "errors"

// ACM-specific error sentinels.
var (
	// ErrACMNotInstalled indicates that ACM Policy CRDs are not installed on the cluster.
	ErrACMNotInstalled = errors.New("ACM policy framework not installed")

	// ErrPolicyNotFound indicates the requested policy was not found.
	ErrPolicyNotFound = errors.New("policy not found")
)

// --- Detection types ---

// DetectViolationsResult is the structured output of the acm_detect_violations tool.
type DetectViolationsResult struct {
	TotalPolicies int             `json:"total_policies"`
	Compliant     int             `json:"compliant"`
	NonCompliant  int             `json:"non_compliant"`
	Pending       int             `json:"pending,omitempty"`
	Violations    []PolicySummary `json:"violations"`
}

// PolicySummary is a lightweight violation summary returned by the detect tool.
type PolicySummary struct {
	Name              string          `json:"name"`
	Namespace         string          `json:"namespace"`
	Compliant         string          `json:"compliant"`
	Severity          string          `json:"severity,omitempty"`
	RemediationAction string          `json:"remediation_action,omitempty"`
	Message           string          `json:"message,omitempty"`
	Categories        []string        `json:"categories,omitempty"`
	AffectedClusters  []ClusterStatus `json:"affected_clusters,omitempty"`
}

// ClusterStatus represents per-cluster compliance status within an ACM policy.
type ClusterStatus struct {
	Name      string `json:"name"`
	Compliant string `json:"compliant"`
}

// --- Diagnosis types ---

// DiagnoseViolationResult is the structured output of the acm_diagnose_violation tool.
type DiagnoseViolationResult struct {
	Policy           PolicyDetail      `json:"policy"`
	InspectionMode   string            `json:"inspection_mode"`          // "direct" or "remote:<cluster>"
	HubDiagnostic    *HubDiagnostic    `json:"hub_diagnostic,omitempty"` // Diagnostic: hub detection details
	NeedsActionCount int               `json:"needs_action_count"`
	CompliantCount   int               `json:"compliant_count"`
	Violations       []ViolationDetail `json:"violations"`
	Summary          string            `json:"summary"`
}

// HubDiagnostic captures details of hub detection and managed cluster kubeconfig extraction.
type HubDiagnostic struct {
	IsHub            bool   `json:"is_hub"`
	TargetCluster    string `json:"target_cluster,omitempty"`
	KubeconfigSource string `json:"kubeconfig_source,omitempty"` // e.g. "ClusterDeployment cnfdg4/cnfdg4-admin-kubeconfig"
	Error            string `json:"error,omitempty"`
}

// PolicyDetail holds the full policy information used by the diagnose tool.
type PolicyDetail struct {
	Name              string                 `json:"name"`
	Namespace         string                 `json:"namespace"`
	Compliant         string                 `json:"compliant"`
	Severity          string                 `json:"severity,omitempty"`
	RemediationAction string                 `json:"remediation_action,omitempty"`
	AffectedClusters  []ClusterStatus        `json:"affected_clusters,omitempty"`
	Templates         []ConfigPolicyTemplate `json:"templates"`
}

// ConfigPolicyTemplate represents a single object-template from a ConfigurationPolicy.
type ConfigPolicyTemplate struct {
	Name              string                 `json:"name"`
	ComplianceType    string                 `json:"compliance_type"`
	ObjectDefinition  map[string]interface{} `json:"object_definition"`
	Kind              string                 `json:"kind"`
	APIVersion        string                 `json:"api_version"`
	ResourceName      string                 `json:"resource_name"`
	ResourceNamespace string                 `json:"resource_namespace,omitempty"`
	ComplianceMessage string                 `json:"compliance_message,omitempty"` // ACM's own compliance message (e.g. "not found", "found but not as specified")
}

// ViolationDetail is the full diagnosis result for a single resource violation.
type ViolationDetail struct {
	Template        ConfigPolicyTemplate `json:"template"`
	Resource        *ResourceInfo        `json:"resource,omitempty"`
	Status          string               `json:"status"` // "needs_action" or "compliant"
	RootCause       *RootCauseResult     `json:"root_cause"`
	DifferingFields []FieldDifference    `json:"differing_fields,omitempty"` // Field-level diff when resource exists but doesn't match
	CascadingFrom   string               `json:"cascading_from,omitempty"`   // Set when this violation is a cascading effect of another
	Remediation     *RemediationPlan     `json:"remediation"`
	Trace           *DiagnosticTrace     `json:"diagnostic_trace,omitempty"` // Present only when verbose=true
}

// FieldDifference describes a single field that differs between desired and actual state.
type FieldDifference struct {
	Path     string      `json:"path"`
	Expected interface{} `json:"expected,omitempty"`
	Actual   interface{} `json:"actual,omitempty"`
}

// Violation status constants.
const (
	StatusNeedsAction = "needs_action"
	StatusCompliant   = "compliant"
)

// ResourceInfo describes a Kubernetes resource that is part of a violation.
type ResourceInfo struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
	Exists     bool   `json:"exists"`
}

// --- Root cause analysis types ---

// RootCauseResult holds the outcome of the root cause analysis decision tree.
type RootCauseResult struct {
	PrimaryCause string     `json:"primary_cause"`
	Confidence   string     `json:"confidence"`
	Detail       string     `json:"detail"`
	Evidence     []Evidence `json:"evidence,omitempty"`
}

// Root cause constants.
const (
	CauseActiveReconciliation        = "active_reconciliation"
	CauseMissingDependency           = "missing_dependency"
	CauseImmutableField              = "immutable_field"
	CauseAdmissionBlocked            = "admission_blocked"
	CauseRBACDenied                  = "rbac_denied"
	CauseQuotaExceeded               = "quota_exceeded"
	CausePolicyConflict              = "policy_conflict"
	CauseDirectFixApplicable         = "direct_fix_applicable"
	CauseResourceNotFound            = "resource_not_found"
	CauseNamespaceTerminating        = "namespace_terminating"
	CauseNamespaceMissing            = "namespace_missing"
	CauseAPINotRegistered            = "api_not_registered"
	CauseRBACNotVisible              = "rbac_not_visible"
	CauseSubscriptionPendingApproval = "subscription_pending_approval"
	CauseUnknown                     = "unknown"
)

// NamespaceHealthResult holds the outcome of a namespace health check.
type NamespaceHealthResult struct {
	Exists  bool   `json:"exists"`
	Phase   string `json:"phase,omitempty"` // "Active", "Terminating"
	Healthy bool   `json:"healthy"`
}

// OLMAnalysisResult holds the outcome of OLM-specific analysis for Subscriptions.
type OLMAnalysisResult struct {
	InstallPlanApproval string `json:"install_plan_approval,omitempty"` // "Manual" or "Automatic"
	CurrentState        string `json:"current_state,omitempty"`         // e.g. "UpgradePending", "AtLatestKnown"
	ExpectedState       string `json:"expected_state,omitempty"`
	CurrentCSV          string `json:"current_csv,omitempty"`
	InstalledCSV        string `json:"installed_csv,omitempty"`
	CatalogHealth       string `json:"catalog_health,omitempty"`
}

// Confidence level constants.
const (
	ConfidenceHigh   = "high"
	ConfidenceMedium = "medium"
	ConfidenceLow    = "low"
)

// Evidence is a single piece of evidence supporting a root cause conclusion.
type Evidence struct {
	Type   string `json:"type"`
	Detail string `json:"detail"`
}

// OwnershipResult is the output of the ownership analysis step.
type OwnershipResult struct {
	HasExternalOwner bool                `json:"has_external_owner"`
	Owners           []OwnershipConflict `json:"owners,omitempty"`
	FixTarget        string              `json:"fix_target,omitempty"`
}

// OwnershipConflict describes a detected external owner of a resource or its fields.
type OwnershipConflict struct {
	Manager    string   `json:"manager"`
	Type       string   `json:"type"`
	Paths      []string `json:"paths,omitempty"`
	Controller bool     `json:"controller,omitempty"`
}

// DependencyCheck describes the result of verifying a single cross-resource reference.
type DependencyCheck struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
	Exists    bool   `json:"exists"`
	Error     string `json:"error,omitempty"`
}

// MutabilityResult is the outcome of a server-side dry-run patch attempt.
type MutabilityResult struct {
	Mutable bool   `json:"mutable"`
	Reason  string `json:"reason,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Blocker string `json:"blocker,omitempty"`
}

// PolicyConflict describes a detected conflict from another policy or admission controller.
type PolicyConflict struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Kind        string `json:"kind,omitempty"`
	Message     string `json:"message,omitempty"`
	Enforcement string `json:"enforcement,omitempty"`
}

// EventInfo describes a Kubernetes event relevant to a resource.
type EventInfo struct {
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Source    string `json:"source"`
	Count     int32  `json:"count"`
	Type      string `json:"type"`
	FirstSeen string `json:"first_seen,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

// WebhookInfo describes a single webhook within a webhook configuration.
type WebhookInfo struct {
	ConfigName  string        `json:"config_name"`
	WebhookName string        `json:"webhook_name"`
	Type        string        `json:"type"`
	Service     string        `json:"service,omitempty"`
	FailPolicy  string        `json:"fail_policy,omitempty"`
	Rules       []WebhookRule `json:"rules,omitempty"`
}

// WebhookRule describes the matching criteria of a single webhook rule.
type WebhookRule struct {
	APIGroups   []string `json:"api_groups"`
	APIVersions []string `json:"api_versions"`
	Resources   []string `json:"resources"`
	Operations  []string `json:"operations"`
}

// --- Remediation types ---

// RemediationPlan is the generated remediation for a violation.
type RemediationPlan struct {
	DirectPatchWorks bool     `json:"direct_patch_works"`
	PatchYAML        string   `json:"patch_yaml,omitempty"`
	PatchSource      string   `json:"patch_source,omitempty"`
	Confidence       string   `json:"confidence"`
	ActualFix        string   `json:"actual_fix"`
	Steps            []string `json:"steps"`
	Warnings         []string `json:"warnings,omitempty"`
	ApplyCommand     string   `json:"apply_command,omitempty"`
}

// --- Diagnostic trace types ---

// DiagnosticTrace records the step-by-step execution of the root cause analysis pipeline.
// Included in the output only when the verbose parameter is true.
type DiagnosticTrace struct {
	Steps []TraceStep `json:"steps"`
}

// TraceStep records what happened at a single RCA step.
type TraceStep struct {
	Name     string `json:"name"`     // Step name: "resource_lookup", "ownership", "dependencies", "mutability", "conflicts", "events"
	Status   string `json:"status"`   // "executed", "skipped"
	Duration string `json:"duration"` // e.g. "12ms"
	Finding  string `json:"finding"`  // Human-readable one-liner of what the step found
	Decision string `json:"decision"` // "continue", "root_cause_found", "skipped_resource_missing"
}

// Trace step status constants.
const (
	TraceStatusExecuted = "executed"
	TraceStatusSkipped  = "skipped"
)

// Trace decision constants.
const (
	TraceDecisionContinue               = "continue"
	TraceDecisionRootCauseFound         = "root_cause_found"
	TraceDecisionSkippedResourceMissing = "skipped_resource_missing"
	TraceDecisionSkippedResourceExists  = "skipped_resource_exists"
)
