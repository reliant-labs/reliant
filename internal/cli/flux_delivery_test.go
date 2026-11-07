package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
)

// fluxEnv is an `applier = "flux"` env as the render projects it: one cluster
// workload, the bootstrap chart, and the resolved delivery.
func fluxEnv() *KCLEntities {
	fd := &FluxDeliveryEntity{
		Chart: fluxdelivery.ChartName, Env: "prod", Cluster: "gke_p_prod",
		Namespace: "forge-delivery-p-prod", ResourceSet: "delivery.prod",
		ApplyIdentities: true, DeliveryScope: "namespace",
		Signing:    FluxDeliveryControl{Requested: "enforce", Effective: "audit"},
		FluxVerify: FluxDeliveryControl{Requested: "enforce", Effective: "audit"},
	}
	e := &KCLEntities{
		Applier:       applierFlux,
		FluxDelivery:  fd,
		ClusterTarget: &ClusterTargetEntity{Cluster: "gke_p_prod", Namespace: "prod"},
		Workloads: []WorkloadEntity{{
			Name:    "api",
			Runtime: RuntimeEntity{Type: RuntimeCluster, Cluster: &ClusterRuntime{Cluster: "gke_p_prod"}},
		}},
		HelmCharts: []HelmChartEntity{
			{Name: fluxdelivery.ChartName, OCI: "oci://ghcr.io/controlplaneio-fluxcd/charts/flux-operator", Version: "0.62.1", Namespace: "flux-system"},
			{Name: "cert-manager", Version: "1", Namespace: "cert-manager"},
		},
	}
	attachFluxDelivery(e)
	return e
}

// An `applier = "flux"` env is never direct-applied: its Flux applies it.
func TestDirectApplyAllowed_FluxApplier(t *testing.T) {
	t.Parallel()
	if DirectApplyAllowed(fluxEnv()) {
		t.Fatal("an applier = flux env was allowed a direct apply")
	}
	if err := refuseFluxDeliveryApply("prod", fluxEnv()); err == nil || !strings.Contains(err.Error(), "--target "+fluxdelivery.ChartName) {
		t.Errorf("refusal = %v, want it to name the bootstrap", err)
	}
	if err := refuseFluxDeliveryApply("prod", boundOnCluster("forge")); err != nil {
		t.Errorf("applier = forge refused: %v", err)
	}
	if err := refuseFluxDeliveryApply("prod", nil); err != nil {
		t.Errorf("no render refused: %v", err)
	}
	if got := shapeApplierOf(fluxEnv()); got != "flux" {
		t.Errorf("shape applier = %q", got)
	}
}

// The whole bundle of a flux env is reconciled and pruned, so the build holds
// it to Flux safety like an undeclared env, unlike applier = forge.
func TestEnvShapeDoc_FluxIsReconciled(t *testing.T) {
	t.Parallel()
	var doc envShapeDoc
	doc.Shape.Applier = "flux"
	if !doc.reconciled() {
		t.Error("an applier = flux env is not reconciled; its bundle would skip the Flux safety check")
	}
	doc.Shape.Applier = "forge"
	if doc.reconciled() {
		t.Error("control: applier = forge is reconciled")
	}
}

func TestParseKCLEntities_FluxDeliveryAttachesToItsChart(t *testing.T) {
	t.Parallel()
	raw := map[string]any{"output": map[string]any{
		"applier": "flux",
		"flux_delivery": map[string]any{
			"chart": "flux-delivery", "env": "prod", "cluster": "k3d-p", "namespace": "forge-delivery-p-prod",
			"resourceset": "delivery.prod", "apply_identities": true, "delivery_scope": "namespace",
			"pull_secret_from": "prod/pull", "pull_secret": "forge-delivery-pull",
			"signing":     map[string]any{"requested": "enforce", "effective": "audit"},
			"flux_verify": map[string]any{"requested": "off", "effective": "off"},
		},
		"helm_charts": []any{
			map[string]any{"name": "flux-delivery", "version": "0.62.1", "namespace": "flux-system"},
			map[string]any{"name": "other", "version": "1", "namespace": "other"},
		},
		"workloads": []any{},
	}}
	b, _ := json.Marshal(raw)
	e, err := parseKCLEntities(b)
	if err != nil {
		t.Fatal(err)
	}
	if e.FluxDelivery == nil || e.FluxDelivery.PullSecretFrom != "prod/pull" || e.FluxDelivery.Signing.Effective != "audit" {
		t.Fatalf("flux_delivery = %+v", e.FluxDelivery)
	}
	if e.HelmCharts[0].FluxDelivery != e.FluxDelivery || e.HelmCharts[1].FluxDelivery != nil {
		t.Errorf("the delivery attached to %+v / %+v; want the bootstrap chart alone", e.HelmCharts[0].FluxDelivery, e.HelmCharts[1].FluxDelivery)
	}
}

