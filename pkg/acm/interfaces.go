// SPDX-License-Identifier: Apache-2.0

//go:generate mockgen -destination=mock_interfaces_test.go -package=acm_test github.com/sakhoury/kube-compare-mcp/pkg/acm PolicyLister,ResourceInspector,ACMClientFactory

package acm

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

// PolicyLister abstracts ACM Policy CR queries for testing.
type PolicyLister interface {
	// ListPolicies returns all ACM policies, optionally filtered by namespace.
	ListPolicies(ctx context.Context, namespace string) ([]PolicySummary, error)
	// GetPolicy returns full detail for a specific policy.
	GetPolicy(ctx context.Context, name, namespace string) (*PolicyDetail, error)
}

// ResourceInspector abstracts Kubernetes cluster inspection operations for testing.
type ResourceInspector interface {
	// GetResource fetches a single resource by GVR, name, and namespace.
	GetResource(ctx context.Context, gvr schema.GroupVersionResource, name, namespace string) (*unstructured.Unstructured, error)
	// ListResources lists all resources matching the GVR in the given namespace.
	ListResources(ctx context.Context, gvr schema.GroupVersionResource, namespace string) ([]unstructured.Unstructured, error)
	// DryRunPatch performs a server-side dry-run merge patch to test mutability.
	DryRunPatch(ctx context.Context, gvr schema.GroupVersionResource, name, namespace string, patch []byte) error
	// ListValidatingWebhooks returns all ValidatingWebhookConfigurations.
	ListValidatingWebhooks(ctx context.Context) ([]WebhookInfo, error)
	// ListMutatingWebhooks returns all MutatingWebhookConfigurations.
	ListMutatingWebhooks(ctx context.Context) ([]WebhookInfo, error)
	// ListEvents returns Kubernetes events for a specific resource.
	ListEvents(ctx context.Context, name, namespace, kind string) ([]EventInfo, error)
	// CheckAccess checks if the current identity has a specific RBAC permission.
	CheckAccess(ctx context.Context, verb string, gvr schema.GroupVersionResource, namespace string) (bool, error)
}

// ACMClientFactory creates PolicyLister and ResourceInspector from a rest.Config.
type ACMClientFactory interface {
	NewPolicyLister(config *rest.Config) (PolicyLister, error)
	NewResourceInspector(config *rest.Config) (ResourceInspector, error)
}

// --- Helper functions for GVK/GVR conversion ---

// ParseAPIVersion splits an apiVersion string into group and version.
// "apps/v1" -> ("apps", "v1"), "v1" -> ("", "v1")
func ParseAPIVersion(apiVersion string) (group, version string) {
	parts := strings.SplitN(apiVersion, "/", 2)
	if len(parts) == 1 {
		return "", parts[0]
	}
	return parts[0], parts[1]
}

// KindToResource converts a Kubernetes Kind to its plural resource name.
func KindToResource(kind string) string {
	lower := strings.ToLower(kind)

	switch {
	case strings.HasSuffix(lower, "ss") || strings.HasSuffix(lower, "x") ||
		strings.HasSuffix(lower, "sh") || strings.HasSuffix(lower, "ch"):
		return lower + "es"
	case strings.HasSuffix(lower, "y") && len(lower) > 1:
		beforeY := lower[len(lower)-2]
		if beforeY != 'a' && beforeY != 'e' && beforeY != 'i' && beforeY != 'o' && beforeY != 'u' {
			return lower[:len(lower)-1] + "ies"
		}
		return lower + "s"
	case strings.HasSuffix(lower, "s"):
		return lower + "es"
	default:
		return lower + "s"
	}
}

// GVRFromObjectDef extracts GroupVersionResource, name, and namespace from an objectDefinition map.
func GVRFromObjectDef(objDef map[string]interface{}) (gvr schema.GroupVersionResource, name, namespace string, err error) {
	apiVersion, _ := objDef["apiVersion"].(string)
	kind, _ := objDef["kind"].(string)

	if apiVersion == "" || kind == "" {
		return schema.GroupVersionResource{}, "", "", fmt.Errorf("objectDefinition missing apiVersion or kind")
	}

	group, version := ParseAPIVersion(apiVersion)
	resource := KindToResource(kind)

	if metadata, ok := objDef["metadata"].(map[string]interface{}); ok {
		name, _ = metadata["name"].(string)
		namespace, _ = metadata["namespace"].(string)
	}

	return schema.GroupVersionResource{
		Group:    group,
		Version:  version,
		Resource: resource,
	}, name, namespace, nil
}
