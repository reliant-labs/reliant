package config

import (
	_ "embed"
	"slices"
)

//go:embed pipeline.schema.json
var jsonSchema []byte

// JSONSchema returns a JSON Schema (draft 2020-12) of pipeline.yaml version 1
// for editors, e.g. through a `# yaml-language-server: $schema=…` comment.
// Parse remains the authority; see the package documentation for what the
// schema cannot check. The slice is a copy.
func JSONSchema() []byte { return slices.Clone(jsonSchema) }
