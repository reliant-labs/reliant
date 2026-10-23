// File: internal/codegen/composition_roots_kcl.go
//
// The deploy half of per-root config (composition graph PR 6): config_gen.k's
// ROOTS, and the two lambdas that bind a workload to a root.
//
//   - ROOTS: one Root per root of the composition graph — the args that run
//     it and the env vars it reaches, split into values and secrets, read off
//     its closure when generate ran (GraphRoot, composition_graph_config.go).
//   - root_of(name) is what a workload declaration merges in to run a root:
//     `{root, args}`. An unknown root fails the render, naming the roots.
//   - workloadEnvMap(cfg, w) is a workload's config env: for a workload bound
//     to a root, exactly the values the root reaches and, of the credentials,
//     the ones it reaches AND requires plus those `config_secrets` opts into
//     (each of which the root must reach — the render fails on one it does
//     not, so a credential reaches only the workloads whose root reads it);
//     for any other workload, appConfigEnvMap as before.
//
// A root's optional credentials stay opt-in for the reason appConfigEnvMap
// gives: an env without that Secret key must not stall the pod.

package codegen

import (
	"sort"
	"strconv"

	"github.com/reliant-labs/forge/internal/templates"
)

// RootsEnv is how config_gen.k projects a root's process env: the KCL type
// of the config instance a workload's env is built from, and the lambda that
// projects it. The single-AppConfig project's is AppConfig/appConfigEnvMap; a
// binary that binds config per command projects through its process selector
// (<bin>ProcessEnvMap), which also takes the process's argv — a root's args.
type RootsEnv struct {
	ConfigType, EnvMap string
	Argv               bool
}

// appConfigRootsEnv is the single-AppConfig project's.
var appConfigRootsEnv = RootsEnv{ConfigType: "AppConfig", EnvMap: "appConfigEnvMap"}

// RootsKCL renders the ROOTS block appended to config_gen.k, or "" when the
// roots' reach is unknown (no root config message) or there are none.
func RootsKCL(roots []GraphRoot) string { return RootsKCLFor(roots, appConfigRootsEnv) }

// RootsKCLFor is RootsKCL for a project whose processes' env is projected
// through env: each root's env and secrets are the leaves it reaches across
// every config its process loads.
func RootsKCLFor(roots []GraphRoot, env RootsEnv) string {
	if len(roots) == 0 {
		return ""
	}
	for _, r := range roots {
		if r.Config == nil {
			return ""
		}
	}
	sorted := append([]GraphRoot(nil), roots...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	data := rootsKCLData{ConfigType: env.ConfigType, EnvMap: env.EnvMap, Argv: env.Argv}
	for _, r := range sorted {
		v := rootKCLView{Name: strconv.Quote(r.Name), Args: kclStringList(r.Args)}
		var env, secrets, required []string
		for _, l := range r.Leaves() {
			if l.EnvVar == "" {
				continue
			}
			switch {
			case l.Sensitive && l.Required:
				required = append(required, l.EnvVar)
				secrets = append(secrets, l.EnvVar)
			case l.Sensitive:
				secrets = append(secrets, l.EnvVar)
			default:
				env = append(env, l.EnvVar)
			}
		}
		v.Env, v.Secrets, v.Required = kclStringList(env), kclStringList(secrets), kclStringList(required)
		data.Roots = append(data.Roots, v)
	}
	out, err := templates.ProjectTemplates().Render("config-roots.k.tmpl", data)
	if err != nil {
		panic("render config-roots.k.tmpl (a forge bug): " + err.Error())
	}
	return string(out)
}

// rootsKCLData is config-roots.k.tmpl's input: each root's KCL literals.
type rootsKCLData struct {
	Roots              []rootKCLView
	ConfigType, EnvMap string
	Argv               bool
}

type rootKCLView struct{ Name, Args, Env, Secrets, Required string }

// DefaultServerRoot is the root a graph-mode project's config module projects
// when this run did not read the graph (a narrowed run, a project whose graph
// does not assemble yet): the default `server`, reaching every field — what
// it reaches in a scaffold, whose API listener takes the whole config.
func DefaultServerRoot(fields []ConfigField) GraphRoot {
	r := GraphRoot{Name: "server", Args: []string{"server"}, Config: []ConfigLeaf{}}
	for _, f := range fields {
		path := f.Name
		if f.KCLBlock != "" {
			path = f.KCLBlock + "." + f.Name
		}
		r.Config = append(r.Config, leafOf(f, path))
	}
	return r
}

// DefaultServerRootLoads is DefaultServerRoot for a binary that binds config
// per command: the default `server` reaches every field of its root config
// and of each other config a process running `server` loads.
func DefaultServerRootLoads(rootFields []ConfigField, perBinary []BinaryConfigFields, binary, rootMessage string) GraphRoot {
	r := DefaultServerRoot(rootFields)
	for _, bc := range perBinary {
		if bc.Binary != binary || bc.MessageName == rootMessage {
			continue
		}
		in := configInput{msg: &ConfigMessage{Name: bc.MessageName, Binary: bc.Binary, Command: bc.Command, Subtrees: bc.Subtrees}}
		if !in.loadedAt(r.Args) {
			continue
		}
		all := DefaultServerRoot(bc.Fields).Config
		r.Loads = append(r.Loads, RootConfigLoad{Message: bc.MessageName, Config: all, Read: len(all) > 0, Command: bc.Command, Subtrees: bc.Subtrees})
	}
	return r
}
