// SPDX-License-Identifier: Apache-2.0

package acm

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// GenerateRemediation produces a remediation plan using a priority cascade:
//  1. Extract objectDefinition from the policy (high confidence)
//  2. Generate merge patch from diff if actual resource exists (high confidence)
//  3. Fall back to structured context for AI assistant (low confidence)
func GenerateRemediation(template *ConfigPolicyTemplate, actual *unstructured.Unstructured, rootCause *RootCauseResult, ownerResult *OwnershipResult) *RemediationPlan {
	logger := slog.Default()

	// Handle mustnothave: remediation is deletion
	if strings.EqualFold(template.ComplianceType, "mustnothave") {
		return generateDeletionRemediation(template)
	}

	// Determine if direct patching will work based on root cause
	directPatchWorks := rootCause.PrimaryCause == CauseDirectFixApplicable ||
		rootCause.PrimaryCause == CauseResourceNotFound ||
		rootCause.PrimaryCause == CauseSubscriptionPendingApproval

	// 1. Try to extract remediation from the policy objectDefinition
	if len(template.ObjectDefinition) > 0 {
		patchYAML, err := ObjectDefToYAML(template.ObjectDefinition)
		if err != nil {
			logger.Debug("Failed to convert objectDefinition to YAML", "error", err)
		} else {
			plan := &RemediationPlan{
				DirectPatchWorks: directPatchWorks,
				PatchYAML:        patchYAML,
				PatchSource:      "policy_object_definition",
				Confidence:       ConfidenceHigh,
				ActualFix:        describeActualFix(rootCause, ownerResult, template),
				Steps:            generateSteps(rootCause, ownerResult, template, patchYAML),
			}

			if !directPatchWorks && ownerResult != nil && ownerResult.HasExternalOwner {
				plan.Warnings = append(plan.Warnings,
					fmt.Sprintf("Direct patching may be reverted by %s", ownerResult.Owners[0].Manager))
			}

			if directPatchWorks {
				ns := template.ResourceNamespace
				if ns != "" {
					plan.ApplyCommand = fmt.Sprintf("kubectl apply -f remediation.yaml -n %s", ns)
				} else {
					plan.ApplyCommand = "kubectl apply -f remediation.yaml"
				}
			}

			return plan
		}
	}

	// 2. If the resource exists, try to generate a merge patch
	if actual != nil && len(template.ObjectDefinition) > 0 {
		patchYAML, err := generateMergePatch(template.ObjectDefinition, actual)
		if err != nil {
			logger.Debug("Failed to generate merge patch", "error", err)
		} else if patchYAML != "" {
			return &RemediationPlan{
				DirectPatchWorks: directPatchWorks,
				PatchYAML:        patchYAML,
				PatchSource:      "merge_patch",
				Confidence:       ConfidenceHigh,
				ActualFix:        describeActualFix(rootCause, ownerResult, template),
				Steps:            generateSteps(rootCause, ownerResult, template, patchYAML),
			}
		}
	}

	// 3. Fallback: structured context for the AI assistant
	return &RemediationPlan{
		DirectPatchWorks: directPatchWorks,
		PatchSource:      "none",
		Confidence:       ConfidenceLow,
		ActualFix:        describeActualFix(rootCause, ownerResult, template),
		Steps: []string{
			fmt.Sprintf("Investigate the %s/%s resource manually", template.Kind, template.ResourceName),
			"Review the root cause analysis for blocking issues",
			"Apply the required changes once the root cause is resolved",
		},
	}
}

// generateDeletionRemediation creates a plan for mustnothave violations.
func generateDeletionRemediation(template *ConfigPolicyTemplate) *RemediationPlan {
	cmd := fmt.Sprintf("kubectl delete %s %s",
		strings.ToLower(template.Kind),
		template.ResourceName)

	if template.ResourceNamespace != "" {
		cmd += fmt.Sprintf(" -n %s", template.ResourceNamespace)
	}

	return &RemediationPlan{
		DirectPatchWorks: true,
		PatchSource:      "deletion",
		Confidence:       ConfidenceHigh,
		ActualFix:        fmt.Sprintf("Delete the %s/%s resource", template.Kind, template.ResourceName),
		Steps: []string{
			fmt.Sprintf("Delete the resource: %s", cmd),
			"Verify the policy becomes compliant",
		},
		ApplyCommand: cmd,
		Warnings: []string{
			"This will permanently delete the resource. Ensure it is not needed before proceeding.",
		},
	}
}

