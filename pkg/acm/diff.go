// SPDX-License-Identifier: Apache-2.0

package acm

import (
	"encoding/json"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// maxDiffFields limits the number of field differences reported to keep output manageable.
const maxDiffFields = 30

// Well-known top-level keys handled specially during diffing.
const (
	keyMetadata = "metadata"
	keyStatus   = "status"
)

// ComputeFieldDiffs compares the desired state (from the policy objectDefinition) against
// the actual resource and returns a list of fields that differ. This surfaces exactly
// what ACM's "found but not as specified" means in concrete terms.
func ComputeFieldDiffs(desired map[string]interface{}, actual *unstructured.Unstructured) []FieldDifference {
	if actual == nil || len(desired) == 0 {
		return nil
	}

	var diffs []FieldDifference
	collectDiffs(desired, actual.Object, "", &diffs)

	// Sort for deterministic output
	sort.Slice(diffs, func(i, j int) bool {
		return diffs[i].Path < diffs[j].Path
	})

	// Cap the number of diffs to avoid overwhelming output
	if len(diffs) > maxDiffFields {
		diffs = diffs[:maxDiffFields]
	}

	return diffs
}

// collectDiffs recursively walks the desired state and compares against actual,
// collecting fields that differ or are missing.
func collectDiffs(desired, actual map[string]interface{}, prefix string, diffs *[]FieldDifference) {
	for key, desiredVal := range desired {
		// Skip metadata fields that the API server manages
		if prefix == "" && (key == keyMetadata || key == keyStatus) {
			// For metadata, only compare specific user-settable fields
			if key == keyMetadata {
				collectMetadataDiffs(desiredVal, actual[key], diffs)
			}
			if key == keyStatus {
				collectStatusDiffs(desiredVal, actual[key], diffs)
			}
			continue
		}

		path := joinPath(prefix, key)
		actualVal, exists := actual[key]

		if !exists {
			*diffs = append(*diffs, FieldDifference{
				Path:     path,
				Expected: summarizeValue(desiredVal),
				Actual:   nil,
			})
			continue
		}

		// Both exist — compare recursively if both are maps
		desiredMap, desiredIsMap := desiredVal.(map[string]interface{})
		actualMap, actualIsMap := actualVal.(map[string]interface{})

		if desiredIsMap && actualIsMap {
			collectDiffs(desiredMap, actualMap, path, diffs)
			continue
		}

		// For lists and scalars, compare as JSON for reliable equality
		if !jsonEqual(desiredVal, actualVal) {
			*diffs = append(*diffs, FieldDifference{
				Path:     path,
				Expected: summarizeValue(desiredVal),
				Actual:   summarizeValue(actualVal),
			})
		}
	}
}

// collectMetadataDiffs compares only user-settable metadata fields: labels and annotations.
func collectMetadataDiffs(desired, actual interface{}, diffs *[]FieldDifference) {
	desiredMeta, ok := desired.(map[string]interface{})
	if !ok {
		return
	}
	actualMeta, _ := actual.(map[string]interface{})
	if actualMeta == nil {
		actualMeta = map[string]interface{}{}
	}

	// Compare labels
	if desiredLabels, ok := desiredMeta["labels"].(map[string]interface{}); ok {
		actualLabels, _ := actualMeta["labels"].(map[string]interface{})
		if actualLabels == nil {
			actualLabels = map[string]interface{}{}
		}
		for k, v := range desiredLabels {
			path := fmt.Sprintf("metadata.labels.%s", k)
			av, exists := actualLabels[k]
			if !exists {
				*diffs = append(*diffs, FieldDifference{Path: path, Expected: v, Actual: nil})
			} else if !jsonEqual(v, av) {
				*diffs = append(*diffs, FieldDifference{Path: path, Expected: v, Actual: av})
			}
		}
	}

	// Compare annotations
	if desiredAnnotations, ok := desiredMeta["annotations"].(map[string]interface{}); ok {
		actualAnnotations, _ := actualMeta["annotations"].(map[string]interface{})
		if actualAnnotations == nil {
			actualAnnotations = map[string]interface{}{}
		}
		for k, v := range desiredAnnotations {
			path := fmt.Sprintf("metadata.annotations.%s", k)
			av, exists := actualAnnotations[k]
			if !exists {
				*diffs = append(*diffs, FieldDifference{Path: path, Expected: v, Actual: nil})
			} else if !jsonEqual(v, av) {
				*diffs = append(*diffs, FieldDifference{Path: path, Expected: v, Actual: av})
			}
		}
	}
}

// collectStatusDiffs compares status fields. ACM policies often enforce specific status values
// (e.g. status.state: AtLatestKnown on Subscriptions).
func collectStatusDiffs(desired, actual interface{}, diffs *[]FieldDifference) {
	desiredStatus, ok := desired.(map[string]interface{})
	if !ok {
		return
	}
	actualStatus, _ := actual.(map[string]interface{})
	if actualStatus == nil {
		actualStatus = map[string]interface{}{}
	}
	collectDiffs(desiredStatus, actualStatus, "status", diffs)
}

// joinPath joins path segments with dots.
func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// jsonEqual compares two values by their JSON representation.
func jsonEqual(a, b interface{}) bool {
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	return string(aJSON) == string(bJSON)
}

// summarizeValue returns a concise representation of a value for diff output.
// Long values are truncated to keep the output readable.
func summarizeValue(v interface{}) interface{} {
	if v == nil {
		return nil
	}

	switch val := v.(type) {
	case string:
		if len(val) > 200 {
			return val[:200] + "...(truncated)"
		}
		return val
	case map[string]interface{}:
		if len(val) > 5 {
			return fmt.Sprintf("{...%d fields}", len(val))
		}
		return val
	case []interface{}:
		if len(val) > 5 {
			return fmt.Sprintf("[...%d items]", len(val))
		}
		return val
	default:
		return v
	}
}
