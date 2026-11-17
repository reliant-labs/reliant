package bundle

import (
	"fmt"
	"slices"
	"sort"

	"github.com/reliant-labs/forge/pkg/release"
)

// PlacementDeclaration is logical release dependency input, never a readiness claim.
// The actual keys and binding hashes are projected from the sealed parse by Build.
type PlacementDeclaration struct {
	Workload     string
	Runtime      string
	Actuator     string
	Dependencies []string
	Unit         string
}

// placementPrerequisites projects a mixed (cluster + hosted) release's
// readiness graph from the sealed parse. It runs in phases: classify the
// controllers into items, order them by declared and delivery-unit
// dependencies, bind the inventory to them, then validate and sort.
func placementPrerequisites(in BuildInput, docs []parsedDoc, objects []release.ShapeObject, trees []release.BundleTree) (*release.PlacementPrerequisites, error) {
	if !mixedPlacements(in.Placements, objects) {
		return nil, nil
	}
	if !in.Reader.Supports(release.BundleFeaturePlacementPrerequisites) {
		return nil, fmt.Errorf("mixed placements require reader feature %s before composition or publication; upgrade the recording control plane", release.BundleFeaturePlacementPrerequisites)
	}
	if in.Placements == nil {
		return nil, fmt.Errorf("mixed placements require complete placement dependency declarations")
	}
	placements := in.Placements
	if in.Snapshot != nil {
		var err error
		placements, err = snapshotPlacements(*in.Snapshot, placements)
		if err != nil {
			return nil, err
		}
	}
	decl, err := declarePlacements(placements)
	if err != nil {
		return nil, err
	}
	ownersOf, err := placementOwners(docs, decl)
	if err != nil {
		return nil, err
	}
	g := &placementGraph{
		p:             &release.PlacementPrerequisites{Version: release.PlacementPrerequisitesVersion, Items: []release.PlacementItem{}, Edges: []release.PlacementEdge{}, Bindings: []release.PlacementBinding{}},
		decl:          decl,
		ownersOf:      ownersOf,
		keys:          map[string][]release.PlacementKey{},
		inventoryOnly: map[string]bool{},
		edgeSet:       map[release.PlacementEdge]bool{},
	}
	if err := g.classify(objects); err != nil {
		return nil, err
	}
	if err := g.markInventoryOnly(placements); err != nil {
		return nil, err
	}
	if err := g.orderDeclared(placements); err != nil {
		return nil, err
	}
	g.orderUnits(docs, trees)
	if err := g.bind(objects); err != nil {
		return nil, err
	}
	if err := g.requireBound(objects); err != nil {
		return nil, err
	}
	if err := g.p.Validate(); err != nil {
		return nil, err
	}
	// The v1 actuator can hold one hosted group after a forge prerequisite
	// closure. Hosted->forge and hosted interleaving require a future scheduler.
	for _, e := range g.p.Edges {
		if e.Predecessor.Runtime == "hosted" {
			return nil, fmt.Errorf("unsupported mixed placement edge %s -> %s: hosted predecessor/interleaving", e.Predecessor.Name, e.Dependent.Name)
		}
	}
	sortPlacementPrerequisites(g.p)
	return g.p, nil
}

// mixedPlacements reports whether the release places workloads both in a
// cluster and on hosting, by its sealed objects or its declarations. Only a
// mixed release carries placement prerequisites.
func mixedPlacements(placements []PlacementDeclaration, objects []release.ShapeObject) bool {
	cluster, hosted := false, false
	for _, o := range objects {
		if o.Cluster == release.BundleHostedCluster {
			hosted = true
		} else if o.Cluster != "" {
			cluster = true
		}
	}
	for _, d := range placements {
		cluster = cluster || d.Runtime == "cluster"
		hosted = hosted || d.Runtime == "hosted"
	}
	return cluster && hosted
}

