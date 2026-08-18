// Copyright (c) 2025 Reliant Labs
// Package config manages application configuration from various sources.
//
// forge:exclude-contract
//
// Leaf utility package: the exported surface is concrete helpers over the
// stdlib or the OS, with no collaborator to fake and no second implementation.
// An interface here would have exactly one implementor and one caller shape,
// which is indirection without a seam.
package config

import (
	"fmt"

	"github.com/reliant-labs/reliant/internal/llm/models"
)

// MCPType defines the type of MCP (Model Control Protocol) server.
type MCPType string

// Supported MCP types
const (
	MCPStdio MCPType = "stdio"
	MCPSse   MCPType = "sse"
	MCPHTTP  MCPType = "http"
)

// MCPServer defines the configuration for a Model Control Protocol server.
type MCPServer struct {
	Command string            `yaml:"command" json:"command"`
	Env     []string          `yaml:"env" json:"env"`
	Args    []string          `yaml:"args" json:"args"`
	Type    MCPType           `yaml:"type" json:"type"`
	URL     string            `yaml:"url" json:"url"`
	Headers map[string]string `yaml:"headers" json:"headers"`
	Enabled bool              `yaml:"enabled" json:"enabled"`

	// Dir is the working directory the server's process is started in. Empty
	// means the project the tool call belongs to, which is what a language
	// server or any other tree-indexing server wants: it is the only way such a
	// server learns WHICH tree to index. Set it explicitly only to pin a server
	// to a fixed tree regardless of the caller's project.
	//
	// Supports ${VAR} / $VAR expansion, like Env.
	Dir string `yaml:"dir,omitempty" json:"dir,omitempty"`

	// DirScoped declares that this server's answers are scoped to the directory
	// it was STARTED in, so two projects must not share one process.
	//
	// Most MCP servers are stateless request/response and are safely shared: the
	// manager keeps ONE client per server name and simply associates it with
	// every project that asks for it. A server that indexes a tree breaks that
	// assumption — it resolves its workspace once, at launch, and then answers
	// every later question against that tree no matter who asked. Measured: a
	// Go language server registered globally answered from the daemon's own
	// checkout for a chat in a different project, returning confident matches
	// from the wrong repository rather than no matches.
	//
	// When true the manager keys this server's clients by project as well as by
	// name, so each project gets its own process. Declared in CONFIG rather
	// than matched on a server name in reliant's source: which servers are
	// tree-scoped is a fact about the servers a user installs, and reliant
	// cannot know the set.
	DirScoped bool `yaml:"dirScoped,omitempty" json:"dirScoped,omitempty"`
}

// Data defines storage configuration.
type Data struct {
	Directory string `yaml:"directory,omitempty" json:"directory,omitempty"`
}

// Config is the main configuration structure for the application.
type Config struct {
	Data            Data                     `yaml:"data" json:"data"`
	WorkingDir      string                   `yaml:"wd,omitempty" json:"wd,omitempty"`
	WorktreeDir     string                   `yaml:"worktreeDir,omitempty" json:"worktreeDir,omitempty"`
	MCPServers      map[string]MCPServer     `yaml:"mcpServers,omitempty" json:"mcpServers,omitempty"`
	Debug           bool                     `yaml:"debug,omitempty" json:"debug,omitempty"`
	ContextPaths    []string                 `yaml:"contextPaths,omitempty" json:"contextPaths,omitempty"`
	Models          *models.UserModelsConfig `yaml:"models,omitempty" json:"models,omitempty"`
	GlobalMemoryMD  string                   `yaml:"-" json:"-"`
	ProjectMemoryMD string                   `yaml:"-" json:"-"`
	// Skills is populated from the daemon's project config sync. It is the
	// authoritative, in-memory snapshot of SKILL.md files available to this
	// project and is what the skill tool / auto-suggestion use at runtime.
	Skills []StoredSkill `yaml:"-" json:"-"`
	// RepoMemories maps nested repo relative path (e.g. "api", "forge") to
	// the concatenated content of that repo's reliant.md + reliant.local.md.
	// Populated from the daemon's project config sync so cloud workers don't
	// need filesystem access.
	RepoMemories map[string]string `yaml:"-" json:"-"`
	// DaemonRuntimeType identifies the sandbox/runtime the serving daemon runs
	// under (e.g. "kata", "gvisor"). Empty for local/unknown daemons. Reported
	// by the daemon at registration (DAEMON_RUNTIME_TYPE env → register label)
	// and threaded here so the LLM context can carry runtime capability
	// limitations. Ephemeral, in-memory only.
	DaemonRuntimeType string `yaml:"-" json:"-"`
	// SnapshotSynced reports whether a DAEMON has actually pushed a config
	// snapshot for this project, as opposed to the row being the placeholder
	// CreateProject seeds (see SeedDaemonID) or missing outright.
	//
	// It exists because the two states are otherwise indistinguishable: an
	// unsynced project and a synced project that genuinely contains nothing
	// both arrive here as a Config with empty Skills/Workflows. Consumers were
	// left inferring "empty means not filled yet, so retry" — which is right
	// for a project mid-sync and wrong (an unwinnable retry loop) for one whose
	// snapshot will never arrive. Branch on this instead of on emptiness.
	SnapshotSynced bool `yaml:"-" json:"-"`
}

// DaemonRuntimeTypeLabelKey is the daemon-registration label key that carries
// the daemon's runtime/sandbox type ("kata", "gvisor", ...). The daemon sets it
// from the DAEMON_RUNTIME_TYPE env var; the server persists it with the pushed
// project config so it can be surfaced to the model as a capability heads-up.
const DaemonRuntimeTypeLabelKey = "reliant.runtime-type"

// Application constants
const (
	MaxTokensFallbackDefault = 32000
)

// DefaultContextPaths defines the context/instruction files that are auto-loaded.
// Simplified to only support reliant.md (project instructions) and reliant.local.md (local/gitignored).
var DefaultContextPaths = []string{
	"reliant.md",       // Project instructions (committed to git)
	"reliant.local.md", // Local instructions (gitignored)
}

// Validate checks if the configuration is valid and applies defaults where needed.
func Validate(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}

	return nil
}
