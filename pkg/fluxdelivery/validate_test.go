package fluxdelivery_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/flux"
	"github.com/reliant-labs/forge/pkg/fluxdelivery"
	"github.com/reliant-labs/forge/pkg/release"
)

// TestValidateRefusals is the refusal matrix: each case breaks one rule of a
// good input and must be refused with that rule's error, before anything is
// written.
func TestValidateRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		base   func() fluxdelivery.BindingInput
		mutate func(in *fluxdelivery.BindingInput)
		want   error
	}{
		{"binding set before the ledger has bindings", flatConnected, func(in *fluxdelivery.BindingInput) {
			in.Binding = "web"
			in.Source.Name = fluxdelivery.SourceName(in.Org, in.Env, "web")
		}, fluxdelivery.ErrBindingUnsupported},
		{"unknown schema", flatConnected, func(in *fluxdelivery.BindingInput) { in.Schema = "forge.dev/flux-delivery/v2" }, fluxdelivery.ErrInvalidInput},
		{"org not a label", flatConnected, func(in *fluxdelivery.BindingInput) { in.Org = "Acme" }, fluxdelivery.ErrInvalidName},
		{"fence not a number", flatConnected, func(in *fluxdelivery.BindingInput) { in.Fence = "x" }, fluxdelivery.ErrInvalidInput},
		{"no promotion", flatConnected, func(in *fluxdelivery.BindingInput) { in.Promotion = "" }, fluxdelivery.ErrInvalidInput},
		{"unknown retire", flatConnected, func(in *fluxdelivery.BindingInput) { in.Retire = "delete" }, fluxdelivery.ErrInvalidInput},
		{"label sets the shard", flatConnected, func(in *fluxdelivery.BindingInput) { in.Labels[fluxdelivery.ShardLabel] = "shard0" }, fluxdelivery.ErrInvalidName},
		{"label sets a flux-operator key", flatConnected, func(in *fluxdelivery.BindingInput) {
			in.Appliers[0].Labels["resourceset.fluxcd.controlplane.io/name"] = "x"
		}, fluxdelivery.ErrInvalidName},
		{"source name composed elsewhere", flatConnected, func(in *fluxdelivery.BindingInput) { in.Source.Name = "acme-env-prod" }, fluxdelivery.ErrInvalidName},
		{"tag instead of digest", flatConnected, func(in *fluxdelivery.BindingInput) { in.Source.Digest = "latest" }, fluxdelivery.ErrNotDigestPinned},
		{"scheme twice", flatConnected, func(in *fluxdelivery.BindingInput) { in.Source.URL = "oci://oci://r/x" }, fluxdelivery.ErrInvalidInput},
		{"no scheme", flatConnected, func(in *fluxdelivery.BindingInput) { in.Source.URL = "registry.example.com/x" }, fluxdelivery.ErrInvalidInput},
		{"unknown provider", flatConnected, func(in *fluxdelivery.BindingInput) { in.Source.Provider = "github" }, fluxdelivery.ErrUnknownProvider},
		{"non-canonical interval", flatConnected, func(in *fluxdelivery.BindingInput) { in.Source.Interval = "5m" }, fluxdelivery.ErrInvalidInput},
		{"enforce with no keys", flatConnected, func(in *fluxdelivery.BindingInput) {
			in.Source.Verify = fluxdelivery.Verify{Mode: fluxdelivery.VerifyEnforce, Provider: "cosign"}
		}, fluxdelivery.ErrInvalidVerify},
		{"enforce with keys and identities", verifyKeyless, func(in *fluxdelivery.BindingInput) { in.Source.Verify.SecretRef = "k" }, fluxdelivery.ErrInvalidVerify},
		{"unknown verify mode", flatConnected, func(in *fluxdelivery.BindingInput) { in.Source.Verify.Mode = "" }, fluxdelivery.ErrInvalidVerify},
		{"no appliers", flatConnected, func(in *fluxdelivery.BindingInput) { in.Appliers = nil }, fluxdelivery.ErrNoAppliers},
		{"applier reads another source", flatConnected, func(in *fluxdelivery.BindingInput) { in.Appliers[0].Source = "acme.env.other" }, fluxdelivery.ErrInvalidInput},
		{"unclustered tree", flatConnected, func(in *fluxdelivery.BindingInput) {
			in.Appliers[0].Cluster = ""
		}, fluxdelivery.ErrNoClusterCredential},
		{"path is not release's", flatConnected, func(in *fluxdelivery.BindingInput) { in.Appliers[0].Path = "manifests/gke" }, fluxdelivery.ErrPathMismatch},
		{"tree path is not release's", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers[1].Path = release.BundleTreePath("daemon", "daemon-substrate", release.PhasePost)
		}, fluxdelivery.ErrPathMismatch},
		{"name composed elsewhere", flatConnected, func(in *fluxdelivery.BindingInput) { in.Appliers[0].Name = "acme-gke" }, fluxdelivery.ErrInvalidName},
		{"no credential", flatConnected, func(in *fluxdelivery.BindingInput) { in.Appliers[0].KubeConfigSecret = "" }, fluxdelivery.ErrNoClusterCredential},
		{"both credentials", flatHosted, func(in *fluxdelivery.BindingInput) { in.Appliers[0].KubeConfigSecret = "k" }, fluxdelivery.ErrNoClusterCredential},
		{"in-cluster without an identity", flatHosted, func(in *fluxdelivery.BindingInput) { in.Appliers[0].ServiceAccountName = "" }, fluxdelivery.ErrNoClusterCredential},
		{"service account on a kubeconfig", flatConnected, func(in *fluxdelivery.BindingInput) {
			in.Appliers[0].ServiceAccountName = flux.DeployServiceAccount
		}, fluxdelivery.ErrServiceAccountOnConnected},
		{"in-cluster tree", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers[0].KubeConfigSecret = ""
			in.Appliers[0].InCluster = true
			in.Appliers[0].ServiceAccountName = flux.DeployServiceAccount
		}, fluxdelivery.ErrNoClusterCredential},
		{"cluster twice", flatConnected, func(in *fluxdelivery.BindingInput) { in.Appliers[1] = in.Appliers[0] }, fluxdelivery.ErrDuplicateApplier},
		{"cluster flat and tree", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers = append(in.Appliers, flatApplier(*in, "hub", "hub-kubeconfig"))
		}, fluxdelivery.ErrDuplicateApplier},
		{"hosted cluster as a tree", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers = append(in.Appliers, treeApplier(*in, release.BundleHostedCluster, "base", release.PhasePre, "k", nil))
		}, fluxdelivery.ErrInvalidTree},
		{"dependency outside the binding", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers[1].DependsOn = []string{"acme.env.other-hub-base-pre"}
		}, fluxdelivery.ErrInvalidTree},
		{"external dependency inside the binding", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers[1].ExternalDependsOn = []string{in.Appliers[0].Name}
		}, fluxdelivery.ErrInvalidTree},
		{"unknown health kind", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers[1].Health = []fluxdelivery.HealthCheck{{APIVersion: "v1", Kind: "Pod", Name: "x"}}
		}, fluxdelivery.ErrInvalidTree},
		{"health apiVersion disagrees", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers[1].Health[0].APIVersion = "apps/v1beta1"
		}, fluxdelivery.ErrInvalidTree},
		{"tree without retry", treesV2, func(in *fluxdelivery.BindingInput) { in.Appliers[0].RetryInterval = "" }, fluxdelivery.ErrInvalidTree},
		{"applier torn down alone", treesV2, func(in *fluxdelivery.BindingInput) {
			in.Appliers[0].Retire = fluxdelivery.RetireTeardown
		}, fluxdelivery.ErrInvalidInput},
		{"patch without a target", flatHosted, func(in *fluxdelivery.BindingInput) { in.Appliers[0].Patches[0].Target.Name = "" }, fluxdelivery.ErrInvalidInput},
	} {
		in := tc.base()
		tc.mutate(&in)
		err := fluxdelivery.Validate(in)
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: Validate = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestValidateRefusesTwoClustersFoldingToOneName: two kube contexts that
// differ only in characters the name folds produce one object name — the
// second Kustomization would overwrite the first.
func TestValidateRefusesTwoClustersFoldingToOneName(t *testing.T) {
	t.Parallel()
	in := base("")
	in.Appliers = []fluxdelivery.Applier{
		flatApplier(in, "gke_prod", "a-kubeconfig"),
		flatApplier(in, "gke-prod", "b-kubeconfig"),
	}
	if err := fluxdelivery.Validate(in); !errors.Is(err, fluxdelivery.ErrDuplicateApplier) {
		t.Fatalf("Validate = %v, want ErrDuplicateApplier", err)
	}
}

// TestValidateNamesTheBindingRule: the non-empty binding refusal says why,
// so a writer that reaches it knows it is ahead of the ledger rather than
// wrong.
func TestValidateNamesTheBindingRule(t *testing.T) {
	t.Parallel()
	err := fluxdelivery.Validate(bindingWeb())
	if err == nil || !strings.Contains(err.Error(), "no per-binding promotion") {
		t.Fatalf("Validate(binding web) = %v", err)
	}
}