// declarePlacements indexes the declarations by workload, refusing a
// duplicate and any runtime/actuator pair the v1 actuator cannot run.
func declarePlacements(placements []PlacementDeclaration) (map[string]PlacementDeclaration, error) {
	decl := map[string]PlacementDeclaration{}
	for _, d := range placements {
		if _, ok := decl[d.Workload]; ok {
			return nil, fmt.Errorf("duplicate placement workload %q", d.Workload)
		}
		if !((d.Runtime == "cluster" && d.Actuator == "forge") || (d.Runtime == "hosted" && d.Actuator == "platform")) {
			return nil, fmt.Errorf("unsupported mixed placement %s: %s/%s", d.Workload, d.Runtime, d.Actuator)
		}
		decl[d.Workload] = d
	}
	return decl, nil
}

// placementGraph is the prerequisites under construction, plus the indexes
// its phases share.
type placementGraph struct {
	p        *release.PlacementPrerequisites
	decl     map[string]PlacementDeclaration
	ownersOf map[release.ObjectKey][]string
	// keys is each declared workload's items.
	keys map[string][]release.PlacementKey
	// inventoryOnly marks the cluster components that render no controller.
	inventoryOnly map[string]bool
	edgeSet       map[release.PlacementEdge]bool
}

func (g *placementGraph) owners(o release.ShapeObject) []string {
	if o.Cluster == release.BundleHostedCluster {
		return []string{o.Name} // a hosted record is its own component
	}
	return g.ownersOf[o.Key()]
}

func (g *placementGraph) addEdge(a, b release.PlacementKey) {
	e := release.PlacementEdge{Predecessor: a, Dependent: b}
	if !g.edgeSet[e] {
		g.edgeSet[e] = true
		g.p.Edges = append(g.p.Edges, e)
	}
}

// classify makes the items. Only actual controller/Job and hosted record
// identities are items. Helpers bind to their logical component, never
// invented pseudo-status objects.
func (g *placementGraph) classify(objects []release.ShapeObject) error {
	for _, o := range objects {
		if !placementPrimary(o.Kind, o.Cluster == release.BundleHostedCluster) {
			continue
		}
		declared := []string{}
		for _, owner := range g.owners(o) {
			if _, ok := g.decl[owner]; ok {
				declared = append(declared, owner)
			}
		}
		if len(declared) == 0 {
			continue
		}
		if len(declared) > 1 {
			return fmt.Errorf("unsupported mixed placement: controller %s/%s has several release owners %v", o.Kind, o.Name, declared)
		}
		workload := declared[0]
		d := g.decl[workload]
		runtime, actuator := "cluster", "forge"
		if o.Cluster == release.BundleHostedCluster {
			runtime, actuator = "hosted", "platform"
		}
		if runtime != d.Runtime || actuator != d.Actuator {
			return fmt.Errorf("placement declaration and sealed object disagree for %s", workload)
		}
		k := release.PlacementKey{Runtime: runtime, Actuator: actuator, Cluster: o.Cluster, Namespace: o.Namespace, Kind: o.Kind, Name: o.Name}
		g.keys[workload] = append(g.keys[workload], k)
		g.p.Items = append(g.p.Items, release.PlacementItem{Key: k, Workload: workload})
	}
	return nil
}

// markInventoryOnly records the cluster components that render no controller
// (a raw manifest consumer of routes or policies, a release-input contract
// with no objects): applied inventory, with nothing of its own to observe, so
// no item and no predecessor. A hosted one without its record is refused.
func (g *placementGraph) markInventoryOnly(placements []PlacementDeclaration) error {
	for _, d := range placements {
		if len(g.keys[d.Workload]) == 0 {
			if d.Runtime != "cluster" {
				return fmt.Errorf("placement workload %q has no sealed controller/record", d.Workload)
			}
			g.inventoryOnly[d.Workload] = true
		}
	}
	return nil
}

