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
//
// An env family a root reaches is a RootFamily on it: its expanded variables
// (<prefix><KEY><suffix>) pass the reach filter, and a sensitive one is
// opted into by its pattern in `config_secrets` — handed unasked only when
// the field is required — exactly as a scalar credential is.

package codegen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	// Process is set when a project keeps a project-global AppConfig (the
	// root config, ConfigType/EnvMap) beside configs bound to its primary
	// binary: a root's process loads both, so its env is built from two
	// instances, and workloadEnvMap takes the binary's as a third argument.
	Process *RootsProcess
}

// RootsProcess is the primary binary's own configs beside a project-global
// AppConfig: the KCL type of the instance they are projected from — the
// binary's process bundle when it binds config per command, else its root
// config — the lambda that projects it, whether that lambda takes argv, and
// the member the per-env config.k declares the instance under.
type RootsProcess struct {
	ConfigType, EnvMap, Instance string
	Argv                         bool
}

// rootsProcessEnvMap is the module-private lambda config_gen.k composes a
// root's env through when RootsEnv.Process is set.
const rootsProcessEnvMap = "_rootsProcessEnvMap"

// appConfigRootsEnv is the single-AppConfig project's.
var appConfigRootsEnv = RootsEnv{ConfigType: "AppConfig", EnvMap: "appConfigEnvMap"}

// graphRootsKCL is the ROOTS block of a composition-graph project's config
// module, which every env file the scaffold writes calls into
// (config_gen.root_of, config_gen.workloadEnvMap), so it is never left out:
// nil roots (generate did not read the graph) is defaultRoot; a root whose
// reach generate could not read is fill(root); and a graph with no root at
// all is RootsUnavailableKCL, so an env that names one fails its render
// saying why instead of on a missing symbol.
func graphRootsKCL(roots []GraphRoot, env RootsEnv, defaultRoot GraphRoot, fill func(GraphRoot) GraphRoot) string {
	if roots == nil {
		roots = []GraphRoot{defaultRoot}
	}
	if len(roots) == 0 {
		return RootsUnavailableKCL("the composition graph has no root (a //forge:root struct in internal/app) for a workload to run")
	}
	filled := make([]GraphRoot, 0, len(roots))
	for _, r := range roots {
		if r.Config == nil {
			r = fill(r)
		}
		filled = append(filled, r)
	}
	return RootsKCLFor(filled, env)
}

// RootsUnavailableKCL is the ROOTS block of a composition-graph project whose
// roots' env cannot be projected, for the reason given: root_of and
// workloadEnvMap are still declared, and each fails the render with that
// reason when called. An env that calls neither renders as before.
func RootsUnavailableKCL(reason string) string {
	q := strconv.Quote(reason)
	return fmt.Sprintf(`
# ---------------------------------------------------------------------------
# ROOTS are not projected: %s.
# root_of and workloadEnvMap fail the render with that reason when called.
# ---------------------------------------------------------------------------
ROOTS: {str: any} = {}

root_of = lambda name: str -> {str:} {
    assert False, "config_gen.root_of(\"${name}\"): " + %s
    {}
}

workloadEnvMap = lambda c: any, w: any -> {str: str | forge.SecretRef} {
    assert False, "config_gen.workloadEnvMap(workload '${w.name}'): " + %s
    {}
}
`, reason, q, q)
}

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
	if env.Process != nil {
		// Every call goes through the composing lambda, which takes the
		// binary's instance and argv and hands the root config's half to
		// env.EnvMap.
		data.Process = env.Process
		data.RootEnvMap, data.RootSensitive = env.EnvMap, KCLAllSensitiveName(env.ConfigType)
		data.EnvMap, data.Argv = rootsProcessEnvMap, true
	}
	for _, r := range sorted {
		v := rootKCLView{Name: strconv.Quote(r.Name), Args: kclStringList(r.Args)}
		var env, secrets, required, families []string
		for _, l := range r.Leaves() {
			if l.EnvPrefix != "" {
				// An env family: its expanded variables pass the reach
				// filter by pattern, and a sensitive one is opted into
				// (or, required, handed) by that pattern.
				families = append(families, fmt.Sprintf("RootFamily {prefix = %s, suffix = %s%s}",
					strconv.Quote(l.EnvPrefix), strconv.Quote(l.EnvSuffix), rootFamilyFlags(l)))
				data.Families = true
				continue
			}
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
		if len(families) > 0 {
			v.Families = "[" + strings.Join(families, ", ") + "]"
		}
		data.Roots = append(data.Roots, v)
	}
	out, err := templates.ProjectTemplates().Render("config-roots.k.tmpl", data)
	if err != nil {
		panic("render config-roots.k.tmpl (a forge bug): " + err.Error())
	}
	return string(out)
}

// rootFamilyFlags is the RootFamily members past prefix/suffix that differ
// from their defaults.
func rootFamilyFlags(l ConfigLeaf) string {
	var out string
	if l.Sensitive {
		out += ", sensitive = True"
	}
	if l.Sensitive && l.Required {
		out += ", required = True"
	}
	return out
}

// rootsKCLData is config-roots.k.tmpl's input: each root's KCL literals.
// Families is set when any root reaches an env family; only then does the
// module carry RootFamily, so a project without one renders as before.
type rootsKCLData struct {
	Roots              []rootKCLView
	ConfigType, EnvMap string
	Argv               bool
	Families           bool
	// Process, RootEnvMap and RootSensitive are set for RootsEnv.Process:
	// the binary's instance, and the root config's own lambda and list of
	// sensitive env vars, which split a root's config_secrets between them.
	Process                   *RootsProcess
	RootEnvMap, RootSensitive string
}

type rootKCLView struct{ Name, Args, Env, Secrets, Required, Families string }

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
	return rootReachingAll(GraphRoot{Name: "server", Args: []string{"server"}}, rootFields, perBinary, binary, rootMessage)
}

// rootReachingAll is r reaching every field of its root config (rootFields,
// the message rootMessage) and of each other config of binary that a process
// running r.Args loads: what generate takes a root to reach when it could
// not read the root's reach off the graph — the default `server` of a
// narrowed run, or a root of a project with no root config message on disk.
func rootReachingAll(r GraphRoot, rootFields []ConfigField, perBinary []BinaryConfigFields, binary, rootMessage string) GraphRoot {
	r.Config, r.Loads = DefaultServerRoot(rootFields).Config, nil
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