// objectDefToYAML converts an objectDefinition map to a YAML string.
func ObjectDefToYAML(objDef map[string]interface{}) (string, error) {
	yamlBytes, err := yaml.Marshal(objDef)
	if err != nil {
		return "", fmt.Errorf("failed to marshal objectDefinition to YAML: %w", err)
	}
	return string(yamlBytes), nil
}

// generateMergePatch creates a minimal merge patch between desired and actual state.
func generateMergePatch(desired map[string]interface{}, actual *unstructured.Unstructured) (string, error) {
	actualJSON, err := json.Marshal(actual.Object)
	if err != nil {
		return "", fmt.Errorf("failed to marshal actual resource: %w", err)
	}

	desiredJSON, err := json.Marshal(desired)
	if err != nil {
		return "", fmt.Errorf("failed to marshal desired state: %w", err)
	}

	var actualMap, desiredMap map[string]interface{}
	if err := json.Unmarshal(actualJSON, &actualMap); err != nil {
		return "", fmt.Errorf("unmarshal actual resource: %w", err)
	}
	if err := json.Unmarshal(desiredJSON, &desiredMap); err != nil {
		return "", fmt.Errorf("unmarshal desired state: %w", err)
	}

	patch := computeDiff(desiredMap, actualMap)
	if len(patch) == 0 {
		return "", nil
	}

	// Add identifying fields to the patch
	if apiVersion, ok := desired["apiVersion"]; ok {
		patch["apiVersion"] = apiVersion
	}
	if kind, ok := desired["kind"]; ok {
		patch["kind"] = kind
	}
	if metadata, ok := desired["metadata"].(map[string]interface{}); ok {
		patchMeta := make(map[string]interface{})
		if name, ok := metadata["name"]; ok {
			patchMeta["name"] = name
		}
		if ns, ok := metadata["namespace"]; ok {
			patchMeta["namespace"] = ns
		}
		if len(patchMeta) > 0 {
			patch["metadata"] = patchMeta
		}
	}

	yamlBytes, err := yaml.Marshal(patch)
	if err != nil {
		return "", fmt.Errorf("marshal patch to YAML: %w", err)
	}

	return string(yamlBytes), nil
}

// computeDiff returns fields from desired that differ from actual.
func computeDiff(desired, actual map[string]interface{}) map[string]interface{} {
	diff := make(map[string]interface{})

	for key, desiredVal := range desired {
		if key == "status" || key == "metadata" {
			continue
		}

		actualVal, exists := actual[key]
		if !exists {
			diff[key] = desiredVal
			continue
		}

		desiredMap, desiredIsMap := desiredVal.(map[string]interface{})
		actualMap, actualIsMap := actualVal.(map[string]interface{})

		if desiredIsMap && actualIsMap {
			subDiff := computeDiff(desiredMap, actualMap)
			if len(subDiff) > 0 {
				diff[key] = subDiff
			}
			continue
		}

		desiredJSON, _ := json.Marshal(desiredVal)
		actualJSON, _ := json.Marshal(actualVal)
		if string(desiredJSON) != string(actualJSON) {
			diff[key] = desiredVal
		}
	}

	return diff
}

// describeActualFix produces a human-readable description of what needs to be done.
func describeActualFix(rootCause *RootCauseResult, ownerResult *OwnershipResult, template *ConfigPolicyTemplate) string {
	if ownerResult != nil && ownerResult.HasExternalOwner {
		return ownerResult.FixTarget
	}

	switch rootCause.PrimaryCause {
	case CauseDirectFixApplicable:
		return fmt.Sprintf("Apply the remediation YAML to create or update %s/%s", template.Kind, template.ResourceName)
	case CauseResourceNotFound:
		return fmt.Sprintf("Create the missing %s/%s resource", template.Kind, template.ResourceName)
	case CauseNamespaceTerminating:
		return fmt.Sprintf("Wait for namespace termination to complete or force-delete the namespace, then recreate %s/%s", template.Kind, template.ResourceName)
	case CauseNamespaceMissing:
		return fmt.Sprintf("Create the namespace '%s', then create %s/%s", template.ResourceNamespace, template.Kind, template.ResourceName)
	case CauseAPINotRegistered:
		return fmt.Sprintf("Install the CRD/operator that provides the %s resource type, then create %s/%s", template.Kind, template.Kind, template.ResourceName)
	case CauseRBACNotVisible:
		return fmt.Sprintf("Grant GET permission for %s resources, then verify %s/%s exists", template.Kind, template.Kind, template.ResourceName)
	case CauseSubscriptionPendingApproval:
		return fmt.Sprintf("Approve the pending InstallPlan for Subscription '%s' in namespace '%s'", template.ResourceName, template.ResourceNamespace)
	case CauseImmutableField:
		return fmt.Sprintf("Delete and recreate %s/%s with the correct configuration", template.Kind, template.ResourceName)
	case CauseAdmissionBlocked:
		return fmt.Sprintf("Resolve the admission controller conflict before applying changes to %s/%s", template.Kind, template.ResourceName)
	case CauseMissingDependency:
		return "Create the missing dependent resources first, then apply the remediation"
	case CauseRBACDenied:
		return "Grant the necessary RBAC permissions before applying the remediation"
	case CauseQuotaExceeded:
		return "Increase the ResourceQuota before applying the remediation"
	case CausePolicyConflict:
		return "Resolve the conflicting policies before applying the remediation"
	default:
		return "Investigate the root cause and apply the remediation once resolved"
	}
}

