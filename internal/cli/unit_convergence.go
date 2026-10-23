package cli

// WHO APPLIES EACH DELIVERY UNIT of an env: the hub, or forge directly.
//
// This replaces the env-level "the hub converges this env" boolean, which could
// only say yes or no for the whole env. Every real env, the platform's own
// included, is moving onto build -> OCI bundle -> ledger -> Flux, and the move
// is made ONE UNIT AT A TIME (design §11): reliant first, then daemon-substrate,
// infra, control-plane, hub-base. During that move an env is neither fully
// forge-applied nor fully hub-converged, so the answer has to be per unit.
//
// THE SPLIT IS A DECLARATION. Bundle.hub_converges names the units the hub
// converges; every other unit of a real cluster is applied by `forge env
// deploy`. Three sources, in order of precedence:
//
//   - hub_converges  — the author listed the units (possibly none). Exactly what
//     was written, never augmented.
//   - applier = "forge" — the transitional "no unit has moved yet" spelling of
//     hub_converges = [].
//   - inferred — neither is declared: a plain real env with every cluster bound
//     to a connected cluster, nothing hosted, and nothing forge ships from this
//     machine, is converged by the hub as a whole. This is the rule the env-level
//     model had, kept for envs that already work; it is conservative on purpose
//     (an env that ships a Firebase frontend is NOT inferred, because handing
//     production to a second applier is the owner's decision, not forge's).
//
// A unit is a name from Bundle.delivery_units, or "base" (objects that declare
// no unit). Objects forge applies from outside the bundle's trees — compose
// services, host infra, shipped frontends, helm charts — belong to no unit and
// are always forge's.

import (
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/pkg/release"
)

// unitSplitSource says where a unitSplit came from.
type unitSplitSource string

const (
	// unitSplitNone: nothing declares or implies a split; forge applies the
	// env as it always did and nothing is recorded.
	unitSplitNone unitSplitSource = ""
	// unitSplitDeclared: Bundle.hub_converges.
	unitSplitDeclared unitSplitSource = "hub_converges"
	// unitSplitApplier: Bundle.applier = "forge".
	unitSplitApplier unitSplitSource = "applier"
	// unitSplitInferred: forge's inference for an env that declares neither.
	unitSplitInferred unitSplitSource = "inferred"
)

// unitSplit is the per-unit answer to "who applies this".
type unitSplit struct {
	// Hub are the delivery units the control plane's hub converges. Sorted.
	Hub []string
	// Forge are the delivery units `forge env deploy` applies directly. Sorted.
	Forge []string
	// Source is where the split came from.
	Source unitSplitSource
}

// AnyHub reports whether the hub converges at least one unit.
func (s unitSplit) AnyHub() bool { return len(s.Hub) > 0 }

// AllHub reports whether the hub converges every unit, so forge applies no
// cluster object at all.
func (s unitSplit) AllHub() bool { return len(s.Hub) > 0 && len(s.Forge) == 0 }

// IsHub reports whether the hub converges unit.
func (s unitSplit) IsHub(unit string) bool {
	if unit == "" {
		unit = release.DefaultDeliveryUnit
	}
	for _, h := range s.Hub {
		if h == unit {
			return true
		}
	}
	return false
}

// Applier is who applies unit: release.UnitApplierHub or UnitApplierForge.
func (s unitSplit) Applier(unit string) string {
	if s.IsHub(unit) {
		return release.UnitApplierHub
	}
	return release.UnitApplierForge
}

