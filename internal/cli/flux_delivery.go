package cli

// Bundle.applier = "flux" — THE SELF-HOSTED DELIVERY.
//
// An env with no control plane can be reconciled the way a hub reconciles a
// hosted one: forge records each release in its file ledger, and a Flux that
// runs IN THE ENV'S OWN CLUSTER applies the release's bundle. forge never
// applies the bundle itself. The env opts in by declaring it; an env that
// declares nothing deploys exactly as before.
//
// This file is the forge side of the BOOTSTRAP — everything that Flux needs
// before a release can be pointed at it — and the refusals that keep forge
// from becoming a second applier:
//
//   - the bootstrap is one platform chart the render adds (fluxdelivery.ChartName:
//     the Flux Operator, the env's delivery namespace, its ResourceSet, the
//     apply identities' ServiceAccounts; kcl/lib/flux_delivery.k). Its install
//     adds what only Go can know: the identities' GRANTS, derived from the
//     env's render (flux_delivery_identity.go), and a COPY of the registry pull
//     Secret. `forge env deploy <env> --target flux-delivery` installs it, with
//     no release: a bootstrap chart rides no bundle;
//   - a release deploy records the release, pushes its bundle and writes the
//     POINTER the delivery ResourceSet reads (flux_delivery_deploy.go), after
//     a grant preflight (flux_delivery_grants.go). A direct apply is refused
//     (runDeploy): the declaration must never quietly fall back to forge
//     applying the objects its Flux owns;
//   - signing is at most `audit` (the render resolves it, and forge says so):
//     nothing signs a self-hosted env's bundles yet, so `enforce` would admit
//     no release at all.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/reliant-labs/forge/internal/bundle"
	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

// FluxDeliveryEntity is the resolved self-hosted delivery of an
// `applier = "flux"` env, as kcl/render.k's _render_flux_delivery projects it.
type FluxDeliveryEntity struct {
	// Chart is the bootstrap platform chart's name (fluxdelivery.ChartName).
	Chart string `json:"chart"`
	// Env and Cluster are the env and the one kubectl context it targets:
	// where its Flux runs and every tree applies.
	Env     string `json:"env"`
	Cluster string `json:"cluster"`
	// Namespace is the env's delivery namespace; ResourceSet the delivery
	// ResourceSet's name in it.
	Namespace   string `json:"namespace"`
	ResourceSet string `json:"resourceset"`
	// ApplyIdentities: every tree applies as a derived identity. False:
	// kustomize-controller applies as itself and no identity is derived.
	ApplyIdentities bool `json:"apply_identities"`
	// DeliveryScope is "namespace" or "cluster": where the rollout/post
	// identity may write.
	DeliveryScope string `json:"delivery_scope"`
	// PullSecretFrom is `<namespace>/<name>` of the image-pull Secret the
	// bootstrap copies into Namespace as PullSecret; both empty when the
	// registry needs no credential.
	PullSecretFrom string `json:"pull_secret_from"`
	PullSecret     string `json:"pull_secret"`
	// Signing and FluxVerify are delivery_policy's controls as asked and as
	// they take effect here (at most audit).
	Signing    FluxDeliveryControl `json:"signing"`
	FluxVerify FluxDeliveryControl `json:"flux_verify"`
	// Registry is FluxDelivery.registry as declared: the registry subtree the
	// env's bundles are pushed to, as this machine reaches it. Empty: the
	// registry of the env's first cluster workload image (fluxRegistryBase).
	Registry string `json:"registry"`

	// releaseObjects are a release bundle's objects whose grants a deploy's
	// re-grant adds to the render's (flux_delivery_deploy.go), so re-running
	// the bootstrap for a release covers what that release applies even
	// when this checkout's render no longer matches it. Nil for a bootstrap
	// run on its own.
	releaseObjects []bundle.RenderedObject
}

// FluxDeliveryControl is one delivery-policy control on a self-hosted
// delivery: the mode the env asks for and the mode in effect.
type FluxDeliveryControl struct {
	Requested string `json:"requested"`
	Effective string `json:"effective"`
}

// attachFluxDelivery hands the env's resolved delivery to the bootstrap chart
// it belongs to, so the chart's install (helmChartSpecsFromEntities) derives
// the grants without being told about the env separately.
func attachFluxDelivery(e *KCLEntities) {
	if e == nil || e.FluxDelivery == nil {
		return
	}
	for i := range e.HelmCharts {
		if e.HelmCharts[i].Name == e.FluxDelivery.Chart {
			e.HelmCharts[i].FluxDelivery = e.FluxDelivery
		}
	}
}

// refuseFluxDeliveryApply refuses a direct apply of an `applier = "flux"` env:
// that env's Flux applies its objects, and forge only points it at a release.
func refuseFluxDeliveryApply(env string, e *KCLEntities) error {
	if e == nil || e.Applier != applierFlux {
		return nil
	}
	return fmt.Errorf("env %q declares applier = \"flux\": the Flux forge installs in its cluster applies its bundle, "+
		"and forge never applies it directly.\n"+
		"  Ship a release (record it, push its bundle, point the Flux at it):\n"+
		"    forge env deploy %s [<version>]\n"+
		"  Bootstrap that Flux first if you have not (no release is recorded):\n"+
		"    forge env deploy %s --target %s", env, env, env, fluxdelivery.ChartName)
}

