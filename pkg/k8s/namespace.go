// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"fmt"
	"log/slog"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

var namespaceGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "namespaces",
}

// CheckNamespaceHealth verifies that a namespace exists and is healthy (Active phase).
// Returns nil if no namespace is required (empty namespace = cluster-scoped resource).
func CheckNamespaceHealth(ctx context.Context, inspector acm.ResourceInspector, namespace string) *acm.NamespaceHealthResult {
	if namespace == "" {
		return nil
	}

	logger := slog.Default()

	ns, err := inspector.GetResource(ctx, namespaceGVR, namespace, "")
	if err != nil {
		logger.Debug("Namespace not found", "namespace", namespace, "error", err)
		return &acm.NamespaceHealthResult{
			Exists:  false,
			Healthy: false,
		}
	}

	phase, _, _ := unstructured.NestedString(ns.Object, "status", "phase")
	if phase == "" {
		phase = "Active" // default if not set
	}

	return &acm.NamespaceHealthResult{
		Exists:  true,
		Phase:   phase,
		Healthy: phase == "Active",
	}
}

// CheckCRDRegistered verifies that the Custom Resource Definition for a given kind/group
// is installed on the cluster. Returns true if the CRD exists or the resource is a core type.
func CheckCRDRegistered(ctx context.Context, inspector acm.ResourceInspector, kind, apiVersion string) (bool, string) {
	group, _ := acm.ParseAPIVersion(apiVersion)
	if group == "" || isCoreAPIGroup(group) {
		return true, "" // Core resources are always available
	}

	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	crdName := acm.KindToResource(kind) + "." + group

	_, err := inspector.GetResource(ctx, crdGVR, crdName, "")
	if err != nil {
		return false, crdName
	}
	return true, crdName
}

// CheckRBACVisibility verifies that the current identity can GET the given resource type.
// A "not found" error from GetResource is indistinguishable from "no permission" without
// an explicit access check.
func CheckRBACVisibility(ctx context.Context, inspector acm.ResourceInspector, gvr schema.GroupVersionResource, namespace string) (bool, error) {
	allowed, err := inspector.CheckAccess(ctx, "get", gvr, namespace)
	if err != nil {
		return false, fmt.Errorf("rbac check for GET %s in %q: %w", gvr.Resource, namespace, err)
	}
	return allowed, nil
}