func TestFluxDeliveryBootstrapRequested(t *testing.T) {
	t.Parallel()
	boot := deployCmdFlags{targets: []string{fluxdelivery.ChartName}}
	if !fluxDeliveryBootstrapRequested(boot) {
		t.Fatal("--target flux-delivery is not the bootstrap")
	}
	for name, f := range map[string]deployCmdFlags{
		"no target":    {},
		"another":      {targets: []string{"api"}},
		"with version": {targets: boot.targets, promote: promoteCmdFlags{version: "v1"}},
		"dry run":      {targets: boot.targets, dryRun: true},
	} {
		if fluxDeliveryBootstrapRequested(f) {
			t.Errorf("%s: treated as the bootstrap", name)
		}
	}
}

func TestFluxDeliverySigningNotice(t *testing.T) {
	t.Parallel()
	line := fluxDeliverySigningNotice("prod", fluxEnv().FluxDelivery)
	for _, want := range []string{"no control plane", "signing enforce -> audit", "flux_verify enforce -> audit"} {
		if !strings.Contains(line, want) {
			t.Errorf("notice %q lacks %q", line, want)
		}
	}
	kept := *fluxEnv().FluxDelivery
	kept.Signing = FluxDeliveryControl{Requested: "audit", Effective: "audit"}
	kept.FluxVerify = FluxDeliveryControl{Requested: "off", Effective: "off"}
	if line := fluxDeliverySigningNotice("prod", &kept); line != "" {
		t.Errorf("nothing was capped, notice = %q", line)
	}
	if fluxDeliverySigningNotice("prod", nil) != "" {
		t.Error("a non-flux env printed a notice")
	}
}