// orderDeclared adds an edge for every declared dependency.
func (g *placementGraph) orderDeclared(placements []PlacementDeclaration) error {
	for _, d := range placements {
		for _, peer := range d.Dependencies {
			resolved, err := g.declaredPredecessors(peer, map[string]bool{})
			if err != nil {
				return fmt.Errorf("placement %s has %w", d.Workload, err)
			}
			for _, a := range resolved {
				for _, b := range g.keys[d.Workload] {
					g.addEdge(a, b)
				}
			}
		}
	}
	return nil
}

// declaredPredecessors resolves a dependency on an inventory-only component
// THROUGH it to the nearest components with items: web -> routes (no
// controller) -> api still makes web wait for api. A cycle that closes on an
// item surfaces as a self or cyclic edge, which Validate refuses.
func (g *placementGraph) declaredPredecessors(peer string, seen map[string]bool) ([]release.PlacementKey, error) {
	if !g.inventoryOnly[peer] {
		if len(g.keys[peer]) == 0 {
			return nil, fmt.Errorf("missing predecessor %q", peer)
		}
		return g.keys[peer], nil
	}
	if seen[peer] {
		return nil, nil
	}
	seen[peer] = true
	var out []release.PlacementKey
	for _, next := range g.decl[peer].Dependencies {
		ks, err := g.declaredPredecessors(next, seen)
		if err != nil {
			return nil, err
		}
		out = append(out, ks...)
	}
	return out, nil
}

// orderUnits adds the delivery-unit edges. Unit order is the sealed trees'
// own (release.PlanTrees over the complete inventory, typos already refused
// there): the implicit base dependency exists only when base renders, and a
// unit holding only support objects passes ordering through without becoming
// an invented readiness item.
func (g *placementGraph) orderUnits(docs []parsedDoc, trees []release.BundleTree) {
	unitItems := g.unitItems(docs)
	unitAfter := unitDependencies(trees)
	for unit, items := range unitItems {
		for _, a := range unitPredecessors(unit, unitAfter, unitItems, map[string]bool{}) {
			for _, b := range items {
				g.addEdge(a, b)
			}
		}
	}
}

// unitItems is each delivery unit's cluster items.
func (g *placementGraph) unitItems(docs []parsedDoc) map[string][]release.PlacementKey {
	objectUnits := map[release.ObjectKey]string{}
	for _, doc := range docs {
		for _, c := range doc.clusters {
			objectUnits[release.ObjectKey{Cluster: c, Kind: doc.meta.kind, Namespace: doc.meta.namespace, Name: doc.meta.name}] = docUnit(doc)
		}
	}
	unitItems := map[string][]release.PlacementKey{}
	for _, item := range g.p.Items {
		k := item.Key
		if k.Runtime == "hosted" {
			continue // hosted records are flat: no delivery unit
		}
		u := objectUnits[release.ObjectKey{Cluster: k.Cluster, Kind: k.Kind, Namespace: k.Namespace, Name: k.Name}]
		unitItems[u] = append(unitItems[u], k)
	}
	return unitItems
}

// unitDependencies is, per unit, the other units its trees depend on.
func unitDependencies(trees []release.BundleTree) map[string]map[string]bool {
	treeAt := map[string]release.BundleTree{}
	for _, t := range trees {
		treeAt[t.Path] = t
	}
	unitAfter := map[string]map[string]bool{}
	for _, t := range trees {
		for _, path := range t.DependsOn {
			if dep, ok := treeAt[path]; ok && dep.Unit != t.Unit {
				if unitAfter[t.Unit] == nil {
					unitAfter[t.Unit] = map[string]bool{}
				}
				unitAfter[t.Unit][dep.Unit] = true
			}
		}
	}
	return unitAfter
}

// unitPredecessors are the nearest units with items, walking through
// support-only units; their own predecessors order them transitively.
func unitPredecessors(unit string, unitAfter map[string]map[string]bool, unitItems map[string][]release.PlacementKey, seen map[string]bool) []release.PlacementKey {
	var out []release.PlacementKey
	for dep := range unitAfter[unit] {
		if seen[dep] {
			continue
		}
		seen[dep] = true
		if len(unitItems[dep]) > 0 {
			out = append(out, unitItems[dep]...)
		} else {
			out = append(out, unitPredecessors(dep, unitAfter, unitItems, seen)...)
		}
	}
	return out
}

