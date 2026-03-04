// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// makeOptionalFieldsNullable iterates over all properties in the schema and
// makes non-required fields accept null values in addition to their declared type.
// This is needed because LLM clients often send "field": null instead of omitting
// optional fields, and the MCP SDK validates the JSON against the schema strictly.
func makeOptionalFieldsNullable(schema *jsonschema.Schema) {
	required := make(map[string]bool, len(schema.Required))
	for _, r := range schema.Required {
		required[r] = true
	}

	for name, prop := range schema.Properties {
		if required[name] {
			continue
		}
		if prop.Type != "" {
			prop.Types = []string{prop.Type, "null"}
			prop.Type = ""
		}
	}
}

// ClusterDiffInputSchema returns the JSON schema for ClusterDiffInput
// with proper enum constraints for output_format.
func ClusterDiffInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[ClusterDiffInput](nil)
	if err != nil {
		panic(err)
	}
	if prop, ok := schema.Properties["output_format"]; ok {
		prop.Enum = []any{"json", "yaml", "junit"}
		prop.Default = json.RawMessage(`"json"`)
	}
	makeOptionalFieldsNullable(schema)
	return schema
}

// ResolveRDSInputSchema returns the JSON schema for ResolveRDSInput
// with proper enum constraints for rds_type.
func ResolveRDSInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[ResolveRDSInput](nil)
	if err != nil {
		panic(err)
	}
	if prop, ok := schema.Properties["rds_type"]; ok {
		prop.Enum = []any{"core", "ran"}
	}
	makeOptionalFieldsNullable(schema)
	return schema
}

// ValidateRDSInputSchema returns the JSON schema for ValidateRDSInput
// with proper enum constraints for rds_type and output_format.
func ValidateRDSInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[ValidateRDSInput](nil)
	if err != nil {
		panic(err)
	}
	if prop, ok := schema.Properties["rds_type"]; ok {
		prop.Enum = []any{"core", "ran"}
	}
	if prop, ok := schema.Properties["output_format"]; ok {
		prop.Enum = []any{"json", "yaml", "junit"}
		prop.Default = json.RawMessage(`"json"`)
	}
	makeOptionalFieldsNullable(schema)
	return schema
}

// Kubernetes resource name pattern (RFC 1123 DNS subdomain).
const k8sNamePattern = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`

// BIOSDiffInputSchema returns the JSON schema for BIOSDiffInput
// with proper enum constraints, defaults, and validation patterns.
func BIOSDiffInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[BIOSDiffInput](nil)
	if err != nil {
		panic(err)
	}
	if prop, ok := schema.Properties["namespace"]; ok {
		prop.Pattern = k8sNamePattern
	}
	if prop, ok := schema.Properties["host_name"]; ok {
		prop.Pattern = k8sNamePattern
	}
	if prop, ok := schema.Properties["reference_source"]; ok {
		prop.Pattern = k8sNamePattern
		prop.Default = json.RawMessage(`"reference-configs"`)
	}
	if prop, ok := schema.Properties["reference_override"]; ok {
		prop.Pattern = k8sNamePattern
	}
	if prop, ok := schema.Properties["output_format"]; ok {
		prop.Enum = []any{"json", "yaml"}
		prop.Default = json.RawMessage(`"json"`)
	}
	return schema
}

// InspectACMPolicyInputSchema returns the JSON schema for InspectACMPolicyInput.
func InspectACMPolicyInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[InspectACMPolicyInput](nil)
	if err != nil {
		panic(err)
	}
	makeOptionalFieldsNullable(schema)
	return schema
}

// DiagnoseACMPolicyInputSchema returns the JSON schema for DiagnoseACMPolicyInput.
func DiagnoseACMPolicyInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[DiagnoseACMPolicyInput](nil)
	if err != nil {
		panic(err)
	}
	makeOptionalFieldsNullable(schema)
	return schema
}

// BIOSDiffOutputSchema returns the JSON schema for BIOSDiffResult
// enabling structured output validation per MCP 2025-06-18 specification.
func BIOSDiffOutputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[BIOSDiffResult](nil)
	if err != nil {
		panic(err)
	}
	if prop, ok := schema.Properties["Namespace"]; ok {
		prop.Description = "The namespace that was compared"
	}
	if prop, ok := schema.Properties["Hosts"]; ok {
		prop.Description = "Comparison results for each BareMetalHost"
	}
	if prop, ok := schema.Properties["Summary"]; ok {
		prop.Description = "Aggregate statistics across all hosts"
	}
	return schema
}
