package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// build_external_graph.go orders ShellBuilds that are built FROM one another.
//
// ShellBuild.built_images declares `ENV_NAME = "<workload>"`: this command
// needs the image that workload's build pushed. KCL cannot compose that value
// — a render happens before any build, and a digest names bytes that do not
// exist yet — so the edge is data, and the build resolves it: the named
// workload runs first, and the dependent runs with ENV_NAME set to the exact
// `<repository>@sha256:<digest>` it pushed. That is the same digest the
// dependency's build state and the release record, so what an image was built
// FROM and what the release says was built cannot disagree — which a
// hand-edited parent digest in a Dockerfile could never promise.

// externalBuildSchedule is the build units and their built_images graph: what
// the dispatcher runs, what --plan prints, and what runBuild refuses on before
// any build lane has pushed anything.
func externalBuildSchedule(services []WorkloadEntity, opts buildOptions, tag, projectDir string) (externalBuildGraph, map[string][]WorkloadEntity, error) {
	units, aliases, err := externalBuildUnits(services, opts, tag, projectDir)
	if err != nil {
		return externalBuildGraph{}, nil, err
	}
	var base *release.Snapshot
	if opts.composition != nil {
		base = opts.composition.BaseSnapshot
	}
	graph, err := newExternalBuildGraph(units, aliases, func(workload string) bool {
		_, ok := compositionCarriedImage(base, workload)
		return ok
	})
	return graph, aliases, err
}

// externalBuildEdge is one built_images edge of a build unit.
type externalBuildEdge struct {
	// Env is the variable the dependent's command reads.
	Env string
	// Workload is the workload built_images names.
	Workload string
	// Owner is the build unit that produces Workload's image in this
	// invocation — Workload itself, or the unit it shares an artifact with.
	// "" when the image is carried from a composition's base instead.
	Owner string
}

// externalBuildGraph is the build units in dependency order, with each unit's
// edges. Units with no edge between them stay independent: the dispatcher
// still runs them in parallel.
type externalBuildGraph struct {
	order []WorkloadEntity
	edges map[string][]externalBuildEdge
}

// newExternalBuildGraph resolves every unit's built_images edges and orders
// the units so each dependency precedes its dependents. Everything it refuses
// is refused before any build runs or any image is pushed.
//
// carried reports whether a workload outside this invocation still has an
// image the build can name — a composition carrying it from its base. An
// ordinary build never carries: narrowBuildEntities pulls every dependency
// into the build, so a name missing here is not a ShellBuild workload with an
// image at all.
func newExternalBuildGraph(units []WorkloadEntity, aliases map[string][]WorkloadEntity, carried func(workload string) bool) (externalBuildGraph, error) {
	owner := map[string]string{}
	for _, u := range units {
		owner[u.Name] = u.Name
		for _, alias := range aliases[u.Name] {
			owner[alias.Name] = u.Name
		}
	}
	g := externalBuildGraph{edges: map[string][]externalBuildEdge{}}
	indegree := map[string]int{}
	dependents := map[string][]string{}
	byName := map[string]WorkloadEntity{}
	for _, u := range units {
		byName[u.Name] = u
		indegree[u.Name] += 0
		envs := make([]string, 0, len(u.EffectiveBuiltImages()))
		for env := range u.EffectiveBuiltImages() {
			envs = append(envs, env)
		}
		sort.Strings(envs)
		seenDep := map[string]bool{}
		for _, env := range envs {
			workload := u.EffectiveBuiltImages()[env]
			edge := externalBuildEdge{Env: env, Workload: workload, Owner: owner[workload]}
			switch {
			case edge.Owner == u.Name:
				return externalBuildGraph{}, fmt.Errorf("ShellBuild %s: built_images %s names %q, which this same build produces; an image cannot be built FROM itself", u.Name, env, workload)
			case edge.Owner == "" && carried != nil && carried(workload):
				// Resolved from the composition's base at dispatch.
			case edge.Owner == "":
				return externalBuildGraph{}, fmt.Errorf("ShellBuild %s: built_images %s names %q, which this build does not produce. "+
					"built_images must name another workload in this environment that is built by a ShellBuild and declares an image", u.Name, env, workload)
			default:
				if !seenDep[edge.Owner] {
					seenDep[edge.Owner] = true
					indegree[u.Name]++
					dependents[edge.Owner] = append(dependents[edge.Owner], u.Name)
				}
			}
			g.edges[u.Name] = append(g.edges[u.Name], edge)
		}
	}
	// Kahn's algorithm, taking ready units by name so the order — and so a
	// sequential build's log — is deterministic.
	var ready []string
	for name, n := range indegree {
		if n == 0 {
			ready = append(ready, name)
		}
	}
	sort.Strings(ready)
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		g.order = append(g.order, byName[name])
		for _, d := range dependents[name] {
			indegree[d]--
			if indegree[d] == 0 {
				ready = append(ready, d)
				sort.Strings(ready)
			}
		}
	}
	if len(g.order) != len(units) {
		var stuck []string
		for name, n := range indegree {
			if n > 0 {
				stuck = append(stuck, name)
			}
		}
		sort.Strings(stuck)
		return externalBuildGraph{}, fmt.Errorf("ShellBuild built_images form a cycle among %s: each would have to be built before the other", strings.Join(stuck, ", "))
	}
	return g, nil
}

// compositionCarriedImage is the immutable image a composition's base binds
// for workload — the image a carried (not rebuilt) dependency still is.
func compositionCarriedImage(base *release.Snapshot, workload string) (string, bool) {
	if base == nil {
		return "", false
	}
	for _, b := range base.Bindings {
		if b.Consumer.Workload != workload || (b.Slot != "build-only" && b.Slot != "container:"+workload) {
			continue
		}
		a, ok := base.Artifacts[b.Artifact]
		if !ok || a.Kind != release.KindOCI || a.Location == "" || !release.ValidDigest(a.Content) {
			continue
		}
		return a.Location + "@" + a.Content, true
	}
	return "", false
}

// withBuiltImageDependencies adds back every workload of full that a ShellBuild
// in narrowed names in built_images, transitively. A build that names one
// workload cannot run without the images it is built FROM, so `--target app`
// builds app's parents too — in full's order, nothing else added.
func withBuiltImageDependencies(full, narrowed *KCLEntities) *KCLEntities {
	if full == nil || narrowed == nil {
		return narrowed
	}
	keep := map[string]bool{}
	var queue []string
	for _, w := range narrowed.Workloads {
		keep[w.Name] = true
		queue = append(queue, w.Name)
	}
	byName := map[string]WorkloadEntity{}
	for _, w := range full.Workloads {
		byName[w.Name] = w
	}
	added := false
	for len(queue) > 0 {
		w := byName[queue[0]]
		queue = queue[1:]
		for _, dep := range w.EffectiveBuiltImages() {
			if _, declared := byName[dep]; declared && !keep[dep] {
				keep[dep] = true
				added = true
				queue = append(queue, dep)
			}
		}
	}
	if !added {
		return narrowed
	}
	out := *narrowed
	out.Workloads = nil
	for _, w := range full.Workloads {
		if keep[w.Name] {
			out.Workloads = append(out.Workloads, w)
		}
	}
	return &out
}