// bind binds the full component inventory and broadcasts pre-rollout
// Jobs/configs. Secret documents have already been removed and are never
// inspected here.
func (g *placementGraph) bind(objects []release.ShapeObject) error {
	bindingSet := map[release.PlacementBinding]bool{}
	for _, o := range objects {
		targets, err := g.bindTargets(o)
		if err != nil {
			return err
		}
		if o.Cluster != release.BundleHostedCluster && !placementBindable(o.Kind) {
			continue // applied inventory, not an invented readiness status
		}
		for _, k := range targets {
			if k.Cluster != o.Cluster || k.Namespace != o.Namespace {
				continue
			}
			b := release.PlacementBinding{Item: k, Object: release.PlacementObject{Cluster: o.Cluster, APIVersion: o.APIVersion, Kind: o.Kind, Namespace: o.Namespace, Name: o.Name}, ManifestHash: o.Hash}
			if o.Kind == "Job" {
				b.JobContract = release.KubernetesJobCompleteV1
			}
			if !bindingSet[b] {
				bindingSet[b] = true
				g.p.Bindings = append(g.p.Bindings, b)
			}
		}
	}
	return nil
}

// bindTargets is the items a sealed object binds to: its owners' items, or —
// for an unowned clustered controller or ConfigMap — every cluster item of
// its namespace.
func (g *placementGraph) bindTargets(o release.ShapeObject) ([]release.PlacementKey, error) {
	var targets []release.PlacementKey
	owned := false
	for _, owner := range g.owners(o) {
		if g.inventoryOnly[owner] {
			continue // its objects are env-level, as an unowned object's are
		}
		if len(g.keys[owner]) == 0 {
			return nil, fmt.Errorf("sealed object %s/%s has undeclared placement owner %q", o.Kind, o.Name, owner)
		}
		owned = true
		targets = append(targets, g.keys[owner]...)
	}
	if !owned && o.Cluster != release.BundleHostedCluster && placementBindable(o.Kind) {
		for _, item := range g.p.Items {
			if item.Key.Cluster == o.Cluster && item.Key.Namespace == o.Namespace && item.Key.Runtime == "cluster" {
				targets = append(targets, item.Key)
			}
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("unbound broadcast prerequisite %s/%s", o.Kind, o.Name)
		}
	}
	if o.Cluster == release.BundleHostedCluster && len(targets) == 0 {
		return nil, fmt.Errorf("unsupported/unbound hosted sealed record %s/%s", o.Kind, o.Name)
	}
	return targets, nil
}

// requireBound checks that every clustered runtime object and ConfigMap is
// bound to an item of its own namespace, the rule a control plane's reader
// admits by: an object left unbound (its only owner's controllers live in
// another namespace) would be refused there, after the release was built.
func (g *placementGraph) requireBound(objects []release.ShapeObject) error {
	bound := map[release.ObjectKey]bool{}
	for _, b := range g.p.Bindings {
		bound[release.ObjectKey{Cluster: b.Object.Cluster, Kind: b.Object.Kind, Namespace: b.Object.Namespace, Name: b.Object.Name}] = true
	}
	for _, o := range objects {
		if o.Cluster != "" && o.Cluster != release.BundleHostedCluster && placementBindable(o.Kind) && !bound[o.Key()] {
			return fmt.Errorf("sealed %s %s/%s is bound to no placement item in its namespace: its owners %v have no controller there",
				o.Kind, o.Namespace, o.Name, g.owners(o))
		}
	}
	return nil
}

