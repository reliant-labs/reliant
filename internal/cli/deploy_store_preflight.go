package cli

// STORE-BACKED SECRETS AT DEPLOY TIME (SF9).
//
// A forge.ExternalSecret with a forge.StoreSource is not a precondition the
// cluster must already satisfy, and not a Secret forge applies: the External
// Secrets Operator creates it after the deploy. Three things in this file
// follow from that, and each is the preflight's half of one declaration:
//
//   - the SUPPLY: such a Secret counts as supplied for the render-time gate
//     (secretSupplyForPreflight keeps it as an ExternalSecret promise), and an
//     Orphan one is exempted from the live existence check
//     (storeProvisionedByDeploy), because its deliverer is waiting for this
//     deploy. A Merge one keeps the ordinary check: ESO only updates a Secret
//     that already exists, so its absence is a real gap;
//   - the EXPECTATION: per cluster, the operator, the store and the names the
//     ExternalSecrets read (storeExpectations), checked by cluster.CheckStore;
//   - the EXEMPTION from a hub-reconciled deploy's "forge cannot deliver this
//     value" refusal (storeBackedSecretRefs), because ESO delivers it.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/deploy/eso"
	"github.com/reliant-labs/forge/pkg/release"
)

// storeSecretContext is the kubectl context a store-backed Secret lands on:
// its source's own cluster, else the env's primary one.
func storeSecretContext(e *KCLEntities, s ExternalSecretEntity) string {
	if s.Source != nil && s.Source.Cluster != "" {
		return s.Source.Cluster
	}
	return e.ClusterTarget.field("cluster")
}

// storeProvisionedByDeploy lists the store-backed Secrets this deploy's
// deliverer creates AFTER the preflight, so the live existence check does not
// demand them up front. Orphan only: see the file comment.
func storeProvisionedByDeploy(e *KCLEntities) []cluster.ProvisionedSecret {
	if e == nil {
		return nil
	}
	var out []cluster.ProvisionedSecret
	for _, s := range e.RequiredSecrets {
		if s.Source == nil || s.Source.CreationPolicy == "Merge" {
			continue
		}
		out = append(out, cluster.ProvisionedSecret{
			Name: s.Name, Namespace: s.Namespace, By: cluster.SupplyExternalSecret,
		})
		if s.Source.Shadow {
			out = append(out, cluster.ProvisionedSecret{
				Name: s.Name + "-eso-shadow", Namespace: s.Namespace, By: cluster.SupplyExternalSecret,
			})
		}
	}
	return out
}

// storeBackedSecretRefs are the Secrets (cluster, namespace, name) ESO
// delivers, so a hub-reconciled deploy must not demand forge sync them.
func storeBackedSecretRefs(e *KCLEntities) map[release.BundleSecretRef]struct{} {
	out := map[release.BundleSecretRef]struct{}{}
	if e == nil {
		return out
	}
	for _, s := range e.RequiredSecrets {
		if s.Source == nil {
			continue
		}
		names := []string{s.Name}
		if s.Source.Shadow {
			names = append(names, s.Name+"-eso-shadow")
		}
		for _, n := range names {
			// The ref a bundle records carries the cluster when the render
			// routed the object; match on namespace and name alone, because
			// ESO delivers the Secret wherever its ExternalSecret lands.
			out[release.BundleSecretRef{Namespace: s.Namespace, Name: n}] = struct{}{}
		}
	}
	return out
}

// withoutStoreBacked drops the refs ESO delivers.
func withoutStoreBacked(refs []release.BundleSecretRef, e *KCLEntities) []release.BundleSecretRef {
	delivered := storeBackedSecretRefs(e)
	if len(delivered) == 0 {
		return refs
	}
	out := refs[:0:0]
	for _, r := range refs {
		if _, ok := delivered[release.BundleSecretRef{Namespace: r.Namespace, Name: r.Name}]; ok {
			continue
		}
		out = append(out, r)
	}
	return out
}

