// SPDX-License-Identifier: Apache-2.0

package k8s

import (
	"context"
	"log/slog"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/sakhoury/kube-compare-mcp/pkg/acm"
)

// Known reference field patterns in Kubernetes resources.
// Maps field names to the resource kind they reference.
var referenceFields = map[string]string{
	"secretName":         "Secret",
	"configMapName":      "ConfigMap",
	"serviceAccountName": "ServiceAccount",
	"storageClassName":   "StorageClass",
	"claimName":          "PersistentVolumeClaim",
	"ingressClassName":   "IngressClass",
}

// Known reference object patterns (nested objects with a "name" field).
var referenceObjects = map[string]string{
	"secretRef":       "Secret",
	"configMapRef":    "ConfigMap",
	"secretKeyRef":    "Secret",
	"configMapKeyRef": "ConfigMap",
}

// ValidateDependencies walks the desired state (objectDefinition) and checks
// whether all referenced resources exist in the cluster.
func ValidateDependencies(ctx context.Context, inspector acm.ResourceInspector, objDef map[string]interface{}, namespace string) []acm.DependencyCheck {
	logger := slog.Default()

	var checks []acm.DependencyCheck

	// Collect all references
	var refs []depRef
	walkForRefs(objDef, &refs)

	// Check each reference
	for _, ref := range refs {
		ns := ref.namespace
		if ns == "" {
			ns = namespace
		}

		check := acm.DependencyCheck{
			Type:      ref.kind,
			Name:      ref.name,
			Namespace: ns,
		}

		gvr := kindToGVR(ref.kind)
		if gvr.Resource == "" {
			// Unknown kind, skip validation
			check.Exists = true
			checks = append(checks, check)
			continue
		}

		_, err := inspector.GetResource(ctx, gvr, ref.name, ns)
		if err != nil {
			check.Exists = false
			check.Error = err.Error()
			logger.Debug("Dependency not found",
				"kind", ref.kind,
				"name", ref.name,
				"namespace", ns,
				"error", err,
			)
		} else {
			check.Exists = true
		}

		checks = append(checks, check)
	}

	// Check CRD availability for custom resource types
	apiVersion, _ := objDef["apiVersion"].(string)
	if apiVersion != "" {
		group, _ := acm.ParseAPIVersion(apiVersion)
		if group != "" && !isCoreAPIGroup(group) {
			kind, _ := objDef["kind"].(string)
			crdGVR := schema.GroupVersionResource{
				Group:    "apiextensions.k8s.io",
				Version:  "v1",
				Resource: "customresourcedefinitions",
			}
			crdName := acm.KindToResource(kind) + "." + group
			_, err := inspector.GetResource(ctx, crdGVR, crdName, "")
			check := acm.DependencyCheck{
				Type: "CRD",
				Name: crdName,
			}
			if err != nil {
				check.Exists = false
				check.Error = "Custom Resource Definition not installed"
			} else {
				check.Exists = true
			}
			checks = append(checks, check)
		}
	}

	return checks
}

// depRef is an internal reference found during the objectDefinition walk.
type depRef struct {
	kind      string
	name      string
	namespace string
}

// walkForRefs recursively walks a JSON object tree and collects resource references.
func walkForRefs(obj interface{}, refs *[]depRef) {
	switch v := obj.(type) {
	case map[string]interface{}:
		// Check direct reference fields
		for field, kind := range referenceFields {
			if val, ok := v[field].(string); ok && val != "" {
				*refs = append(*refs, depRef{kind: kind, name: val})
			}
		}

		// Check reference object patterns (e.g., secretRef: {name: "x"})
		for field, kind := range referenceObjects {
			if refObj, ok := v[field].(map[string]interface{}); ok {
				if name, ok := refObj["name"].(string); ok && name != "" {
					*refs = append(*refs, depRef{kind: kind, name: name})
				}
			}
		}

		// Check volume sources
		if volumes, ok := v["volumes"].([]interface{}); ok {
			for _, vol := range volumes {
				walkVolumeRefs(vol, refs)
			}
		}

		// Recurse into all nested maps and lists
		for _, val := range v {
			walkForRefs(val, refs)
		}

	case []interface{}:
		for _, item := range v {
			walkForRefs(item, refs)
		}
	}
}

// walkVolumeRefs extracts references from volume definitions.
func walkVolumeRefs(vol interface{}, refs *[]depRef) {
	volMap, ok := vol.(map[string]interface{})
	if !ok {
		return
	}

	// configMap volume
	if cm, ok := volMap["configMap"].(map[string]interface{}); ok {
		if name, ok := cm["name"].(string); ok && name != "" {
			*refs = append(*refs, depRef{kind: "ConfigMap", name: name})
		}
	}

	// secret volume
	if sec, ok := volMap["secret"].(map[string]interface{}); ok {
		if name, ok := sec["secretName"].(string); ok && name != "" {
			*refs = append(*refs, depRef{kind: "Secret", name: name})
		}
	}

	// persistentVolumeClaim volume
	if pvc, ok := volMap["persistentVolumeClaim"].(map[string]interface{}); ok {
		if name, ok := pvc["claimName"].(string); ok && name != "" {
			*refs = append(*refs, depRef{kind: "PersistentVolumeClaim", name: name})
		}
	}
}

// kindToGVR maps common Kubernetes kinds to their GroupVersionResource.
func kindToGVR(kind string) schema.GroupVersionResource {
	switch kind {
	case "Secret":
		return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	case "ConfigMap":
		return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "configmaps"}
	case "ServiceAccount":
		return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	case "PersistentVolumeClaim":
		return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "persistentvolumeclaims"}
	case "StorageClass":
		return schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}
	case "IngressClass":
		return schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"}
	default:
		return schema.GroupVersionResource{}
	}
}

// isCoreAPIGroup checks if an API group is a well-known Kubernetes core/standard group.
func isCoreAPIGroup(group string) bool {
	coreGroups := map[string]bool{
		"":                             true,
		"apps":                         true,
		"batch":                        true,
		"networking.k8s.io":            true,
		"policy":                       true,
		"rbac.authorization.k8s.io":    true,
		"storage.k8s.io":               true,
		"autoscaling":                  true,
		"admissionregistration.k8s.io": true,
		"apiextensions.k8s.io":         true,
		"authorization.k8s.io":         true,
		"certificates.k8s.io":          true,
		"coordination.k8s.io":          true,
		"discovery.k8s.io":             true,
		"events.k8s.io":                true,
		"scheduling.k8s.io":            true,
	}
	return coreGroups[group]
}