// sortPlacementPrerequisites puts items, edges and bindings in their
// canonical order, so the sealed bytes do not depend on map iteration.
func sortPlacementPrerequisites(p *release.PlacementPrerequisites) {
	sort.Slice(p.Items, func(i, j int) bool { return placementKeyOrder(p.Items[i].Key) < placementKeyOrder(p.Items[j].Key) })
	sort.Slice(p.Edges, func(i, j int) bool {
		a, b := p.Edges[i], p.Edges[j]
		return placementKeyOrder(a.Predecessor)+"|"+placementKeyOrder(a.Dependent) < placementKeyOrder(b.Predecessor)+"|"+placementKeyOrder(b.Dependent)
	})
	sort.Slice(p.Bindings, func(i, j int) bool {
		a, b := p.Bindings[i], p.Bindings[j]
		return placementKeyOrder(a.Item)+fmt.Sprint(a.Object) < placementKeyOrder(b.Item)+fmt.Sprint(b.Object)
	})
}

// Frozen consumer declarations, not the current checkout, govern carried peers.
func snapshotPlacements(s release.Snapshot, declarations []PlacementDeclaration) ([]PlacementDeclaration, error) {
	byName := map[string]PlacementDeclaration{}
	for _, d := range declarations {
		byName[d.Workload] = d
	}
	var servers []string
	for _, c := range s.Consumers {
		for _, slot := range c.Slots {
			if slot != "frontend" && slot != "build-only" {
				servers = append(servers, c.Key.Workload)
				break
			}
		}
	}
	var out []PlacementDeclaration
	for _, c := range s.Consumers {
		// A build-only artifact has no runtime placement: it stays in the
		// snapshot's artifacts and is no prerequisite item.
		if slices.Equal(c.Slots, []string{"build-only"}) {
			continue
		}
		d, ok := byName[c.Key.Workload]
		if !ok {
			return nil, fmt.Errorf("snapshot consumer %s lacks placement declaration", c.Key.Workload)
		}
		d.Dependencies = nil
		frontend := false
		for _, slot := range c.Slots {
			frontend = frontend || slot == "frontend"
		}
		if frontend && !c.DependenciesDeclared {
			d.Dependencies = append(d.Dependencies, servers...)
		} else {
			for _, peer := range c.DependsOn {
				d.Dependencies = append(d.Dependencies, peer.Consumer.Workload)
			}
		}
		out = append(out, d)
	}
	return out, nil
}

// placementOwners is each sealed object's owners by releaseOwnershipOf, the
// rule SnapshotInventory uses. A forge-managed app name counts only when it
// names a declared component; an object with no owner is env-level.
func placementOwners(docs []parsedDoc, declared map[string]PlacementDeclaration) (map[release.ObjectKey][]string, error) {
	out := map[release.ObjectKey][]string{}
	for _, doc := range docs {
		body, _ := doc.body.(map[string]any)
		own, err := releaseOwnershipOf(mapAt(body, "metadata"))
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", doc.meta.kind, doc.meta.name, err)
		}
		names := own.Owners
		if own.Owners == nil && own.Label != "" {
			if _, ok := declared[own.Label]; own.Explicit || ok {
				names = []string{own.Label}
			}
		}
		for _, c := range doc.clusters {
			out[release.ObjectKey{Cluster: c, Kind: doc.meta.kind, Namespace: doc.meta.namespace, Name: doc.meta.name}] = names
		}
	}
	return out, nil
}

func placementPrimary(kind string, hosted bool) bool {
	if hosted {
		return kind == "StaticSite" || kind == "Workload" || kind == "ManagedDatabase"
	}
	return release.PlacementRuntimeKind(kind)
}

// placementBindable is a clustered object kind that binds to a placement
// item: a runtime controller/Job, or a ConfigMap.
func placementBindable(kind string) bool {
	return placementPrimary(kind, false) || kind == "ConfigMap"
}

func placementKeyOrder(k release.PlacementKey) string {
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s", k.Runtime, k.Actuator, k.Cluster, k.Namespace, k.Kind, k.Name)
}
