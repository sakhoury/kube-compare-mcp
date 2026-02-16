// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"
	"sort"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

// AnalyzeEvents fetches and analyzes Kubernetes events for a specific resource.
// Returns events sorted by last seen timestamp (most recent first),
// with Warning events prioritized.
func AnalyzeEvents(ctx context.Context, inspector acm.ResourceInspector, name, namespace, kind string) []acm.EventInfo {
	logger := slog.Default()

	events, err := inspector.ListEvents(ctx, name, namespace, kind)
	if err != nil {
		logger.Debug("Failed to list events",
			"name", name,
			"namespace", namespace,
			"kind", kind,
			"error", err,
		)
		return nil
	}

	if len(events) == 0 {
		return nil
	}

	// Sort: Warnings first, then by last seen (most recent first)
	sort.Slice(events, func(i, j int) bool {
		// Warnings before Normal events
		if events[i].Type != events[j].Type {
			return events[i].Type == "Warning"
		}
		// Most recent first
		return events[i].LastSeen > events[j].LastSeen
	})

	// Limit to the most relevant events
	const maxEvents = 20
	if len(events) > maxEvents {
		events = events[:maxEvents]
	}

	logger.Debug("Events analyzed",
		"resource", kind+"/"+name,
		"namespace", namespace,
		"totalEvents", len(events),
	)

	return events
}