// The bootstrap chart's spec carries the derived grants and the pull Secret
// copy; no other chart is touched.
func TestHelmChartSpecs_FluxDeliveryExtras(t *testing.T) {
	prev := fluxDeliveryChartExtras
	t.Cleanup(func() { fluxDeliveryChartExtras = prev })
	var asked []string
	fluxDeliveryChartExtras = func(_ context.Context, fd FluxDeliveryEntity) (string, []cluster.SecretCopy, error) {
		asked = append(asked, fd.Env)
		return "apiVersion: rbac.authorization.k8s.io/v1\nkind: ClusterRole\nmetadata:\n  name: derived\n",
			[]cluster.SecretCopy{{FromNamespace: "prod", FromName: "pull", ToNamespace: fd.Namespace, ToName: "forge-delivery-pull"}}, nil
	}
	e := fluxEnv()
	e.HelmCharts[0].Manifests = []any{map[string]any{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": map[string]any{"name": "declared"}}}
	e.HelmCharts[1].OCI = "oci://example/cm"
	specs, err := helmChartSpecsFromEntities(context.Background(), e.HelmCharts, e.Clusters, "p/prod")
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "prod" {
		t.Fatalf("extras derived for %v, want the bootstrap chart once", asked)
	}
	boot := specs[0]
	if !strings.Contains(boot.Manifests, "name: declared") || !strings.Contains(boot.Manifests, "name: derived") {
		t.Errorf("bootstrap manifests lack the declared or the derived objects:\n%s", boot.Manifests)
	}
	if len(boot.SecretCopies) != 1 || boot.SecretCopies[0].ToNamespace != "forge-delivery-p-prod" {
		t.Errorf("secret copies = %+v", boot.SecretCopies)
	}
	if specs[1].SecretCopies != nil || strings.Contains(specs[1].Manifests, "derived") {
		t.Errorf("another chart carried the delivery's extras: %+v", specs[1])
	}
}

// The default extras: the copy is always computed, and an identity-free
// delivery derives no grant (no render needed).
func TestFluxDeliveryChartExtras_NoIdentities(t *testing.T) {
	t.Parallel()
	fd := *fluxEnv().FluxDelivery
	fd.ApplyIdentities = false
	fd.PullSecretFrom, fd.PullSecret = "prod/ghcr-pull", fluxdelivery.SelfHostedPullSecret
	ids, copies, err := fluxDeliveryChartExtras(context.Background(), fd)
	if err != nil || ids != "" {
		t.Fatalf("no identities: ids %q err %v", ids, err)
	}
	want := cluster.SecretCopy{FromNamespace: "prod", FromName: "ghcr-pull", ToNamespace: fd.Namespace, ToName: fluxdelivery.SelfHostedPullSecret, Labels: fluxDeliveryLabels("prod")}
	if len(copies) != 1 || copies[0].String() != want.String() || copies[0].Labels[fluxdelivery.EnvLabel] != "prod" {
		t.Errorf("copies = %+v, want %+v", copies, want)
	}
}

// `--target flux-delivery` on a flux env installs its bootstrap charts and
// records nothing; on any other env it is not the bootstrap.
func TestDispatchFluxDeliveryBootstrap(t *testing.T) {
	prevRender, prevInstall := renderKCLRawHook, installFluxDeliveryChartsFn
	t.Cleanup(func() { renderKCLRawHook, installFluxDeliveryChartsFn = prevRender, prevInstall })
	render := func(applier string) {
		renderKCLRawHook = func(context.Context, string, string, ...string) ([]byte, error) {
			out := map[string]any{"workloads": []any{}, "helm_charts": []any{
				map[string]any{"name": "flux-delivery", "version": "0.62.1", "namespace": "flux-system"},
				map[string]any{"name": "cert-manager", "version": "1", "namespace": "cert-manager"},
			}, "cluster_target": map[string]any{"cluster": "gke_p_prod", "namespace": "prod"}}
			if applier != "" {
				out["applier"] = applier
			}
			if applier == "flux" {
				out["flux_delivery"] = map[string]any{"chart": "flux-delivery", "env": "prod", "cluster": "gke_p_prod",
					"namespace": "forge-delivery-p-prod", "resourceset": "delivery.prod", "apply_identities": true,
					"signing":     map[string]any{"requested": "enforce", "effective": "audit"},
					"flux_verify": map[string]any{"requested": "enforce", "effective": "audit"}}
			}
			return json.Marshal(map[string]any{"output": out})
		}
	}
	var installed []string
	installFluxDeliveryChartsFn = func(_ context.Context, _ *KCLEntities, ins []platformChartInstall, owner string) error {
		for _, in := range ins {
			installed = append(installed, in.Chart.Name+"@"+in.Context)
		}
		return nil
	}

	render("flux")
	var out bytes.Buffer
	handled, err := dispatchFluxDeliveryBootstrap(context.Background(), "prod", deployCmdFlags{targets: []string{"flux-delivery"}}, &out)
	if !handled || err != nil {
		t.Fatalf("handled %t err %v", handled, err)
	}
	if len(installed) != 1 || installed[0] != "flux-delivery@gke_p_prod" {
		t.Errorf("installed %v", installed)
	}
	if !strings.Contains(out.String(), "capped at audit") || !strings.Contains(out.String(), "delivery.prod") {
		t.Errorf("output lacks the signing notice or the ResourceSet:\n%s", out.String())
	}

	installed = nil
	if handled, err := dispatchFluxDeliveryBootstrap(context.Background(), "prod", deployCmdFlags{targets: []string{"flux-delivery", "api"}}, &out); !handled || err == nil || len(installed) != 0 {
		t.Errorf("a workload target beside the bootstrap: handled %t err %v installed %v", handled, err, installed)
	}

	render("")
	if handled, err := dispatchFluxDeliveryBootstrap(context.Background(), "prod", deployCmdFlags{targets: []string{"flux-delivery"}}, &out); handled || err != nil {
		t.Errorf("a non-flux env was handled as a bootstrap: handled %t err %v", handled, err)
	}
}
