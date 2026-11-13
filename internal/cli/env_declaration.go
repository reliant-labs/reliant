package cli

// THE ENV-DECLARATION SEAM: where one environment's facts come from.
//
// An env is declared in exactly one place:
//
//   - KCL: deploy/kcl/<env>/main.k exists. Its render is the declaration
//     (obsTargetFromEntities), as it always was. KCL is the long-term home of
//     an environment's topology, so an env with a KCL tree is KCL's.
//   - forge.yaml: `envs:` names it and there is no deploy/kcl/<env>/ — a
//     brownfield project that adopted forge for monitoring and never wrote KCL.
//     Its facts are lowered from `monitoring:` (yamlObsTarget) into the SAME
//     obsbackend.Facts the render fills, so every `forge obs` verb reads one
//     model whichever file declared the env.
//   - flags: no forge.yaml at all, and `forge obs … --project <name>` names
//     the project: the env is taken to be on that project's control plane,
//     monitoring on. This is what lets `forge obs` work in a repo that holds
//     no forge files.
//
// Declaring the SAME env's monitoring in both places — forge.yaml lists the env
// under `envs:` with a `monitoring:` block, and deploy/kcl/<env>/ exists — is a
// load error that names both, never a silent precedence: the two could
// disagree about where the env's telemetry goes, and an agent reading either
// would be wrong about the other.

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/obsbackend"
)

// envDeclSource is where an env is declared.
type envDeclSource string

const (
	envDeclNone  envDeclSource = ""
	envDeclKCL   envDeclSource = "kcl"
	envDeclYAML  envDeclSource = "forge.yaml"
	envDeclFlags envDeclSource = "flags"
)

// envDecl is one env's declaration, as the seam resolved it.
type envDecl struct {
	Env    string
	Source envDeclSource
	// Project is the forge project name the env belongs to: forge.yaml
	// `name`, or --project.
	Project string
	// Monitoring is the YAML/flags declaration's monitoring block; nil for a
	// KCL env (whose observability is its render's).
	Monitoring *config.MonitoringConfig
}

// obsProjectFlag and obsControlPlaneFlag are `forge obs`'s persistent
// --project and --control-plane: the project and control plane of a repo with
// no forge.yaml. Set by cobra flag binding; read only through the seam.
var (
	obsProjectFlag      string
	obsControlPlaneFlag string
)

// errEnvDeclaredTwice is the load error for an env both files declare.
var errEnvDeclaredTwice = errors.New("env declared twice")

// declareEnv resolves env's declaration in the project at projectDir. cfg is
// its forge.yaml (nil when there is none).
func declareEnv(projectDir string, cfg *config.ProjectConfig, env string) (envDecl, error) {
	d := envDecl{Env: env}
	if cfg != nil {
		d.Project = cfg.Name
	}
	if p := strings.TrimSpace(obsProjectFlag); p != "" {
		if cfg != nil && cfg.Name != "" && cfg.Name != p {
			return d, fmt.Errorf("--project %q disagrees with forge.yaml's name %q: drop --project, or run from a directory whose forge.yaml names %q", p, cfg.Name, p)
		}
		d.Project = p
	}
	kcl := projectDir != "" && obsTargetCacheable(env) && fileExists(filepath.Join(projectDir, "deploy", "kcl", env, "main.k"))
	yamlEnv := cfg.DeclaresEnv(env)

	switch {
	case kcl && yamlEnv && cfg.Monitoring != nil:
		return d, fmt.Errorf("%w: env %q's monitoring is declared in both forge.yaml (envs: + monitoring:) and deploy/kcl/%s/main.k\n"+
			"fix: KCL is the long-term home of an env: drop %q from forge.yaml `envs:` and declare its observability in deploy/kcl/%s/main.k "+
			"(forge skill load observability), or delete deploy/kcl/%s/ if this env is monitoring-only",
			errEnvDeclaredTwice, env, env, env, env, env)
	case kcl:
		d.Source = envDeclKCL
	case yamlEnv:
		d.Source = envDeclYAML
		d.Monitoring = cfg.Monitoring
	case cfg == nil && d.Project != "":
		// No forge files at all: --project names the project, and the env is
		// on its control plane with monitoring on.
		d.Source = envDeclFlags
		d.Monitoring = &config.MonitoringConfig{Enabled: true}
	}
	if d.Source != envDeclKCL && d.Monitoring != nil && strings.TrimSpace(obsControlPlaneFlag) != "" {
		m := *d.Monitoring
		m.ControlPlane = strings.TrimSpace(obsControlPlaneFlag)
		d.Monitoring = &m
	}
	return d, nil
}

// yamlObsTarget lowers a forge.yaml / flags declaration into the obsTarget
// (obsbackend.Facts) a KCL render would produce for the same intent: the
// observability block is declared, its switch is `monitoring.enabled`, and its
// destination is either the self-hosted endpoint or the Reliant control
// plane's intake. Nothing in it runs on this machine or a cluster forge knows.
func yamlObsTarget(d envDecl) obsTarget {
	f := obsbackend.Facts{Env: d.Env}
	if m := d.Monitoring; m != nil {
		f.Declared, f.Enabled = true, m.Enabled
		if ep := strings.TrimSpace(m.Endpoint); ep != "" {
			f.Destination = &obsbackend.Destination{Endpoint: ep}
		} else {
			f.Destination = &obsbackend.Destination{TokenFromControlPlane: true}
			f.ControlPlane = monitoringControlPlane(m)
		}
	}
	return obsTarget{Facts: f}
}

// monitoringControlPlane is the control plane a monitoring declaration's envs
// live on: its control_plane, else Reliant's.
func monitoringControlPlane(m *config.MonitoringConfig) string {
	if m != nil {
		if cp := strings.TrimSpace(m.ControlPlane); cp != "" {
			return strings.TrimRight(cp, "/")
		}
	}
	return cloud.DefaultEndpoint
}

// loadDeclarationConfig reads the forge.yaml governing projectDir, or nil when
// there is none. A forge.yaml that does not load is an error: the seam must not
// guess past a file the author meant.
func loadDeclarationConfig() (string, *config.ProjectConfig, error) {
	path, err := findProjectConfigFile()
	if errors.Is(err, ErrProjectConfigNotFound) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, err
	}
	cfg, err := loadProjectConfigFrom(path)
	if err != nil {
		return filepath.Dir(path), nil, err
	}
	return filepath.Dir(path), cfg, nil
}
