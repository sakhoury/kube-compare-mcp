// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

// CheckMutability performs a server-side dry-run patch to test whether a remediation
// patch can be applied to the resource. It classifies rejection reasons.
func CheckMutability(ctx context.Context, inspector acm.ResourceInspector, gvr schema.GroupVersionResource, name, namespace string, patch []byte) *acm.MutabilityResult {
	logger := slog.Default()

	if len(patch) == 0 {
		return &acm.MutabilityResult{
			Mutable: true,
			Reason:  "no_patch_needed",
		}
	}

	err := inspector.DryRunPatch(ctx, gvr, name, namespace, patch)
	if err == nil {
		logger.Debug("Dry-run patch succeeded", "resource", name, "namespace", namespace)
		return &acm.MutabilityResult{Mutable: true}
	}

	errStr := err.Error()
	logger.Debug("Dry-run patch failed",
		"resource", name,
		"namespace", namespace,
		"error", errStr,
	)

	return classifyPatchRejection(errStr)
}

// classifyPatchRejection categorizes a patch rejection error into a structured result.
func classifyPatchRejection(errStr string) *acm.MutabilityResult {
	lower := strings.ToLower(errStr)

	switch {
	case strings.Contains(lower, "field is immutable") || strings.Contains(lower, "immutable"):
		return &acm.MutabilityResult{
			Mutable: false,
			Reason:  "immutable_field",
			Detail:  errStr,
			Blocker: extractImmutableField(errStr),
		}

	case strings.Contains(lower, "denied the request") || strings.Contains(lower, "admission webhook"):
		webhook := extractWebhookName(errStr)
		return &acm.MutabilityResult{
			Mutable: false,
			Reason:  "webhook_denied",
			Detail:  errStr,
			Blocker: webhook,
		}

	case strings.Contains(lower, "forbidden") || strings.Contains(lower, "cannot patch"):
		return &acm.MutabilityResult{
			Mutable: false,
			Reason:  "rbac_denied",
			Detail:  errStr,
		}

	case strings.Contains(lower, "exceeded quota") || strings.Contains(lower, "resourcequota"):
		return &acm.MutabilityResult{
			Mutable: false,
			Reason:  "quota_exceeded",
			Detail:  errStr,
		}

	case strings.Contains(lower, "not found"):
		return &acm.MutabilityResult{
			Mutable: false,
			Reason:  "resource_not_found",
			Detail:  errStr,
		}

	default:
		return &acm.MutabilityResult{
			Mutable: false,
			Reason:  "unknown",
			Detail:  errStr,
		}
	}
}

// extractImmutableField tries to extract the specific immutable field name from the error.
func extractImmutableField(errStr string) string {
	for _, marker := range []string{"field is immutable", "is immutable"} {
		idx := strings.Index(strings.ToLower(errStr), marker)
		if idx <= 0 {
			continue
		}
		before := errStr[:idx]
		before = strings.TrimRight(before, " :")
		parts := strings.Fields(before)
		if len(parts) > 0 {
			candidate := parts[len(parts)-1]
			if strings.Contains(candidate, ".") || strings.Contains(candidate, "spec") {
				return candidate
			}
		}
	}
	return ""
}

// extractWebhookName tries to extract the webhook name from an admission rejection error.
func extractWebhookName(errStr string) string {
	for _, prefix := range []string{`admission webhook "`, `admission webhook \"`} {
		idx := strings.Index(errStr, prefix)
		if idx < 0 {
			continue
		}
		start := idx + len(prefix)
		remaining := errStr[start:]
		for _, quote := range []string{`"`, `\"`} {
			end := strings.Index(remaining, quote)
			if end > 0 {
				return remaining[:end]
			}
		}
	}
	return ""
}
