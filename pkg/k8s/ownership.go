// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"encoding/json"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

// Known external field managers that indicate active reconciliation.
var externalManagerKeywords = []string{
	"argocd", "argo-cd",
	"flux", "kustomize-controller", "helm-controller",
	"helm", "tiller",
	"terraform", "pulumi", "crossplane",
	"ansible", "ansible-operator",
}

// Known ownership annotations and their manager mapping.
var ownershipAnnotations = map[string]string{
	"argocd.argoproj.io/managed-by":           "ArgoCD",
	"meta.helm.sh/release-name":               "Helm",
	"kustomize.toolkit.fluxcd.io/name":        "Flux Kustomization",
	"helm.toolkit.fluxcd.io/name":             "Flux HelmRelease",
	"flux.weave.works/sync-checksum":          "Flux v1",
	"crossplane.io/composition-resource-name": "Crossplane",
}

// AnalyzeOwnership determines if a resource is externally managed and by whom.
// It checks managedFields, ownership annotations, and ownerReferences.
func AnalyzeOwnership(resource *unstructured.Unstructured) *acm.OwnershipResult {
	result := &acm.OwnershipResult{}

	// 1. Check managedFields for external field managers
	managedFieldOwners := analyzeManagedFields(resource)
	result.Owners = append(result.Owners, managedFieldOwners...)

	// 2. Check annotations for GitOps/Helm ownership markers
	annotationOwners := analyzeAnnotations(resource)
	result.Owners = append(result.Owners, annotationOwners...)

	// 3. Check ownerReferences for controller ownership
	ownerRefOwners := analyzeOwnerReferences(resource)
	result.Owners = append(result.Owners, ownerRefOwners...)

	// Determine if there's an external owner and set the fix target
	for _, owner := range result.Owners {
		if isExternalManager(owner.Manager) || owner.Controller {
			result.HasExternalOwner = true
			result.FixTarget = resolveFixTarget(owner)
			break
		}
	}

	return result
}

// analyzeManagedFields parses metadata.managedFields for external field managers.
func analyzeManagedFields(resource *unstructured.Unstructured) []acm.OwnershipConflict {
	var owners []acm.OwnershipConflict

	managedFields := resource.GetManagedFields()
	if len(managedFields) == 0 {
		return nil
	}

	seen := make(map[string]bool)
	for _, mf := range managedFields {
		manager := mf.Manager
		if seen[manager] || !isExternalManager(manager) {
			continue
		}
		seen[manager] = true

		paths := flattenFieldsV1(mf.FieldsV1)
		owners = append(owners, acm.OwnershipConflict{
			Manager: manager,
			Type:    "managed_fields",
			Paths:   paths,
		})
	}

	return owners
}

// flattenFieldsV1 extracts field paths from a FieldsV1 structure.
func flattenFieldsV1(fields interface{}) []string {
	if fields == nil {
		return nil
	}

	// FieldsV1 is stored as raw JSON in the ManagedFieldsEntry
	var raw map[string]interface{}

	switch v := fields.(type) {
	case map[string]interface{}:
		raw = v
	default:
		data, err := json.Marshal(fields)
		if err != nil {
			return nil
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil
		}
	}

	var paths []string
	walkFieldsV1(raw, "", &paths)
	return paths
}

// walkFieldsV1 recursively walks a FieldsV1 map and collects field paths.
func walkFieldsV1(node map[string]interface{}, prefix string, paths *[]string) {
	for key, val := range node {
		var cleanKey string
		switch {
		case strings.HasPrefix(key, "f:"):
			cleanKey = strings.TrimPrefix(key, "f:")
		case strings.HasPrefix(key, "k:"):
			cleanKey = parseMergeKey(key)
		case strings.HasPrefix(key, "v:"):
			continue // value markers, skip
		default:
			cleanKey = key
		}

		var currentPath string
		if prefix == "" {
			currentPath = cleanKey
		} else {
			currentPath = prefix + "." + cleanKey
		}

		*paths = append(*paths, currentPath)

		if child, ok := val.(map[string]interface{}); ok {
			walkFieldsV1(child, currentPath, paths)
		}
	}
}

// parseMergeKey extracts a human-readable identifier from a FieldsV1 merge key.
// Example: `k:{"name":"app"}` -> `[name=app]`
func parseMergeKey(key string) string {
	inner := strings.TrimPrefix(key, "k:")
	var keyMap map[string]interface{}
	if err := json.Unmarshal([]byte(inner), &keyMap); err != nil {
		return inner
	}

	var parts []string
	for k, v := range keyMap {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// analyzeAnnotations checks for ownership annotations and labels from GitOps tools and Helm.
func analyzeAnnotations(resource *unstructured.Unstructured) []acm.OwnershipConflict {
	var owners []acm.OwnershipConflict

	// Check ownership annotations
	annotations := resource.GetAnnotations()
	for annotation, managerName := range ownershipAnnotations {
		if val, ok := annotations[annotation]; ok {
			// For the generic managed-by label, use the value as the manager name
			name := managerName
			if name == "" {
				name = val
			}
			owners = append(owners, acm.OwnershipConflict{
				Manager: fmt.Sprintf("%s (%s=%s)", name, annotation, val),
				Type:    "annotation",
			})
		}
	}

	// Also check the standard managed-by label
	labels := resource.GetLabels()
	if managedBy, ok := labels["app.kubernetes.io/managed-by"]; ok {
		if isExternalManager(managedBy) {
			owners = append(owners, acm.OwnershipConflict{
				Manager: fmt.Sprintf("%s (app.kubernetes.io/managed-by)", managedBy),
				Type:    "annotation",
			})
		}
	}

	return owners
}

// analyzeOwnerReferences checks for controller ownerReferences.
func analyzeOwnerReferences(resource *unstructured.Unstructured) []acm.OwnershipConflict {
	var owners []acm.OwnershipConflict

	for _, ref := range resource.GetOwnerReferences() {
		isController := ref.Controller != nil && *ref.Controller
		owners = append(owners, acm.OwnershipConflict{
			Manager:    fmt.Sprintf("%s/%s", ref.Kind, ref.Name),
			Type:       "owner_reference",
			Controller: isController,
		})
	}

	return owners
}

// isExternalManager checks if a field manager name belongs to a known external tool.
func isExternalManager(manager string) bool {
	lower := strings.ToLower(manager)
	for _, keyword := range externalManagerKeywords {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// resolveFixTarget generates a human-readable description of where the actual fix should be applied.
func resolveFixTarget(owner acm.OwnershipConflict) string {
	lower := strings.ToLower(owner.Manager)

	switch {
	case strings.Contains(lower, "argocd") || strings.Contains(lower, "argo-cd"):
		return "Update the ArgoCD Application source (Git repository) with the required changes"
	case strings.Contains(lower, "flux") || strings.Contains(lower, "kustomize-controller"):
		return "Update the Flux Kustomization or HelmRelease source in Git"
	case strings.Contains(lower, "helm") || strings.Contains(lower, "tiller"):
		return "Update the Helm chart values or templates and run helm upgrade"
	case strings.Contains(lower, "terraform"):
		return "Update the Terraform configuration and run terraform apply"
	case strings.Contains(lower, "crossplane"):
		return "Update the Crossplane Composition or Claim"
	case owner.Controller:
		return fmt.Sprintf("Update the parent controller resource: %s", owner.Manager)
	default:
		return fmt.Sprintf("Update via the managing tool: %s", owner.Manager)
	}
}