// ShapeUnits is the split as the release shape records it: one entry per unit,
// or nil when nothing declares or implies a split (so an env that never opted in
// encodes and hashes as it always did).
func (s unitSplit) ShapeUnits() []release.ShapeUnit {
	if s.Source == unitSplitNone || len(s.Hub)+len(s.Forge) == 0 {
		return nil
	}
	out := make([]release.ShapeUnit, 0, len(s.Hub)+len(s.Forge))
	for _, u := range s.Hub {
		out = append(out, release.ShapeUnit{Name: u, Applier: release.UnitApplierHub})
	}
	for _, u := range s.Forge {
		out = append(out, release.ShapeUnit{Name: u, Applier: release.UnitApplierForge})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Describe is the split as one human line: "hub: reliant, infra; forge: base".
func (s unitSplit) Describe() string {
	part := func(label string, units []string) string {
		if len(units) == 0 {
			return label + ": (none)"
		}
		return label + ": " + strings.Join(units, ", ")
	}
	return part("hub", s.Hub) + "; " + part("forge", s.Forge)
}

// unitUniverse is every delivery unit the env's objects can belong to, sorted:
// the declared units, any a workload names, the units on raw manifests, and the
// default unit.
func unitUniverse(e *KCLEntities) []string {
	seen := map[string]bool{release.DefaultDeliveryUnit: true}
	for _, u := range e.DeliveryUnits {
		seen[u.Name] = true
	}
	for _, w := range e.Workloads {
		if w.DeliveryUnit != "" {
			seen[w.DeliveryUnit] = true
		}
	}
	for _, u := range e.ManifestUnits {
		seen[u] = true
	}
	out := make([]string, 0, len(seen))
	for u := range seen {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// unitSplitOf derives the split from the render. It is a pure function of the
// declaration: nothing about the ledger, the machine or the environment.
//
// An env with no control plane has no hub, so its split is always empty. An
// error means the declaration cannot be honoured (a hub that has no cluster to
// converge, or a hosted tier beside a hub-converged unit), and it is returned
// rather than papered over: the alternative is a deploy that silently applies
// less than the author declared.
func unitSplitOf(e *KCLEntities) (unitSplit, error) {
	if e == nil || e.ControlPlane == nil {
		return unitSplit{}, nil
	}
	universe := unitUniverse(e)
	allForge := func(src unitSplitSource) unitSplit {
		return unitSplit{Forge: universe, Source: src}
	}
	switch {
	case e.HubConverges != nil:
		hub := append([]string(nil), e.HubConverges...)
		sort.Strings(hub)
		if len(hub) == 0 {
			return allForge(unitSplitDeclared), nil
		}
		if err := hubUnitsConvergeable(e, hub); err != nil {
			return unitSplit{}, err
		}
		var forge []string
		for _, u := range universe {
			if !containsUnit(hub, u) {
				forge = append(forge, u)
			}
		}
		return unitSplit{Hub: hub, Forge: forge, Source: unitSplitDeclared}, nil
	case e.Applier == applierForge:
		return allForge(unitSplitApplier), nil
	case envInferredHubConverged(e):
		return unitSplit{Hub: universe, Source: unitSplitInferred}, nil
	}
	return unitSplit{Forge: universe}, nil
}

// hubUnitsConvergeable refuses a hub_converges the hub could not act on.
func hubUnitsConvergeable(e *KCLEntities, hub []string) error {
	if bound, _ := declaredBindings(e); len(bound) == 0 {
		return fmt.Errorf("bundle.hub_converges = [%s] but no cluster is bound to a connected cluster: "+
			"the hub converges a unit only onto a connected cluster (set connected_cluster on the env's forge.ClusterTarget, "+
			"or Bundle.connected_clusters), and `forge cluster connect` registers it", strings.Join(hub, ", "))
	}
	if e.HasHosted() {
		return fmt.Errorf("bundle.hub_converges = [%s] but the env also binds hosted tiers (%s) to its control plane: "+
			"a hosted tier is published, not converged from a bundle, and one env cannot be both. "+
			"Move the hosted tiers to their own env, or leave hub_converges unset",
			strings.Join(hub, ", "), joinTiers(hostedTiersOf(e)))
	}
	return nil
}

// envInferredHubConverged is the inference that applied before units existed:
// the env's whole local-apply surface is clusters bound to connected clusters,
// with nothing hosted and nothing forge ships from this machine. See the file
// header for why a shipped frontend still keeps an undeclared env forge-applied.
func envInferredHubConverged(e *KCLEntities) bool {
	if e == nil || e.HasHosted() {
		return false
	}
	if bound, _ := declaredBindings(e); len(bound) == 0 {
		return false
	}
	if len(e.WorkloadsOn(RuntimeCompose)) > 0 || len(e.Infra) > 0 {
		return false
	}
	for _, f := range e.Frontends {
		if f.Runtime.Ships() || f.Runtime.Type == FrontendRuntimeBuildOnly {
			return false
		}
	}
	return true
}

// envClusterWork reports whether the env puts anything on a cluster forge could
// apply: the part of envAppliesLocally that delivery units can hand to the hub.
func envClusterWork(e *KCLEntities) bool {
	if len(e.WorkloadsOn(RuntimeCluster)) > 0 || e.ClusterTarget.field("cluster") != "" || len(e.ManifestClusters) > 0 {
		return true
	}
	for _, d := range e.Databases {
		if !d.Hosted() {
			return true
		}
	}
	return false
}

// forgeAppliesLocally reports whether `forge env deploy` has anything to apply
// from this machine once the split has handed units to the hub: the work that
// is not in any unit (compose, host infra, a shipped frontend, a helm chart,
// secrets forge renders or mints) is always forge's, and cluster work is
// forge's for every unit the hub does not converge.
func forgeAppliesLocally(e *KCLEntities, split unitSplit) bool {
	if e == nil {
		return false
	}
	if len(e.WorkloadsOn(RuntimeCompose)) > 0 || len(e.Infra) > 0 || forgeOwnsClusterExtras(e) {
		return true
	}
	for _, f := range e.Frontends {
		if f.Runtime.Ships() || f.Runtime.Type == FrontendRuntimeBuildOnly {
			return true
		}
	}
	for _, d := range e.Databases {
		if !d.Hosted() && len(split.Forge) > 0 {
			return true
		}
	}
	return envClusterWork(e) && len(split.Forge) > 0
}

// forgeOwnsClusterExtras reports cluster-side work that belongs to no delivery
// unit and so is forge's whatever the split says: a helm chart (the platform
// dependencies a foundation env owns once it exists), a Secret forge renders,
// and a kubeconfig forge mints.
func forgeOwnsClusterExtras(e *KCLEntities) bool {
	return len(e.HelmCharts) > 0 || len(e.RenderedSecrets) > 0 || len(e.KubeconfigSecrets) > 0
}

func containsUnit(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// hubConvergedWire is the split as the EnsureEnvironment spec carries it: the
// env-level summary an older control plane reads (true only when the hub
// converges every unit), and the per-unit list a current one reads. The summary
// is conservative on purpose; see deploytarget.HostedEnvRef.HubConverged.
func hubConvergedWire(s unitSplit) (hubConverged *bool, units *[]string) {
	all := s.AllHub()
	hub := append([]string{}, s.Hub...)
	return &all, &hub
}
