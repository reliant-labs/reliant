// Copyright (c) 2025 Reliant Labs
package wfyaml

import (
	"maps"
	"slices"
)

// sortedKeys returns m's keys in a stable order.
//
// Every map emitted by the marshaller must go through this. Go randomizes map
// iteration order, so ranging a map straight into yaml.Node.Content makes
// MarshalWorkflow return different bytes for the same workflow on every call.
// That breaks the callers in load_workflow.go, which re-marshal a stored
// workflow on each run: an untouched workflow reads as changed, and any hash,
// cache key or diff taken over the definition is meaningless.
func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
