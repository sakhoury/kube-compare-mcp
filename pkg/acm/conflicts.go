// SPDX-License-Identifier: Apache-2.0

package acm

import (
	"context"
	"log/slog"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// DetectConflicts checks for policies and webhooks that may block remediation of the target resource.
func DetectConflicts(ctx context.Context, inspector ResourceInspector, lister PolicyLister,
	resource *unstructured.Unstructured, targetGVR schema.GroupVersionResource) []PolicyConflict {

	logger := slog.Default()
	var conflicts []PolicyConflict

	targetKind := resource.GetKind()
	targetGroup := targetGVR.Group
	targetResource := targetGVR.Resource

	// 1. Check validating webhooks
	vwhs, err := inspector.ListValidatingWebhooks(ctx)
	if err != nil {
		logger.Debug("Failed to list validating webhooks", "error", err)
	} else {
		for _, wh := range vwhs {
			if webhookMatchesResource(wh.Rules, targetGroup, targetResource) {
				conflicts = append(conflicts, PolicyConflict{
					Type:        "validating_webhook",
					Name:        wh.ConfigName + "/" + wh.WebhookName,
					Kind:        "ValidatingWebhookConfiguration",
					Message:     "Validating webhook matches this resource type and may reject changes",
					Enforcement: wh.FailPolicy,
				})
			}
		}
	}

	// 2. Check mutating webhooks
	mwhs, err := inspector.ListMutatingWebhooks(ctx)
	if err != nil {
		logger.Debug("Failed to list mutating webhooks", "error", err)
	} else {
		for _, wh := range mwhs {
			if webhookMatchesResource(wh.Rules, targetGroup, targetResource) {
				conflicts = append(conflicts, PolicyConflict{
					Type:        "mutating_webhook",
					Name:        wh.ConfigName + "/" + wh.WebhookName,
					Kind:        "MutatingWebhookConfiguration",
					Message:     "Mutating webhook matches this resource type and may alter changes",
					Enforcement: wh.FailPolicy,
				})
			}
		}
	}

	// 3. Check Gatekeeper constraints
	gatekeeperConflicts := detectGatekeeperConflicts(ctx, inspector, targetKind)
	conflicts = append(conflicts, gatekeeperConflicts...)

	// 4. Check other ACM policies targeting the same resource
	if lister != nil {
		acmConflicts := detectACMConflicts(ctx, lister, resource)
		conflicts = append(conflicts, acmConflicts...)
	}

	return conflicts
}

// webhookMatchesResource checks if any webhook rule matches the target resource.
func webhookMatchesResource(rules []WebhookRule, targetGroup, targetResource string) bool {
	for _, rule := range rules {
		groupMatch := false
		for _, g := range rule.APIGroups {
			if g == "*" || g == targetGroup {
				groupMatch = true
				break
			}
		}
		if !groupMatch {
			continue
		}

		for _, r := range rule.Resources {
			if r == "*" || r == targetResource {
				return true
			}
		}
	}
	return false
}

// detectGatekeeperConflicts discovers Gatekeeper constraints that may apply to the resource.
func detectGatekeeperConflicts(ctx context.Context, inspector ResourceInspector, targetKind string) []PolicyConflict {
	logger := slog.Default()
	var conflicts []PolicyConflict

	constraintGVR := schema.GroupVersionResource{
		Group:    "constraints.gatekeeper.sh",
		Version:  "v1beta1",
		Resource: "*",
	}

	resources, err := inspector.ListResources(ctx, constraintGVR, "")
	if err != nil {
		logger.Debug("Gatekeeper constraints not available", "error", err)
		return nil
	}

	for _, res := range resources {
		if gatekeeperConstraintMatchesKind(res, targetKind) {
			enforcement := "deny"
			ea, _, _ := unstructured.NestedString(res.Object, "spec", "enforcementAction")
			if ea != "" {
				enforcement = ea
			}

			conflicts = append(conflicts, PolicyConflict{
				Type:        "gatekeeper",
				Name:        res.GetName(),
				Kind:        res.GetKind(),
				Message:     extractGatekeeperMessage(res),
				Enforcement: enforcement,
			})
		}
	}

	return conflicts
}

// gatekeeperConstraintMatchesKind checks if a Gatekeeper constraint applies to the target kind.
func gatekeeperConstraintMatchesKind(constraint unstructured.Unstructured, targetKind string) bool {
	kinds, _, _ := unstructured.NestedSlice(constraint.Object, "spec", "match", "kinds")
	for _, k := range kinds {
		kindMap, ok := k.(map[string]interface{})
		if !ok {
			continue
		}
		kindList, _, _ := unstructured.NestedStringSlice(kindMap, "kinds")
		for _, kind := range kindList {
			if kind == targetKind || kind == "*" {
				return true
			}
		}
	}
	return false
}

// extractGatekeeperMessage tries to get a violation message from a Gatekeeper constraint status.
func extractGatekeeperMessage(constraint unstructured.Unstructured) string {
	violations, _, _ := unstructured.NestedSlice(constraint.Object, "status", "violations")
	if len(violations) > 0 {
		if v, ok := violations[0].(map[string]interface{}); ok {
			if msg, ok := v["message"].(string); ok {
				return msg
			}
		}
	}
	return "Gatekeeper constraint matches this resource type"
}

// detectACMConflicts checks other ACM policies that target the same resource.
func detectACMConflicts(ctx context.Context, lister PolicyLister, resource *unstructured.Unstructured) []PolicyConflict {
	logger := slog.Default()
	var conflicts []PolicyConflict

	policies, err := lister.ListPolicies(ctx, "")
	if err != nil {
		logger.Debug("Failed to list ACM policies for conflict check", "error", err)
		return nil
	}

	targetKind := resource.GetKind()
	targetName := resource.GetName()
	targetNamespace := resource.GetNamespace()

	for _, p := range policies {
		if !strings.EqualFold(p.Compliant, "noncompliant") {
			continue
		}
		if p.Message != "" &&
			strings.Contains(p.Message, targetKind) &&
			strings.Contains(p.Message, targetName) {
			conflicts = append(conflicts, PolicyConflict{
				Type:        "acm_conflict",
				Name:        p.Namespace + "/" + p.Name,
				Kind:        "Policy",
				Message:     "Another ACM policy also targets " + targetKind + "/" + targetName + " in " + targetNamespace,
				Enforcement: p.RemediationAction,
			})
		}
	}

	return conflicts
}
