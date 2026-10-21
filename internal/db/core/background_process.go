package core

import (
	"time"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// BackgroundProcessSourceType identifies how the process was started
type BackgroundProcessSourceType string

const (
	BgProcessSourceLLM            BackgroundProcessSourceType = "llm"
	BgProcessSourcePackageCommand BackgroundProcessSourceType = "package_command"
	BgProcessSourceManual         BackgroundProcessSourceType = "manual"
)

// BackgroundProcessOutputLine represents a single line of process output in the DB.
type BackgroundProcessOutputLine struct {
	ID        int64     `json:"id"`
	ProcessID string    `json:"process_id"`
	Seq       int64     `json:"seq"`
	Stream    string    `json:"stream"` // "stdout" or "stderr"
	Line      string    `json:"line"`
	CreatedAt time.Time `json:"created_at"`
}

// BackgroundProcess represents a persisted background process record
type BackgroundProcess struct {
	ID          string                            `json:"id"`
	PID         *int                              `json:"pid,omitempty"`
	Command     string                            `json:"command"`
	WorkingDir  string                            `json:"working_dir"`
	WorktreeID  *string                           `json:"worktree_id,omitempty"`
	ProjectID   *string                           `json:"project_id,omitempty"`
	ChatID      *string                           `json:"chat_id,omitempty"`
	UserID      string                            `json:"user_id"`
	Status      reliantv1.BackgroundProcessStatus `json:"status"`
	ExitCode    *int                              `json:"exit_code,omitempty"`
	StartedAt   time.Time                         `json:"started_at"`
	EndedAt     *time.Time                        `json:"ended_at,omitempty"`
	Signature   *string                           `json:"signature,omitempty"`
	SourceType  BackgroundProcessSourceType       `json:"source_type"`
	PackageType *string                           `json:"package_type,omitempty"`
	CommandName *string                           `json:"command_name,omitempty"`
	CreatedAt   time.Time                         `json:"created_at"`
	UpdatedAt   time.Time                         `json:"updated_at"`
}

// BackgroundProcessFilters contains options for filtering background processes
type BackgroundProcessFilters struct {
	UserID     string
	WorktreeID *string
	ProjectID  *string
	ChatID     *string
	Status     *reliantv1.BackgroundProcessStatus
	SourceType *BackgroundProcessSourceType
	Limit      int
	Offset     int
}
