// SPDX-License-Identifier: Apache-2.0

package mcpserver

import (
	"encoding/json"

	"github.com/google/jsonschema-go/jsonschema"
)

// ClusterDiffInputSchema returns the JSON schema for ClusterDiffInput
// with proper enum constraints for output_format.
//
// Note: These schema functions are called during NewServer() initialization,
// before the server accepts any connections. A panic here fails fast at startup,
// which is the correct behavior for schema generation errors.
func ClusterDiffInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[ClusterDiffInput](nil)
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

// ResolveRDSInputSchema returns the JSON schema for ResolveRDSInput
// with proper enum constraints for rds_type.
func ResolveRDSInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[ResolveRDSInput](nil)
	if err != nil {
		panic(err) // Fails at startup, not during request handling
	}

	// Add enum constraint for rds_type
	if prop, ok := schema.Properties["rds_type"]; ok {
		prop.Enum = []any{"core", "ran", "hub"}
	}

	return schema
}

// ValidateRDSInputSchema returns the JSON schema for ValidateRDSInput
// with proper enum constraints for rds_type and output_format.
func ValidateRDSInputSchema() *jsonschema.Schema {
	schema, err := jsonschema.For[ValidateRDSInput](nil)
	if err != nil {
		panic(err) // Fails at startup, not during request handling
	}

	// Add enum constraint for rds_type
	if prop, ok := schema.Properties["rds_type"]; ok {
		prop.Enum = []any{"core", "ran", "hub"}
	}

	// Add enum constraint for output_format
	if prop, ok := schema.Properties["output_format"]; ok {
		prop.Enum = []any{"json", "yaml", "junit"}
		prop.Default = json.RawMessage(`"json"`)
	}

	return schema
}
