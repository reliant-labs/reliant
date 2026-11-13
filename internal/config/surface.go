package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Surface is which of forge's pieces a project uses, read off its tree and
// forge.yaml the way the loader derives them (deriveProjectKindFromSources,
// ResolveInventoryAtLoad, ApplyDerivedDefaults) — one derivation, not a
// second inventory.
//
// Unlike LoadProject it never refuses: forge.yaml is decoded leniently, so a
// key this forge does not know (a newer forge wrote it), a mistyped value or
// a project mid-edit still describes itself. Consumers that only DESCRIBE a
// project — the agent memory, rendered into every session — must not go
// blank because the config would fail a command that acts on it.
type Surface struct {
	// Config is forge.yaml decoded leniently, with the loader's derived
	// state applied: Kind, the frontend inventory, the database and the
	// features (Config.Features.*Enabled).
	Config ProjectConfig
	// GoModule reports a go.mod at the root (HasGoModule).
	GoModule bool
	// ServerSources reports a forge service's own sources: pkg/app,
	// internal/handlers or proto/services (hasServerSources).
	ServerSources bool
	// DeployTree reports deploy/kcl (hasDeployTree).
	DeployTree bool
	// DeployEnvs are the deploy/kcl/<env>/ directories holding a main.k,
	// sorted.
	DeployEnvs []string
	// Workloads reports deploy/kcl/workloads.k, where an adopted app's
	// workloads are declared once.
	Workloads bool
	// RootFrontend reports a frontend at the project root: a static site
	// (DiscoverRootFrontend).
	RootFrontend bool
	// Envs and Monitoring are forge.yaml's brownfield switches `envs:` and
	// `monitoring:` (a bare boolean, or a mapping whose `enabled` defaults
	// to true).
	Envs       []string
	Monitoring bool
	// MigrationTool and MigrationsDir are forge.yaml
	// `database.migrations.tool` / `.dir` (or `database.migrations_dir`):
	// the tool a project keeps for its migrations. Empty when undeclared.
	MigrationTool string
	MigrationsDir string
}

// brownfieldSwitches are the forge.yaml keys an adoption writes before (or
// instead of) any forge source: they switch single pieces of forge on for an
// app forge did not build. Read here by name so the surface works whether or
// not this forge's ProjectConfig declares them yet.
type brownfieldSwitches struct {
	Envs       []string  `yaml:"envs"`
	Monitoring yaml.Node `yaml:"monitoring"`
	Database   struct {
		MigrationsDir string `yaml:"migrations_dir"`
		Migrations    struct {
			Dir  string `yaml:"dir"`
			Tool string `yaml:"tool"`
		} `yaml:"migrations"`
	} `yaml:"database"`
}

// monitoringOn reads `monitoring:` as the config declares it: `true` /
// `false`, or a mapping that is on unless it says `enabled: false`.
func monitoringOn(n yaml.Node) bool {
	switch n.Kind {
	case yaml.ScalarNode:
		var on bool
		return n.Decode(&on) == nil && on
	case yaml.MappingNode:
		var m struct {
			Enabled *bool `yaml:"enabled"`
		}
		if n.Decode(&m) != nil {
			return false
		}
		return m.Enabled == nil || *m.Enabled
	}
	return false
}

// ReadSurface derives the Surface of the project rooted at projectDir. The
// only error is an unreadable forge.yaml or one that is not YAML at all.
func ReadSurface(projectDir string) (Surface, error) {
	data, err := os.ReadFile(filepath.Join(projectDir, ProjectConfigFile)) //nolint:gosec // project dir + fixed filename
	if err != nil {
		return Surface{}, err
	}
	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		// A type error leaves every other field decoded; only a document
		// that is not YAML has nothing to describe.
		if _, typed := err.(*yaml.TypeError); !typed {
			return Surface{}, err
		}
	}
	var sw brownfieldSwitches
	_ = yaml.Unmarshal(data, &sw) // best-effort: absent or mistyped reads as off

	cfg.projectDir = projectDir
	cfg.Kind = deriveProjectKindFromSources(projectDir)
	ResolveInventoryAtLoad(&cfg, projectDir)
	ApplyDerivedDefaults(&cfg)
	_, site := DiscoverRootFrontend(projectDir)

	s := Surface{
		Config:        cfg,
		GoModule:      cfg.HasGoModule(),
		ServerSources: cfg.hasServerSources(),
		DeployTree:    cfg.hasDeployTree(),
		Workloads:     fileExists(filepath.Join(projectDir, "deploy", "kcl", "workloads.k")),
		RootFrontend:  site,
		Envs:          sw.Envs,
		Monitoring:    monitoringOn(sw.Monitoring),
		MigrationTool: strings.ToLower(strings.TrimSpace(sw.Database.Migrations.Tool)),
		MigrationsDir: firstNonEmpty(sw.Database.Migrations.Dir, sw.Database.MigrationsDir),
	}
	if s.DeployTree {
		s.DeployEnvs = deployEnvDirs(filepath.Join(projectDir, "deploy", "kcl"))
	}
	return s, nil
}

// deployEnvDirs lists the env directories under a KCL deploy tree: those that
// hold a main.k.
func deployEnvDirs(kclDir string) []string {
	entries, err := os.ReadDir(kclDir)
	if err != nil {
		return nil
	}
	var envs []string
	for _, e := range entries {
		if e.IsDir() && fileExists(filepath.Join(kclDir, e.Name(), "main.k")) {
			envs = append(envs, e.Name())
		}
	}
	sort.Strings(envs)
	return envs
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