// storeExpectations projects the render onto what each cluster must hold,
// honouring the deploy's --target: an operator or store installs in THIS deploy
// only when its chart/record is selected.
func storeExpectations(e *KCLEntities, targets []string, owner string) []cluster.StoreExpectation {
	if e == nil || !hasStoreSources(e) {
		return nil
	}
	selected := func(name string) bool {
		if len(targets) == 0 {
			return true
		}
		for _, t := range targets {
			if t == name {
				return true
			}
		}
		return false
	}

	byCtx := map[string]*cluster.StoreExpectation{}
	get := func(ctx string) *cluster.StoreExpectation {
		if byCtx[ctx] == nil {
			byCtx[ctx] = &cluster.StoreExpectation{Context: ctx, Owner: owner}
		}
		return byCtx[ctx]
	}

	for _, op := range e.ExternalSecretsOperators {
		if op.Cluster != "" && selected(eso.ChartName) {
			get(op.Cluster).InstallsESO = true
		}
	}
	if ob := e.OpenBao; ob != nil && ob.Cluster != "" {
		get(ob.Cluster).OpenBao = &cluster.OpenBaoExpectation{
			Name: ob.Name, Namespace: ob.Namespace, Installs: selected(ob.Name),
		}
	}

	type storeKey struct{ ctx, kind, project string }
	names := map[storeKey]map[string]bool{}
	for _, s := range e.RequiredSecrets {
		if s.Source == nil {
			continue
		}
		ctx := storeSecretContext(e, s)
		if ctx == "" {
			continue
		}
		get(ctx)
		k := storeKey{ctx, s.Source.Store.Type, s.Source.Store.Project}
		if names[k] == nil {
			names[k] = map[string]bool{}
		}
		generated := map[string]bool{}
		for n, g := range s.Source.Generate {
			generated[n] = true
			if g.Companion != "" {
				generated[g.Companion] = true
			}
		}
		for _, name := range s.Source.Data {
			// A name the source generates is minted at promotion, not set by a
			// person: its absence before the first deploy is the point.
			if !generated[name] {
				names[k][name] = true
			}
		}
	}

	for k, set := range names {
		if len(set) == 0 {
			continue
		}
		list := make([]string, 0, len(set))
		for n := range set {
			list = append(list, n)
		}
		sort.Strings(list)
		x := get(k.ctx)
		x.Reads = append(x.Reads, cluster.StoreRead{
			Kind: k.kind, Project: k.project, Names: list,
			// An OpenBao this deploy installs holds nothing yet.
			Pending: k.kind == release.StoreOpenBao && x.OpenBao != nil && x.OpenBao.Installs,
		})
	}

	out := make([]cluster.StoreExpectation, 0, len(byCtx))
	for _, x := range byCtx {
		sort.Slice(x.Reads, func(i, j int) bool { return x.Reads[i].Label() < x.Reads[j].Label() })
		out = append(out, *x)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Context < out[j].Context })
	return out
}

// storePresence answers StoreProbe.Presence through the same seam `forge secret
// list` uses, so a name is "present" by exactly the rule the CLI reports.
func storePresence(envName string, e *KCLEntities) func(ctx context.Context, kctx string, read cluster.StoreRead) (map[string]bool, error) {
	return func(ctx context.Context, kctx string, read cluster.StoreRead) (map[string]bool, error) {
		ref := storeRef{Kind: read.Kind, Project: read.Project}
		src, err := openStoreSource(ctx, envName, e, ref, storeOptions{KubeContext: kctx}, false)
		if errors.Is(err, errHostedEnvNotFound) {
			// An env the control plane has not seen holds no secrets: every name
			// is missing, the same answer `forge secret list` gives.
			return map[string]bool{}, nil
		}
		if err != nil {
			return nil, err
		}
		versions, err := src.presence(ctx, read.Names)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src.describe(), err)
		}
		out := make(map[string]bool, len(versions))
		for n := range versions {
			out[n] = true
		}
		return out, nil
	}
}

// storeOwner is the component-owner stamp this env's operator carries.
func storeOwner(project, env string) string {
	return strings.TrimSpace(cluster.CRDOwner(project, env))
}

// liveStoreProbe is the probe a real deploy uses: kubectl for the cluster, and
// the store clients `forge secret` uses for the names. nil when the env has no
// store-backed Secret, so every other deploy is byte-identical.
func liveStoreProbe(envName string, e *KCLEntities) cluster.StoreProbe {
	if !hasStoreSources(e) {
		return nil
	}
	return cluster.KubectlStoreProbe{PresenceFn: storePresence(envName, e)}
}
