// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

// OLM Subscription API group.
const (
	olmGroup      = "operators.coreos.com"
	olmAPIVersion = "operators.coreos.com/v1alpha1"
)

// AnalyzeSubscription performs OLM-specific analysis when the target resource is a Subscription.
// It inspects installPlanApproval, current state, and catalog health to identify
// Subscription-specific root causes (e.g. pending manual approval).
//
// Returns nil if the resource is not a Subscription or no OLM-specific issue is found.
func AnalyzeSubscription(actual *unstructured.Unstructured, desired map[string]interface{}) *acm.OLMAnalysisResult {
	result := &acm.OLMAnalysisResult{}

	// Extract actual state
	result.InstallPlanApproval, _, _ = unstructured.NestedString(actual.Object, "spec", "installPlanApproval")
	result.CurrentState, _, _ = unstructured.NestedString(actual.Object, "status", "state")
	result.CurrentCSV, _, _ = unstructured.NestedString(actual.Object, "status", "currentCSV")
	result.InstalledCSV, _, _ = unstructured.NestedString(actual.Object, "status", "installedCSV")

	// Extract expected state from desired spec (if specified in policy)
	if statusMap, ok := desired["status"].(map[string]interface{}); ok {
		if state, ok := statusMap["state"].(string); ok {
			result.ExpectedState = state
		}
	}

	// Check catalog health from status.catalogHealth
	catalogHealth, _, _ := unstructured.NestedSlice(actual.Object, "status", "catalogHealth")
	allHealthy := true
	for _, ch := range catalogHealth {
		if chMap, ok := ch.(map[string]interface{}); ok {
			healthy, _, _ := unstructured.NestedBool(chMap, "healthy")
			if !healthy {
				allHealthy = false
				ref, _, _ := unstructured.NestedString(chMap, "catalogSourceRef", "name")
				result.CatalogHealth = fmt.Sprintf("unhealthy: %s", ref)
				break
			}
		}
	}
	if allHealthy && len(catalogHealth) > 0 {
		result.CatalogHealth = "healthy"
	}

	return result
}

// IsSubscriptionPendingApproval returns true if the OLM analysis indicates
// the subscription is waiting for manual InstallPlan approval.
func IsSubscriptionPendingApproval(olmResult *acm.OLMAnalysisResult) bool {
	if olmResult == nil {
		return false
	}
	return olmResult.InstallPlanApproval == "Manual" &&
		olmResult.CurrentState == "UpgradePending"
}

// SubscriptionRootCause generates a RootCauseResult for a Subscription-specific issue.
func SubscriptionRootCause(olmResult *acm.OLMAnalysisResult) *acm.RootCauseResult {
	if IsSubscriptionPendingApproval(olmResult) {
		detail := fmt.Sprintf(
			"Subscription has installPlanApproval: Manual and status.state: %s (expected: %s). "+
				"A new operator version is available but the InstallPlan has not been approved.",
			olmResult.CurrentState, olmResult.ExpectedState)

		if olmResult.CurrentCSV != "" && olmResult.InstalledCSV != "" && olmResult.CurrentCSV != olmResult.InstalledCSV {
			detail += fmt.Sprintf(" Current CSV: %s, Installed CSV: %s.", olmResult.CurrentCSV, olmResult.InstalledCSV)
		}

		return &acm.RootCauseResult{
			PrimaryCause: acm.CauseSubscriptionPendingApproval,
			Confidence:   acm.ConfidenceHigh,
			Detail:       detail,
			Evidence: []acm.Evidence{
				{Type: "olm_subscription_state", Detail: fmt.Sprintf("state=%s, installPlanApproval=%s", olmResult.CurrentState, olmResult.InstallPlanApproval)},
			},
		}
	}

	// Check for unhealthy catalog source
	if olmResult.CatalogHealth != "" && olmResult.CatalogHealth != "healthy" {
		return &acm.RootCauseResult{
			PrimaryCause: acm.CauseMissingDependency,
			Confidence:   acm.ConfidenceMedium,
			Detail:       fmt.Sprintf("CatalogSource is %s. The operator subscription cannot resolve available updates.", olmResult.CatalogHealth),
			Evidence: []acm.Evidence{
				{Type: "olm_catalog_health", Detail: olmResult.CatalogHealth},
			},
		}
	}

	return nil
}

// IsOLMSubscription checks whether a template targets an OLM Subscription resource.
func IsOLMSubscription(tmpl *acm.ConfigPolicyTemplate) bool {
	group, _ := acm.ParseAPIVersion(tmpl.APIVersion)
	return group == olmGroup && tmpl.Kind == "Subscription"
}

// IsOLMOperator checks whether a template targets an OLM Operator resource.
func IsOLMOperator(tmpl *acm.ConfigPolicyTemplate) bool {
	group, _ := acm.ParseAPIVersion(tmpl.APIVersion)
	return group == olmGroup && tmpl.Kind == "Operator"
}
