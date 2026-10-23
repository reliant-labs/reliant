package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// A release cut before #322 keys its OCI artifacts by bare image name, because
// the registry was an environment's property. #322 re-keyed the ledger by
// repository, so those entries name no address and cannot be verified —
// hounders' v0.1.1 became unverifiable this way.
//
// These tests pin BOTH halves of the answer: the verdict stays UNVERIFIABLE
// (forge has not proven the artifact wrong), and the message names the cause
// and the one action that resolves it, rather than describing the symptom.
func TestVerifyOCI_PreRegistryLedgerKeyExplainsTheReKey(t *testing.T) {
	art := release.Artifact{
		Kind:    release.KindOCI,
		Mode:    release.ModeShared,
		Digests: map[string]string{release.SharedVariant: sha("a")},
	}
	// "hounders" — the bare key a pre-#322 `forge env build --release --push`
	// wrote, with no registry host in it.
	got := verifyOCIArtifact(context.Background(), nil, "hounders", art)

	if got.Status != verifyUnverifiable {
		t.Errorf("status = %v, want UNVERIFIABLE: forge has not shown the artifact is absent or mismatched, only that it cannot be addressed", got.Status)
	}
	for _, want := range []string{
		"names no registry host",
		"before forge v0.1.20",
		"--release",
	} {
		if !strings.Contains(got.Detail, want) {
			t.Errorf("detail must contain %q so the user learns the cause and the fix, got:\n%s", want, got.Detail)
		}
	}
	// The refusal to rewrite is the load-bearing decision, so it is stated
	// to the user rather than left as a code comment.
	if !strings.Contains(got.Detail, "does NOT rewrite") {
		t.Errorf("detail must say forge will not rewrite the key, and why:\n%s", got.Detail)
	}
}

// A repository that DOES name a host must not pick up the pre-#322 explanation:
// it is addressable, so it gets checked like any other.
func TestVerifyOCI_QualifiedRepositoryIsNotTreatedAsLegacy(t *testing.T) {
	detail := bareRepositoryDetail("hounders", sha("a"))
	if strings.Contains(detail, "ghcr.io/<owner>/ghcr.io") {
		t.Error("the suggested reference must be built from the bare name, not doubled")
	}
	if !strings.Contains(detail, `"ghcr.io/<owner>/hounders"`) {
		t.Errorf("the suggestion must show the bare name under a registry:\n%s", detail)
	}
}
