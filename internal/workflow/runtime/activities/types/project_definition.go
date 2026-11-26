// Copyright (c) 2026 Reliant Labs
package types

// ProjectDefinition is the origin of the bytes the workflow loader parsed.
// ContentHash is hex SHA-256 of the authored file, not the Git object ID.
type ProjectDefinition struct {
	Path        string `json:"path"`
	ContentHash string `json:"content_hash"`
}
