// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// ClusterCompareInputSchema returns the JSON schema for ClusterCompareInput
// with proper enum constraints for output_format.
//
// Note: These schema functions are called during NewServer() initialization,
// before the server accepts any connections. A panic here fails fast at startup,
// which is the correct behavior for schema generation errors.
func ClusterCompareInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[ClusterCompareInput](nil)
	if err != nil {
		panic(err) // Fails at startup, not during request handling
	}

	// Add enum constraint for output_format
	if prop, ok := schema.Properties["output_format"]; ok {
		prop.Enum = []any{"json", "yaml", "junit"}
		prop.Default = json.RawMessage(`"json"`)
	}

	return schema
}

// FindRDSReferenceInputSchema returns the JSON schema for FindRDSReferenceInput
// with proper enum constraints for rds_type.
func FindRDSReferenceInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[FindRDSReferenceInput](nil)
	if err != nil {
		panic(err) // Fails at startup, not during request handling
	}

	// Add enum constraint for rds_type
	if prop, ok := schema.Properties["rds_type"]; ok {
		prop.Enum = []any{"core", "ran"}
	}

	return schema
}

// CompareClusterRDSInputSchema returns the JSON schema for CompareClusterRDSInput
// with proper enum constraints for rds_type and output_format.
func CompareClusterRDSInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[CompareClusterRDSInput](nil)
	if err != nil {
		panic(err) // Fails at startup, not during request handling
	}

	// Add enum constraint for rds_type
	if prop, ok := schema.Properties["rds_type"]; ok {
		prop.Enum = []any{"core", "ran"}
	}

	// Add enum constraint for output_format
	if prop, ok := schema.Properties["output_format"]; ok {
		prop.Enum = []any{"json", "yaml", "junit"}
		prop.Default = json.RawMessage(`"json"`)
	}

	return schema
}
