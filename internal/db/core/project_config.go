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
	// RepoStatesJSON is the git state the pushing machine's .reliant files
	// were read from (config.SyncedRepoState JSON array). It is the
	// machine's, kept on its catalog row (project_machine_catalogs), never
	// on the project's record.
	RepoStatesJSON *string
	PushedAt       time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
