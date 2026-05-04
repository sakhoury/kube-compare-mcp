// SPDX-License-Identifier: Apache-2.0

package mcpserver

import "k8s.io/apimachinery/pkg/runtime/schema"

const (
	// DefaultReferenceConfigNamespace is the default namespace for reference
	// ConfigMaps on the MCP server cluster (BIOS references, RDS analyzer rules, etc.).
	DefaultReferenceConfigNamespace = "reference-configs"

	// DefaultRDSAnalysisFormat is the default output format for RDS analysis results.
	DefaultRDSAnalysisFormat = "html"
)

// configMapGVR is the GroupVersionResource for core/v1 ConfigMap resources.
var configMapGVR = schema.GroupVersionResource{
	Group:    "",
	Version:  "v1",
	Resource: "configmaps",
}
