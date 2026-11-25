package fluxdelivery_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

// selfHosted is the binding a self-hosted writer computes for a one-cluster
// env: its own delivery namespace, forge's labels, every tree in-cluster, and
// (with identities) each tree under its phase's derived identity.
func selfHosted(identities bool) fluxdelivery.BindingInput {
	const project, env, cluster = "acme", "prod", "gke_acme_prod"
	in := fluxdelivery.BindingInput{
		Schema:    fluxdelivery.Schema,
		Org:       project,
		Env:       env,
		Namespace: fluxdelivery.SelfHostedNamespace(project, env),
		Fence:     "7",
		Promotion: "promo-7",
		Labels: map[string]string{
			"app.kubernetes.io/managed-by": "forge",
			fluxdelivery.EnvLabel:          env,
		},
		Source: fluxdelivery.Source{
			Name:       fluxdelivery.SourceName(project, env, ""),
			URL:        "oci://ghcr.io/acme/acme/bundle.v1/prod",
			Digest:     fxDigest,
			Interval:   "10m0s",
			PullSecret: fluxdelivery.SelfHostedPullSecret,
			Verify:     fluxdelivery.Verify{Mode: fluxdelivery.VerifyOff},
		},
	}
	tree := func(phase release.Phase, deps []string, h ...fluxdelivery.HealthCheck) fluxdelivery.Applier {
		a := treeApplier(in, cluster, release.DefaultDeliveryUnit, phase, "", deps, h...)
		a.InCluster = true
		if identities {
			a.ServiceAccountName = fluxdelivery.SelfHostedServiceAccount(phase)
		}
		return a
	}
	name := func(phase release.Phase) string {
		return fluxdelivery.TreeKustomizationName(project, env, "", cluster, release.DefaultDeliveryUnit, phase)
	}
	in.Appliers = []fluxdelivery.Applier{
		tree(release.PhasePre, nil, health("Job", "migrate-abc123", "acme-prod")),
		tree(release.PhaseRollout, []string{name(release.PhasePre)}, health("Deployment", "api", "acme-prod")),
		tree(release.PhasePost, []string{name(release.PhaseRollout)}),
	}
	return in
}

// TestSelfHostedConstantsAgreeWithKCL pins the names the bootstrap (KCL) and
// a writer (Go) must spell alike: a Kustomization naming an identity the
// bootstrap never created is Forbidden on the first tree.
func TestSelfHostedConstantsAgreeWithKCL(t *testing.T) {
	t.Parallel()
	src := kclLib(t)
	for kcl, goVal := range map[string]string{
		"FLUX_DELIVERY_CHART_NAME":  fluxdelivery.ChartName,
		"FLUX_DELIVERY_PRE_SA":      fluxdelivery.PreServiceAccount,
		"FLUX_DELIVERY_APPLY_SA":    fluxdelivery.ApplyServiceAccount,
		"FLUX_DELIVERY_NONE_SA":     fluxdelivery.NoneServiceAccount,
		"FLUX_DELIVERY_PULL_SECRET": fluxdelivery.SelfHostedPullSecret,
		"FLUX_DELIVERY_ENV_LABEL":   fluxdelivery.EnvLabel,
	} {
		if want := kcl + ` = "` + goVal + `"`; !strings.Contains(src, want) {
			t.Errorf("kcl/lib/flux_delivery.k does not declare %s", want)
		}
	}
	// The namespace and ResourceSet compositions, spelled once per language.
	for _, want := range []string{`"forge-delivery-${project}-${env}"`, `"delivery.${env}"`} {
		if !strings.Contains(src, want) {
			t.Errorf("kcl/lib/flux_delivery.k no longer composes %s; update fluxdelivery.SelfHostedNamespace / ResourceSetName with it", want)
		}
	}
	if got := fluxdelivery.SelfHostedNamespace("acme", "prod"); got != "forge-delivery-acme-prod" {
		t.Errorf("SelfHostedNamespace = %q", got)
	}
	if got := fluxdelivery.ResourceSetName("prod", ""); got != "delivery.prod" {
		t.Errorf("ResourceSetName = %q, want the name the bootstrap gives the ResourceSet", got)
	}
}

func TestSelfHostedServiceAccount(t *testing.T) {
	t.Parallel()
	for phase, want := range map[release.Phase]string{
		release.PhasePre:     fluxdelivery.PreServiceAccount,
		release.PhaseRollout: fluxdelivery.ApplyServiceAccount,
		release.PhasePost:    fluxdelivery.ApplyServiceAccount,
	} {
		if got := fluxdelivery.SelfHostedServiceAccount(phase); got != want {
			t.Errorf("%s tree applies as %q, want %q", phase, got, want)
		}
	}
}

