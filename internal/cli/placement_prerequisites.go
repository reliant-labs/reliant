package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/pkg/release"
)

// declaredPlacementEdges returns the ORDERING edges release_dependencies
// imply, and refuses a peer that names no release component.
//
// release_dependencies is first a compatibility claim ("I consume this peer's
// contract"), and only an edge between two deploy stages turns it into order:
// that peer's stage goes first. Inside one stage it orders nothing, so a
// two-way pair there (reliant-api-server <-> daemon-gateway, each declaring
// the other's NATS contract) is legal, not a cycle. A build-only peer
// (forge.BuildOnly: an image the release carries and something else runs) is
// never deployed and has no stage; the cut already satisfied the claim, so it
// orders nothing either. A cycle through cross-stage edges is refused here by
// item, and deployStageOrder refuses one at stage granularity.
func declaredPlacementEdges(e *KCLEntities) ([]stageEdge, error) { //nolint:funlen // one pass per declaring entity kind (workloads, frontends, manifests, …) over the same edge rules; splitting it per kind would scatter one rule set.
	if e == nil {
		return nil, nil
	}
	stages := map[string]deployStage{}
	addName := func(name string, s deployStage) error {
		if _, ok := stages[name]; ok {
			return fmt.Errorf("duplicate release component %q", name)
		}
		stages[name] = s
		return nil
	}
	var servers []string
	buildOnly := map[string]bool{} // peers with no stage that still name a component
	for _, w := range e.Workloads {
		if s := stageOfWorkload(w); s != "" {
			if err := addName(w.Name, s); err != nil {
				return nil, err
			}
			servers = append(servers, w.Name)
		} else if w.Runtime.Type == RuntimeBuildOnly {
			buildOnly[w.Name] = true
		}
	}
	for _, f := range e.Frontends {
		if s := stageOfFrontend(f); s != "" {
			if err := addName(f.Name, s); err != nil {
				return nil, err
			}
		}
	}
	for _, d := range e.Databases {
		if _, ok := stages[d.Name]; !ok {
			stages[d.Name] = stageOfDatabase(d)
		}
	}
	for _, m := range e.ReleaseManifestConsumers {
		if err := addName(m.Name, stageLocal); err != nil {
			return nil, err
		}
		servers = append(servers, m.Name)
	}
	providers, err := nativeProviders(e)
	if err != nil {
		return nil, err
	}
	for _, p := range providers {
		if err := addName(p.Owner, stageLocal); err != nil {
			return nil, err
		}
		servers = append(servers, p.Owner)
	}
	var edges []stageEdge
	add := func(name string, peers []string) error {
		for _, peer := range peers {
			from, ok := stages[peer]
			if !ok && buildOnly[peer] {
				continue
			}
			if !ok {
				return fmt.Errorf("release component %s has missing predecessor %q", name, peer)
			}
			if from == stages[name] {
				continue // same stage: a compatibility claim, not an order
			}
			edges = append(edges, stageEdge{Dependency: peer, Dependent: name, From: from, To: stages[name]})
		}
		return nil
	}
	for _, w := range e.Workloads {
		if stages[w.Name] != "" {
			if err := add(w.Name, sortedKeys(w.ReleaseDependencies)); err != nil {
				return nil, err
			}
		}
	}
	for _, f := range e.Frontends {
		if stages[f.Name] == "" {
			continue
		}
		peers := sortedKeys(f.ReleaseDependencies)
		if f.ReleaseDependencies == nil {
			peers = servers
		}
		if err := add(f.Name, peers); err != nil {
			return nil, err
		}
	}
	for _, m := range e.ReleaseManifestConsumers {
		if err := add(m.Name, sortedKeys(m.ReleaseDependencies)); err != nil {
			return nil, err
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Dependent != edges[j].Dependent {
			return edges[i].Dependent < edges[j].Dependent
		}
		return edges[i].Dependency < edges[j].Dependency
	})
	after := map[string][]stageEdge{}
	for _, ed := range edges {
		after[ed.Dependent] = append(after[ed.Dependent], ed)
	}
	state := map[string]int{}
	var path []stageEdge
	var visit func(string) error
	visit = func(name string) error {
		if state[name] == 1 {
			var parts []string
			for _, ed := range path {
				parts = append(parts, fmt.Sprintf("%s (%s) needs %s (%s)", ed.Dependent, ed.To, ed.Dependency, ed.From))
			}
			return fmt.Errorf("release dependency cycle: %s", strings.Join(parts, "; "))
		}
		if state[name] == 2 {
			return nil
		}
		state[name] = 1
		for _, ed := range after[name] {
			path = append(path, ed)
			if err := visit(ed.Dependency); err != nil {
				return err
			}
			path = path[:len(path)-1]
		}
		state[name] = 2
		return nil
	}
	for _, name := range sortedKeys(stages) {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return edges, nil
}

// bundlePlacementDeclarations is used at the bundle boundary, before any cut,
// push or record. Pure deployments retain their historical bytes and behavior.
// Native providers are placed like any cluster component: the bundle reads
// their forge.dev/release-owners ownership (internal/bundle placementOwners).
func bundlePlacementDeclarations(e *KCLEntities) ([]bundle.PlacementDeclaration, error) {
	return placementDeclarations(e)
}

// placementDeclarations is the complete runtime inventory of a mixed env:
// every release consumer compositionConsumers lists that runs somewhere,
// native providers included, plus hosted databases.
func placementDeclarations(e *KCLEntities) ([]bundle.PlacementDeclaration, error) {
	if e == nil {
		return nil, nil
	}
	hasCluster, hasHosted := false, false
	for _, w := range e.Workloads {
		hasCluster = hasCluster || w.Runtime.Type == RuntimeCluster
		hasHosted = hasHosted || w.Runtime.Type == RuntimeHosted
	}
	for _, d := range e.Databases {
		hasCluster = hasCluster || !d.Hosted()
		hasHosted = hasHosted || d.Hosted()
	}
	for _, f := range e.Frontends {
		hasHosted = hasHosted || frontendIsHosted(f)
	}
	providers, err := nativeProviders(e)
	if err != nil {
		return nil, err
	}
	hasCluster = hasCluster || len(e.ReleaseManifestConsumers) > 0 || len(providers) > 0
	if !hasCluster || !hasHosted {
		return nil, nil
	}
	edges, err := declaredPlacementEdges(e)
	if err != nil {
		return nil, err
	}
	deps := map[string][]string{}
	for _, ed := range edges {
		deps[ed.Dependent] = append(deps[ed.Dependent], ed.Dependency)
	}
	split, err := unitSplitOf(e)
	if err != nil {
		return nil, err
	}
	var out []bundle.PlacementDeclaration
	for _, w := range e.Workloads {
		runtime, actuator := "cluster", "forge"
		switch w.Runtime.Type {
		case RuntimeHosted:
			runtime, actuator = "hosted", "platform"
		case RuntimeCluster:
			if containsUnit(split.Hub, workloadUnit(w)) {
				actuator = "hub"
			}
		default:
			if stageOfWorkload(w) == "" {
				continue
			}
			return nil, fmt.Errorf("unsupported mixed workload runtime %s for %s", w.Runtime.Type, w.Name)
		}
		out = append(out, bundle.PlacementDeclaration{Workload: w.Name, Runtime: runtime, Actuator: actuator, Dependencies: deps[w.Name], Unit: workloadUnit(w)})
	}
	for _, f := range e.Frontends {
		if !frontendIsHosted(f) {
			return nil, fmt.Errorf("unsupported mixed frontend placement for %s", f.Name)
		}
		out = append(out, bundle.PlacementDeclaration{Workload: f.Name, Runtime: "hosted", Actuator: "platform", Dependencies: deps[f.Name]})
	}
	for _, d := range e.Databases {
		if !d.Hosted() {
			return nil, fmt.Errorf("unsupported mixed database controller for %s", d.Name)
		}
		out = append(out, bundle.PlacementDeclaration{Workload: d.Name, Runtime: "hosted", Actuator: "platform", Dependencies: deps[d.Name]})
	}
	for _, m := range e.ReleaseManifestConsumers {
		actuator := "forge"
		if len(split.Hub) > 0 {
			actuator = "hub"
		} // no inferred ownership for raw mixed units
		out = append(out, bundle.PlacementDeclaration{Workload: m.Name, Runtime: "cluster", Actuator: actuator, Dependencies: deps[m.Name]})
	}
	for _, p := range providers {
		actuator := "forge"
		if len(split.Hub) > 0 {
			actuator = "hub" // same as raw manifest consumers: no inferred unit ownership
		}
		out = append(out, bundle.PlacementDeclaration{Workload: p.Owner, Runtime: "cluster", Actuator: actuator, Dependencies: deps[p.Owner]})
	}
	for _, d := range out {
		if d.Actuator != "forge" && d.Actuator != "platform" {
			return nil, fmt.Errorf("unsupported mixed placement actuator %s for %s", d.Actuator, d.Workload)
		}
		for _, peer := range d.Dependencies {
			for _, predecessor := range out {
				if predecessor.Workload == peer && predecessor.Runtime == "hosted" {
					return nil, fmt.Errorf("unsupported mixed placement: hosted predecessor %s -> %s", peer, d.Workload)
				}
			}
		}
	}
	return out, nil
}

func preflightPlacementDeploy(ctx context.Context, projectDir, env string, e *KCLEntities) error {
	placements, err := bundlePlacementDeclarations(e)
	if err != nil || placements == nil {
		return err
	}
	ledger, err := bundleLedgerFor(ctx, projectDir, env)
	if err != nil {
		return err
	}
	return requirePlacementReader(ctx, projectDir, env, e, ledger)
}

// preflightPlacementBuild negotiates the recording reader for every bundle a
// build will seal or publish (--release, --push), before any build, push or
// cut. A compile-only build publishes nothing and never asks a control
// plane: its local bundle step skips a mixed bundle the reader cannot take
// (writeEnvBundle) instead.
func preflightPlacementBuild(ctx context.Context, e *KCLEntities, opts buildOptions) error {
	if opts.env == "" || opts.plan || (opts.release == "" && !opts.push && !opts.pushIfDeclared) {
		return nil
	}
	for _, env := range parseBundleEnvs(opts.bundleEnvs, opts.env) {
		declaration := e
		if env != opts.env {
			var err error
			declaration, err = renderKCLForDeclaration(ctx, projectDirForKCL(), env)
			if err != nil {
				return err
			}
		}
		if err := preflightPlacementDeploy(ctx, projectDirForKCL(), env, declaration); err != nil {
			return err
		}
	}
	return nil
}

func requirePlacementReader(ctx context.Context, projectDir, env string, e *KCLEntities, ledger envLedger) error {
	placements, err := bundlePlacementDeclarations(e)
	if err != nil || placements == nil {
		return err
	}
	reader, err := bundleReaderFor(ctx, projectDir, env, ledger)
	if err != nil {
		return err
	}
	if ledger.Hosted && reader == nil {
		return fmt.Errorf("mixed placements require an explicit recording reader capability report")
	}
	if !reader.Supports(release.BundleFeaturePlacementPrerequisites) {
		return fmt.Errorf("mixed placements require reader feature %s before effects; upgrade the recording control plane", release.BundleFeaturePlacementPrerequisites)
	}
	return nil
}

// nativeProvider is a forge-rendered component that is a release consumer
// without being a Workload: compositionConsumers keys it by Owner, and every
// object it renders carries forge.dev/release-owners [Owner]. The one native
// provider today is Bundle.openbao; a new one is added here and in
// compositionConsumers (TestPlacementInventoryIsCompositionConsumers).
type nativeProvider struct{ Owner string }

func nativeProviders(e *KCLEntities) ([]nativeProvider, error) {
	var out []nativeProvider
	if p := e.OpenBao; p != nil {
		if p.ReleaseOwner == "" || p.Cluster == "" {
			return nil, fmt.Errorf("native OpenBao provider %q lacks its release owner or cluster", p.Name)
		}
		out = append(out, nativeProvider{Owner: p.ReleaseOwner})
	}
	return out, nil
}

func workloadUnit(w WorkloadEntity) string {
	if w.DeliveryUnit != "" {
		return w.DeliveryUnit
	}
	return release.DefaultDeliveryUnit
}