// generateSteps produces ordered remediation steps based on the root cause.
func generateSteps(rootCause *RootCauseResult, ownerResult *OwnershipResult, template *ConfigPolicyTemplate, patchYAML string) []string {
	var steps []string

	switch rootCause.PrimaryCause {
	case CauseNamespaceTerminating:
		steps = append(steps,
			fmt.Sprintf("Investigate why namespace '%s' is Terminating (check for stuck finalizers)", template.ResourceNamespace),
			fmt.Sprintf("Force-delete the namespace if stuck: kubectl delete namespace %s --force --grace-period=0", template.ResourceNamespace),
			fmt.Sprintf("Recreate the namespace '%s'", template.ResourceNamespace),
		)
	case CauseNamespaceMissing:
		steps = append(steps,
			fmt.Sprintf("Create the namespace: kubectl create namespace %s", template.ResourceNamespace),
		)
	case CauseAPINotRegistered:
		steps = append(steps,
			fmt.Sprintf("Install the operator or CRD that provides the %s resource type", template.Kind),
			"Verify the CRD is available: kubectl get crd",
		)
	case CauseRBACNotVisible:
		steps = append(steps,
			fmt.Sprintf("Grant GET permission for %s resources in namespace '%s'", template.Kind, template.ResourceNamespace),
			"Re-run the diagnosis to verify the resource state",
		)
	case CauseSubscriptionPendingApproval:
		ns := template.ResourceNamespace
		steps = append(steps,
			fmt.Sprintf("List pending InstallPlans: kubectl get installplan -n %s", ns),
			fmt.Sprintf("Approve the InstallPlan: kubectl patch installplan <name> -n %s --type merge -p '{\"spec\":{\"approved\":true}}'", ns),
		)
	case CauseMissingDependency:
		steps = append(steps, "Create the missing dependent resources identified in the root cause analysis")
	case CauseImmutableField:
		ns := template.ResourceNamespace
		deleteCmd := fmt.Sprintf("kubectl delete %s %s", strings.ToLower(template.Kind), template.ResourceName)
		if ns != "" {
			deleteCmd += fmt.Sprintf(" -n %s", ns)
		}
		steps = append(steps, fmt.Sprintf("Delete the existing resource: %s", deleteCmd))
	case CauseAdmissionBlocked:
		steps = append(steps, "Review and update the blocking admission controller or webhook")
	case CauseRBACDenied:
		steps = append(steps, "Update RBAC to grant the necessary permissions")
	case CauseQuotaExceeded:
		steps = append(steps, "Increase the ResourceQuota in the target namespace")
	case CausePolicyConflict:
		steps = append(steps, "Resolve the conflicting policies identified in the analysis")
	}

	// Main remediation step
	if ownerResult != nil && ownerResult.HasExternalOwner {
		steps = append(steps, ownerResult.FixTarget)
	} else if patchYAML != "" {
		steps = append(steps, "Save the remediation YAML to a file (e.g., remediation.yaml)")
		ns := template.ResourceNamespace
		if ns != "" {
			steps = append(steps, fmt.Sprintf("Apply: kubectl apply -f remediation.yaml -n %s", ns))
		} else {
			steps = append(steps, "Apply: kubectl apply -f remediation.yaml")
		}
	}

	// Verification step
	steps = append(steps, "Verify the ACM policy becomes compliant after the change")

	return steps
}