// TestValidateSelfHosted: the self-hosted rules, and that the hub's Validate
// still refuses what only a self-hosted Flux may do.
func TestValidateSelfHosted(t *testing.T) {
	t.Parallel()
	multi := fluxdelivery.SelfHosted{ApplyIdentities: true}
	single := fluxdelivery.SelfHosted{}
	cases := []struct {
		name   string
		in     func() fluxdelivery.BindingInput
		mode   fluxdelivery.SelfHosted
		wantIs error
	}{
		{"identities", func() fluxdelivery.BindingInput { return selfHosted(true) }, multi, nil},
		{"no identities", func() fluxdelivery.BindingInput { return selfHosted(false) }, single, nil},
		{"pre tree under the apply identity", func() fluxdelivery.BindingInput {
			in := selfHosted(true)
			in.Appliers[0].ServiceAccountName = fluxdelivery.ApplyServiceAccount
			return in
		}, multi, fluxdelivery.ErrNoClusterCredential},
		{"tree under the empty identity", func() fluxdelivery.BindingInput {
			in := selfHosted(true)
			in.Appliers[1].ServiceAccountName = fluxdelivery.NoneServiceAccount
			return in
		}, multi, fluxdelivery.ErrNoClusterCredential},
		{"identity tree naming none", func() fluxdelivery.BindingInput { return selfHosted(false) }, multi, fluxdelivery.ErrNoClusterCredential},
		{"identity-free tree naming one", func() fluxdelivery.BindingInput { return selfHosted(true) }, single, fluxdelivery.ErrNoClusterCredential},
		{"a kubeconfig", func() fluxdelivery.BindingInput {
			in := selfHosted(true)
			in.Appliers[2].InCluster = false
			in.Appliers[2].ServiceAccountName = ""
			in.Appliers[2].KubeConfigSecret = "other-cluster"
			return in
		}, multi, fluxdelivery.ErrNoClusterCredential},
		{"a flat cluster", func() fluxdelivery.BindingInput {
			in := selfHosted(true)
			a := flatApplier(in, "gke_acme_prod", "")
			a.InCluster, a.ServiceAccountName = true, fluxdelivery.ApplyServiceAccount
			in.Appliers = []fluxdelivery.Applier{a}
			return in
		}, multi, fluxdelivery.ErrInvalidTree},
		{"structural refusals still hold", func() fluxdelivery.BindingInput {
			in := selfHosted(true)
			in.Appliers[1].Path = "manifests/elsewhere"
			return in
		}, multi, fluxdelivery.ErrPathMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := fluxdelivery.ValidateSelfHosted(tc.in(), tc.mode)
			switch {
			case tc.wantIs == nil && err != nil:
				t.Fatalf("ValidateSelfHosted: %v", err)
			case tc.wantIs != nil && !errors.Is(err, tc.wantIs):
				t.Fatalf("ValidateSelfHosted = %v, want %v", err, tc.wantIs)
			}
		})
	}
	// An in-cluster tree is a breach on the hub, where every tree applies
	// through a kubeconfig.
	if err := fluxdelivery.Validate(selfHosted(true)); !errors.Is(err, fluxdelivery.ErrNoClusterCredential) {
		t.Errorf("hub Validate of a self-hosted input = %v, want ErrNoClusterCredential", err)
	}
}

// TestSelfHostedRenderGoldens: the self-hosted renders, held to their goldens
// and to the self-hosted safety check. The hub's check refuses the
// identity-free render: there, an apply with no identity is a breach.
func TestSelfHostedRenderGoldens(t *testing.T) {
	t.Parallel()
	for name, mode := range map[string]fluxdelivery.SelfHosted{
		"self_hosted_identities":    {ApplyIdentities: true},
		"self_hosted_no_identities": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := selfHosted(mode.ApplyIdentities)
			objs := render(t, in)
			if err := fluxdelivery.CheckRenderedSelfHosted(in, objs, mode); err != nil {
				t.Fatalf("CheckRenderedSelfHosted: %v", err)
			}
			hubErr := fluxdelivery.CheckRendered(in, objs)
			if mode.ApplyIdentities && hubErr != nil {
				t.Errorf("CheckRendered of a self-hosted render with identities: %v", hubErr)
			}
			if !mode.ApplyIdentities && !errors.Is(hubErr, fluxdelivery.ErrUnsafeRender) {
				t.Errorf("CheckRendered of a identity-free render = %v, want ErrUnsafeRender", hubErr)
			}
			for _, o := range objs[1:] {
				if sa, _, _ := unstructured.NestedString(o.Object, "spec", "serviceAccountName"); mode.ApplyIdentities && sa == "" {
					t.Errorf("Kustomization %s names no identity", o.GetName())
				}
			}
			got := marshalObjects(t, objs)
			path := filepath.Join("testdata", "golden", name+".yaml")
			if *update {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading %s (run with -update to create it): %v", path, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from its golden; run `go test ./pkg/fluxdelivery -run TestSelfHostedRenderGoldens -update` and review the diff.\n--- got ---\n%s", path, got)
			}
		})
	}
}

// TestCheckRenderedSelfHostedRefusesAWrongIdentity: the identity is part of
// the render the check holds to the input.
func TestCheckRenderedSelfHostedRefusesAWrongIdentity(t *testing.T) {
	t.Parallel()
	in := selfHosted(true)
	objs := render(t, in)
	if err := unstructured.SetNestedField(objs[1].Object, fluxdelivery.ApplyServiceAccount, "spec", "serviceAccountName"); err != nil {
		t.Fatal(err)
	}
	if err := fluxdelivery.CheckRenderedSelfHosted(in, objs, fluxdelivery.SelfHosted{ApplyIdentities: true}); !errors.Is(err, fluxdelivery.ErrUnsafeRender) {
		t.Fatalf("a pre tree rendered under the apply identity: CheckRenderedSelfHosted = %v, want ErrUnsafeRender", err)
	}
}
