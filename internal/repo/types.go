// Copyright (c) 2025 Reliant Labs
package repo

import (
	"path/filepath"
	"time"
)

// Repo is a git repository nested inside a project.
//
// A project that is itself a git repo at its root has one Repo with
// RelativePath == "". A project that holds N sibling git repos under
// it has one Repo per sibling with RelativePath set to the sibling's
// path relative to the project root.
type Repo struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"`
	RelativePath string    `json:"relative_path"`
	RemoteURL    string    `json:"remote_url,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// AbsolutePath returns the absolute on-disk path of the repo given the
// project root. Equivalent to filepath.Join(projectPath, RelativePath).
func (r *Repo) AbsolutePath(projectPath string) string {
	if r.RelativePath == "" {
		return projectPath
	}
	return filepath.Join(projectPath, r.RelativePath)
}