// fluxDeliveryBootstrapRequested reports whether a deploy names the bootstrap
// chart and nothing that would cut, ship or preview a release. Decided from the
// flags alone, so no other deploy pays for a render to ask.
func fluxDeliveryBootstrapRequested(f deployCmdFlags) bool {
	if f.promote.requestedRelease() || f.explain || f.dryRun || f.frontendsOnly || f.offlineLedger || f.claimable {
		return false
	}
	for _, t := range f.targets {
		if t == fluxdelivery.ChartName {
			return true
		}
	}
	return false
}

// installFluxDeliveryChartsFn installs the bootstrap. A var so the dispatch is
// testable without helm or a cluster.
var installFluxDeliveryChartsFn = installPlatformCharts

// dispatchFluxDeliveryBootstrap is `forge env deploy <env> --target
// flux-delivery`: install the env's bootstrap charts and nothing else. handled
// is false when the env is not an `applier = "flux"` env, so the caller goes on
// exactly as it would have (a scoped no-version deploy is refused there).
func dispatchFluxDeliveryBootstrap(ctx context.Context, envName string, f deployCmdFlags, out io.Writer) (handled bool, err error) {
	entities, err := RenderKCL(ctx, projectDirForKCL(), envName)
	if err != nil {
		return true, err
	}
	if entities.Applier != applierFlux || entities.FluxDelivery == nil {
		return false, nil
	}
	charts, rest := splitPlatformChartTargets(entities, f.targets)
	if len(rest) > 0 {
		return true, fmt.Errorf("env %q declares applier = \"flux\": --target %s names what its in-cluster Flux applies, "+
			"not a bootstrap chart. The bootstrap installs platform charts only (declared: %s)",
			envName, strings.Join(rest, ", "), strings.Join(platformChartNames(entities), ", "))
	}
	installs := platformChartInstalls(entities, charts, nil)
	if len(installs) == 0 {
		return true, fmt.Errorf("env %q: the bootstrap chart %s resolves to no cluster", envName, fluxdelivery.ChartName)
	}
	writeFluxDeliverySigningNotice(out, envName, entities.FluxDelivery)
	project := ""
	if store, serr := loadProjectStore(); serr == nil {
		project = store.Meta().Name
	}
	fmt.Fprintf(out, "Bootstrapping the in-cluster Flux of env %s (delivery namespace %s)...\n", envName, entities.FluxDelivery.Namespace)
	if err := installFluxDeliveryChartsFn(ctx, entities, installs, cluster.ChartOwner(project, envName)); err != nil {
		return true, err
	}
	fmt.Fprintf(out, "Env %s's Flux is installed and its delivery ResourceSet %s/%s is waiting for its first release.\n",
		envName, entities.FluxDelivery.Namespace, entities.FluxDelivery.ResourceSet)
	return true, nil
}

func platformChartNames(e *KCLEntities) []string {
	out := make([]string, 0, len(e.HelmCharts))
	for _, c := range e.HelmCharts {
		out = append(out, c.Name)
	}
	return out
}

// fluxDeliverySigningNotice is the one line that says a self-hosted env's
// signing controls are capped, or "" when nothing was capped.
func fluxDeliverySigningNotice(env string, fd *FluxDeliveryEntity) string {
	if fd == nil {
		return ""
	}
	var capped []string
	for _, c := range []struct {
		name string
		ctl  FluxDeliveryControl
	}{{"signing", fd.Signing}, {"flux_verify", fd.FluxVerify}} {
		if c.ctl.Requested != c.ctl.Effective {
			capped = append(capped, fmt.Sprintf("%s %s -> %s", c.name, c.ctl.Requested, c.ctl.Effective))
		}
	}
	if len(capped) == 0 {
		return ""
	}
	return fmt.Sprintf("Note: env %s has no control plane to sign its bundles, so its delivery_policy is capped at audit "+
		"(%s): forge records what the policy asks and blocks nothing on a signature yet", env, strings.Join(capped, ", "))
}

func writeFluxDeliverySigningNotice(w io.Writer, env string, fd *FluxDeliveryEntity) {
	if line := fluxDeliverySigningNotice(env, fd); line != "" {
		fmt.Fprintln(w, line)
	}
}

// fluxDeliveryChartExtras is what the bootstrap chart's install adds to the
// chart KCL rendered: the derived identity grants, riding the chart (so the
// chart's prune removes a grant the render stops needing), and the pull Secret
// copy. A var so a test states the grants without rendering an env.
var fluxDeliveryChartExtras = func(ctx context.Context, fd FluxDeliveryEntity) (string, []cluster.SecretCopy, error) {
	var copies []cluster.SecretCopy
	if fd.PullSecretFrom != "" {
		ns, name, _ := strings.Cut(fd.PullSecretFrom, "/")
		copies = append(copies, cluster.SecretCopy{
			FromNamespace: ns, FromName: name,
			ToNamespace: fd.Namespace, ToName: fd.PullSecret,
			Labels: fluxDeliveryLabels(fd.Env),
		})
	}
	if !fd.ApplyIdentities {
		return "", copies, nil
	}
	// The SAME projection the bundle is built from, so the grant and the
	// bundle cannot disagree about what a tree applies.
	doc, err := projectEnvShapeFn(ctx, io.Discard, fd.Env)
	if err != nil {
		return "", nil, fmt.Errorf("derive env %q's apply identities from its render: %w", fd.Env, err)
	}
	ids, err := fluxDeliveryIdentityManifests(doc.manifests, fd)
	if err != nil {
		return "", nil, fmt.Errorf("derive env %q's apply identities: %w", fd.Env, err)
	}
	return ids, copies, nil
}
