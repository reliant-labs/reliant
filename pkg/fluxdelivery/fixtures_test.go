package fluxdelivery_test

import (
	"github.com/reliant-labs/forge/pkg/flux"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

// The fixture inputs every render test runs over. Each is a function so a
// test may mutate its copy.
//
// The first three are the inputs that reproduce control-plane's
// fluxtenant.BuildBundle goldens (bundle_flat_connected, bundle_flat_hosted,
// bundle_trees_v2, frozen in testdata/fluxtenant/): the adoption-parity
// evidence of typed_parity_test.go.

const (
	fxOrg    = "acme"
	fxEnv    = "env-prod"
	fxNS     = "flux-acme"
	fxDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

type fixture struct {
	name string
	in   func() fluxdelivery.BindingInput
	// validates is false for a fixture Validate refuses by design (the
	// binding fixture: rendered through Render alone).
	validates bool
}

func fixtures() []fixture {
	return []fixture{
		{"flat_connected", flatConnected, true},
		{"flat_hosted", flatHosted, true},
		{"trees_v2", treesV2, true},
		{"verify_enforce", verifyEnforce, true},
		{"verify_keyless", verifyKeyless, true},
		{"retire_orphan_applier", retireOrphanApplier, true},
		{"retire_orphan", retireOrphan, true},
		{"retire_teardown", retireTeardown, true},
		{"suspended_adoption", suspendedAdoption, true},
		{"binding_web", bindingWeb, false},
	}
}

// base is the binding every fixture starts from: the hub's labels, an
// in-cluster registry over a pull Secret, no verification.
func base(binding string) fluxdelivery.BindingInput {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "reliant-control-plane",
		"reliant.dev/org":              fxOrg,
		"reliant.dev/environment":      fxEnv,
	}
	if binding != "" {
		labels["reliant.dev/binding"] = binding
	}
	return fluxdelivery.BindingInput{
		Schema:    fluxdelivery.Schema,
		Org:       fxOrg,
		Env:       fxEnv,
		Binding:   binding,
		Namespace: fxNS,
		Fence:     "42",
		Promotion: "promo-1",
		Labels:    labels,
		Source: fluxdelivery.Source{
			Name:       fluxdelivery.SourceName(fxOrg, fxEnv, binding),
			URL:        "oci://registry.example.com/orgs/acme/bundle.v1/prod",
			Digest:     fxDigest,
			Interval:   "5m0s",
			PullSecret: "registry-pull",
			Verify:     fluxdelivery.Verify{Mode: fluxdelivery.VerifyOff},
		},
	}
}

// flatApplier is a layout-v1 cluster reached with a kubeconfig, as
// fluxtenant's buildFlatKustomization renders it.
func flatApplier(in fluxdelivery.BindingInput, cluster, kubeconfig string) fluxdelivery.Applier {
	return fluxdelivery.Applier{
		Name:             fluxdelivery.KustomizationName(in.Org, in.Env, in.Binding, cluster),
		Source:           in.Source.Name,
		Cluster:          cluster,
		Path:             release.BundleClusterPath(cluster),
		Labels:           map[string]string{"reliant.dev/cluster": fluxdelivery.LabelValue(cluster)},
		KubeConfigSecret: kubeconfig,
		Wait:             true,
		Timeout:          "5m0s",
	}
}

// treeApplier is one layout-v2 tree, as fluxtenant's buildTreeKustomization
// renders it.
func treeApplier(in fluxdelivery.BindingInput, cluster, unit string, phase release.Phase, kubeconfig string, deps []string, health ...fluxdelivery.HealthCheck) fluxdelivery.Applier {
	return fluxdelivery.Applier{
		Name:    fluxdelivery.TreeKustomizationName(in.Org, in.Env, in.Binding, cluster, unit, phase),
		Source:  in.Source.Name,
		Cluster: cluster,
		Unit:    unit,
		Phase:   phase,
		Path:    release.BundleTreePath(cluster, unit, phase),
		Labels: map[string]string{
			"reliant.dev/cluster":       fluxdelivery.LabelValue(cluster),
			"reliant.dev/delivery-unit": fluxdelivery.LabelValue(unit),
			"reliant.dev/phase":         string(phase),
		},
		KubeConfigSecret: kubeconfig,
		DependsOn:        deps,
		Health:           health,
		Timeout:          "1m30s",
		RetryInterval:    "15s",
	}
}

func health(kind, name, namespace string) fluxdelivery.HealthCheck {
	apiVersion, _ := fluxdelivery.HealthAPIVersion(kind)
	return fluxdelivery.HealthCheck{APIVersion: apiVersion, Kind: kind, Name: name, Namespace: namespace}
}

func flatConnected() fluxdelivery.BindingInput {
	in := base("")
	in.Appliers = []fluxdelivery.Applier{
		flatApplier(in, "gke_reliant-labs_us-central1_prod", "cluster-a-kubeconfig"),
		flatApplier(in, "vke-staging", "cluster-b-kubeconfig"),
	}
	return in
}

func flatHosted() fluxdelivery.BindingInput {
	in := base("")
	a := flatApplier(in, release.BundleHostedCluster, "")
	a.InCluster = true
	a.ServiceAccountName = flux.DeployServiceAccount
	a.TargetNamespace = "env-prod"
	a.CommonLabels = map[string]string{"reliant.dev/org": "acme", "reliant.dev/environment": "env-prod"}
	a.Patches = []fluxdelivery.Patch{{
		Patch:  "metadata:\n  labels:\n    forge.dev/deployment-id: d-1\n",
		Target: fluxdelivery.PatchTarget{Group: "forge.dev", Version: "v1alpha1", Kind: "Workload", Name: "api"},
	}}
	in.Appliers = []fluxdelivery.Applier{a}
	return in
}

func treesV2() fluxdelivery.BindingInput {
	in := base("")
	name := func(cluster, unit string, phase release.Phase) string {
		return fluxdelivery.TreeKustomizationName(in.Org, in.Env, in.Binding, cluster, unit, phase)
	}
	in.Appliers = []fluxdelivery.Applier{
		treeApplier(in, "daemon", "daemon-substrate", release.PhasePre, "daemon-kubeconfig", nil),
		treeApplier(in, "daemon", "daemon-substrate", release.PhaseRollout, "daemon-kubeconfig",
			[]string{name("daemon", "daemon-substrate", release.PhasePre)}, health("Deployment", "operator", "kata")),
		treeApplier(in, "hub", "base", release.PhasePost, "hub-kubeconfig",
			[]string{name("hub", "base", release.PhaseRollout)}, health("Job", "seed-def456", "app")),
		treeApplier(in, "hub", "base", release.PhasePre, "hub-kubeconfig",
			[]string{name("daemon", "daemon-substrate", release.PhaseRollout)}, health("Job", "migrate-abc123", "app")),
		treeApplier(in, "hub", "base", release.PhaseRollout, "hub-kubeconfig",
			[]string{name("hub", "base", release.PhasePre)}, health("Deployment", "web", "app")),
	}
	return in
}

func verifyEnforce() fluxdelivery.BindingInput {
	in := flatConnected()
	in.Source.Verify = fluxdelivery.Verify{Mode: fluxdelivery.VerifyEnforce, Provider: "cosign", SecretRef: "delivery-signing-keys"}
	return in
}

func verifyKeyless() fluxdelivery.BindingInput {
	in := flatConnected()
	in.Source.Verify = fluxdelivery.Verify{Mode: fluxdelivery.VerifyEnforce, Provider: "cosign", OIDC: []fluxdelivery.OIDCIdentity{{
		Issuer:  `^https://token\.actions\.githubusercontent\.com$`,
		Subject: `^https://github\.com/acme/app/\.github/workflows/release\.yml@refs/heads/main$`,
	}}}
	return in
}

// retireOrphanApplier is the first of the two writes that drop an applier:
// one tree retired, the rest untouched, the digest unchanged.
func retireOrphanApplier() fluxdelivery.BindingInput {
	in := treesV2()
	in.Appliers[2].Retire = fluxdelivery.RetireOrphan
	return in
}

// retireOrphan retires the whole binding (forge takes over applying it).
func retireOrphan() fluxdelivery.BindingInput {
	in := flatConnected()
	in.Retire = fluxdelivery.RetireOrphan
	return in
}

// retireTeardown is a ledger-recorded environment deletion: the one render in
// which a delete may reach a workload.
func retireTeardown() fluxdelivery.BindingInput {
	in := treesV2()
	in.Retire = fluxdelivery.RetireTeardown
	in.Appliers[1].Suspended = true
	return in
}

// suspendedAdoption carries a live suspend into the input, with an
// ExternalSecret health check and its expression, a cross-binding dependency
// and an insecure in-cluster registry with Flux's generic provider.
func suspendedAdoption() fluxdelivery.BindingInput {
	in := treesV2()
	in.Source.Insecure = true
	in.Source.Provider = fluxdelivery.ProviderGeneric
	in.Appliers[0].Suspended = true
	in.Appliers[0].Health = []fluxdelivery.HealthCheck{health("ExternalSecret", "db-credentials", "app")}
	in.Appliers[0].HealthExprs = []fluxdelivery.HealthCheckExpr{{
		APIVersion: "external-secrets.io/v1", Kind: "ExternalSecret", Current: fluxdelivery.ExternalSecretCurrent,
	}}
	in.Appliers[0].ExternalDependsOn = []string{"acme.env.foundation-hub-base-post"}
	return in
}

// bindingWeb is the future per-binding shape, rendered through Render alone
// (Validate refuses a non-empty binding today): its golden proves the names
// and labels that path will produce.
func bindingWeb() fluxdelivery.BindingInput {
	in := base("web")
	in.Appliers = []fluxdelivery.Applier{flatApplier(in, "vke-staging", "cluster-b-kubeconfig")}
	return in
}
