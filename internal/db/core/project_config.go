package core

import "time"

// ProjectConfigRecord stores daemon-pushed configuration for a project: one
// row per project, rewritten whole on every push.
type ProjectConfigRecord struct {
	ID                   string
	ProjectID            string
	DaemonID             string
	UserConfigYAML       *string
	ProjectConfigYAML    *string
	LocalConfigYAML      *string
	GlobalMemoryMD       *string
	ProjectMemoryMD      *string
	MCPConfigs           *string
	ProjectWorkflowsJSON *string
	ProjectPresetsJSON   *string
	ProjectScenariosJSON *string
	ProjectSkillsJSON    *string
	RepoMemoriesJSON     *string
	// RuntimeType is the serving daemon's runtime/sandbox type ("kata",
	// "gvisor"); nil for local/unknown daemons. Captured from the pushing
	// daemon's registration label at config-snapshot persist time.
	RuntimeType *string
	PushedAt    time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
