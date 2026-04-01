// Copyright (c) 2025 Reliant Labs
package worktree

import (
	"time"
)

// Status represents the current state of a worktree
type Status string

const (
	StatusActive    Status = "active"
	StatusCompleted Status = "completed"
	StatusAbandoned Status = "abandoned"
	StatusMerging   Status = "merging"
)

// Worktree represents a git worktree instance
type Worktree struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	WorkingDir string    `json:"working_dir"`
	Branch     string    `json:"branch"`
	BaseBranch string    `json:"base_branch"`
	RepoID     string    `json:"repo_id"`
	ProjectID  string    `json:"project_id"`
	SessionID  string    `json:"session_id"`
	Status     Status    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	LastActive time.Time `json:"last_active"`
}

// Metadata represents persistent worktree metadata
type Metadata struct {
	Version   string               `json:"version"`
	Worktrees map[string]*Worktree `json:"worktrees"`
	CurrentID string               `json:"current_id,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// CreateOptions configures worktree creation
type CreateOptions struct {
	Branch     string
	BaseBranch string
	SessionID  string
	CopyFiles  []string // Files to copy from source repo (e.g., .env, .env.local) - searches recursively
	Force      bool     // Force creation by deleting existing worktree/branch
}

// ImportOptions configures worktree import
type ImportOptions struct {
	SessionID string
	Name      string // Optional: override the name (defaults to directory name)
}

// CompleteOptions configures worktree completion
type CompleteOptions struct {
	Push        bool
	CreatePR    bool
	PRTitle     string
	PRBody      string
	DeleteLocal bool
}

// CleanupOptions configures cleanup operations
type CleanupOptions struct {
	Force     bool
	All       bool
	Completed bool
	Abandoned bool
	OlderThan time.Duration
}

// ListOptions configures worktree listing
type ListOptions struct {
	Status    Status
	RepoID    string
	SessionID string
}
