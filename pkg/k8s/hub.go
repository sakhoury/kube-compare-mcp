// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

// HubInfo holds the result of ACM hub detection.
type HubInfo struct {
	// IsHub is true when the connected cluster is an ACM hub.
	IsHub bool
}

// ManagedClusterGVR is the GVR for ACM ManagedCluster resources.
// Exported because it is also used by the targets tool in pkg/mcpserver.
var ManagedClusterGVR = schema.GroupVersionResource{
	Group:    "cluster.open-cluster-management.io",
	Version:  "v1",
	Resource: "managedclusters",
}

// DetectHub checks if the connected cluster is an ACM hub by looking for the
// MultiClusterHub CRD. Returns HubInfo and a HubDiagnostic.
func DetectHub(ctx context.Context, inspector acm.ResourceInspector) (*HubInfo, *acm.HubDiagnostic) {
	logger := slog.Default()

	info := &HubInfo{}
	diag := &acm.HubDiagnostic{}

	// Check if MultiClusterHub CRD exists (hub indicator).
	crdGVR := schema.GroupVersionResource{
		Group:    "apiextensions.k8s.io",
		Version:  "v1",
		Resource: "customresourcedefinitions",
	}
	_, err := inspector.GetResource(ctx, crdGVR, "multiclusterhubs.operator.open-cluster-management.io", "")
	if err != nil {
		logger.Debug("MultiClusterHub CRD not found, not a hub cluster", "error", err)
		return info, diag
	}

	info.IsHub = true
	diag.IsHub = true
	logger.Info("Detected ACM hub cluster")

	return info, diag
}

// ExtractManagedClusters returns the names of managed clusters that are non-compliant
// for the given policy. It uses two strategies:
//  1. Read AffectedClusters from policy status.status (works for root policies)
//  2. Check if the policy namespace matches a ManagedCluster CR (works for propagated policies)
func ExtractManagedClusters(ctx context.Context, inspector acm.ResourceInspector, policy *acm.PolicyDetail) []string {
	logger := slog.Default()

	// Strategy 1: Extract from policy status (root policy on hub)
	var clusters []string
	for _, cs := range policy.AffectedClusters {
		if cs.Compliant == "NonCompliant" || cs.Compliant == "" {
			clusters = append(clusters, cs.Name)
		}
	}
	if len(clusters) > 0 {
		logger.Debug("Extracted managed clusters from policy status",
			"clusters", clusters,
			"policy", policy.Name,
		)
		return clusters
	}

	// Strategy 2: Check if the policy namespace IS a managed cluster name
	// This is the case for propagated policies where the namespace matches the cluster.
	if policy.Namespace != "" {
		_, err := inspector.GetResource(ctx, ManagedClusterGVR, policy.Namespace, "")
		if err == nil {
			logger.Debug("Policy namespace matches a ManagedCluster",
				"namespace", policy.Namespace,
				"policy", policy.Name,
			)
			return []string{policy.Namespace}
		}
		logger.Debug("Policy namespace does not match a ManagedCluster",
			"namespace", policy.Namespace,
			"error", err,
		)
	}

	return nil
}
