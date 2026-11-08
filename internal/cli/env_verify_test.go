package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cluster"

	releasepkg "github.com/reliant-labs/forge/pkg/release"
)

// Environment verification tests run entirely against a stubbed cluster read.
//
// Not touching a real cluster is a correctness requirement, not a speed
// preference. The case this command exists for — a binding declaring digest A
// while the cluster runs digest B — cannot be produced against a live cluster
// without deliberately deploying a wrong image to it, and MISSING requires an
// env with half its release absent. A test that needed those conditions in a
// real cluster is a test that never runs.

// stubLister is a scripted clusterImageLister. A non-nil err makes the read
// fail the way an unreachable cluster does, which is the only way to exercise
// the UNREACHABLE path.
type stubLister struct {
	images []cluster.WorkloadImage
	err    error
	// calls records the (context, namespace) pairs requested, so a test can
	// assert the read was addressed to the DECLARED cluster rather than
	// whatever context happened to be active.
	calls [][2]string
}

func (s *stubLister) ListWorkloadImages(_ context.Context, kubeContext, namespace string) ([]cluster.WorkloadImage, error) {
	s.calls = append(s.calls, [2]string{kubeContext, namespace})
	if s.err != nil {
		return nil, s.err
	}
	return s.images, nil
}

// Two digests that differ in their FIRST byte as well as their length-equal
// body, so a comparison bug cannot pass by accident of a shared prefix.
const (
	digestDeclared = "sha256:1ea56682243fcb8775a2574ce41b129b28fb5c82cf239b64660e236a458f66fe"
	digestRunning  = "sha256:e1e04a9d980b4a8e38dbe5b7e012606b931edd2edf022e95c076001be027a57f"
)

func deployImage(name, image string) cluster.WorkloadImage {
	return cluster.WorkloadImage{Kind: "Deployment", Name: name, Container: name, Image: image}
}

// TestVerifyEnvImages_Drift is THE test. A binding declares digest A, the
// cluster runs digest B, and the verdict must be DRIFT carrying BOTH digests.
//
// Asserting on both values rather than just the state is deliberate: a report
// that says only "DRIFT" sends the reader back to run the kubectl query the
// command just ran, which is the manual step this command replaces. The
// digests are asserted IN FULL — an abbreviated digest is not something anyone
// can act on.
func TestVerifyEnvImages_Drift(t *testing.T) {
	running := []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+digestRunning),
	}
	declared := map[string]string{"control-plane": digestDeclared}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 {
		t.Fatalf("expected 1 verdict, got %d", len(results))
	}
	got := results[0]
	if got.State != imageDrift {
		t.Fatalf("expected DRIFT for a cluster running a different digest, got %s", got.State)
	}
	if got.Declared != digestDeclared {
		t.Errorf("declared digest = %q, want %q", got.Declared, digestDeclared)
	}
	if got.Running != digestRunning {
		t.Errorf("running digest = %q, want %q", got.Running, digestRunning)
	}
	// The detail line is what a human reads. Both digests must survive into
	// it in full.
	if !strings.Contains(got.Detail, digestDeclared) || !strings.Contains(got.Detail, digestRunning) {
		t.Errorf("detail must name BOTH digests in full, got: %s", got.Detail)
	}
	if tally := tallyEnvStatusRelease(results); tally.Drift != 1 || tally.Match != 0 {
		t.Errorf("tally = %+v, want exactly 1 drift", tally)
	}
}

// TestVerifyEnvImages_Match covers the clean case: same digest, MATCH.
func TestVerifyEnvImages_Match(t *testing.T) {
	running := []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+digestDeclared),
	}
	declared := map[string]string{"control-plane": digestDeclared}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 || results[0].State != imageMatch {
		t.Fatalf("expected a single MATCH, got %+v", results)
	}
	if results[0].Running != digestDeclared {
		t.Errorf("running digest = %q, want %q", results[0].Running, digestDeclared)
	}
}

// TestVerifyEnvImages_Missing pins that iteration is over the DECLARED set.
// An image the binding names and the cluster does not run produces no cluster
// row, so a loop over running workloads would skip it silently and report a
// clean environment for one that never received half its release.
func TestVerifyEnvImages_Missing(t *testing.T) {
	running := []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+digestDeclared),
	}
	declared := map[string]string{
		"control-plane": digestDeclared,
		"reliant":       digestRunning,
	}

	results := verifyEnvImages(running, declared)
	if len(results) != 2 {
		t.Fatalf("expected a verdict per DECLARED image (2), got %d", len(results))
	}
	byImage := map[string]imageVerification{}
	for _, r := range results {
		byImage[r.Image] = r
	}
	if got := byImage["control-plane"].State; got != imageMatch {
		t.Errorf("control-plane = %s, want MATCH", got)
	}
	if got := byImage["reliant"].State; got != imageMissing {
		t.Errorf("reliant = %s, want MISSING", got)
	}
	if tally := tallyEnvStatusRelease(results); tally.Missing != 1 {
		t.Errorf("tally = %+v, want 1 missing", tally)
	}
}

// TestVerifyEnvImages_ExtraRunningImagesAreNotFailures pins the deliberate
// asymmetry: a namespace legitimately contains workloads outside the release
// (a database, a sidecar, another team's chart). Failing on those would make
// the command permanently red for correct environments.
func TestVerifyEnvImages_ExtraRunningImagesAreNotFailures(t *testing.T) {
	running := []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+digestDeclared),
		deployImage("postgres", "docker.io/library/postgres:16"),
	}
	declared := map[string]string{"control-plane": digestDeclared}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 {
		t.Fatalf("expected only the declared image to be reported, got %d verdicts", len(results))
	}
	if tally := tallyEnvStatusRelease(results); tally.Drift != 0 || tally.Missing != 0 {
		t.Errorf("an undeclared running image must not fail verification, tally = %+v", tally)
	}
}

// TestVerifyEnvImages_UntaggedIsNeitherMatchNorDrift covers a --no-digest
// deploy. Calling a mutable tag MATCH would be a green check over bytes nobody
// confirmed; calling it DRIFT would be red for something not proven wrong.
func TestVerifyEnvImages_UntaggedIsNeitherMatchNorDrift(t *testing.T) {
	running := []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane:v1.5.15"),
	}
	declared := map[string]string{"control-plane": digestDeclared}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 || results[0].State != imageUntagged {
		t.Fatalf("expected UNTAGGED for a tag-only workload, got %+v", results)
	}
	tally := tallyEnvStatusRelease(results)
	if tally.Match != 0 || tally.Drift != 0 {
		t.Errorf("untagged must be neither match nor drift, tally = %+v", tally)
	}
}

// TestVerifyEnvImages_CronJobDriftIsCaught is the hole a Deployments-only
// reader would leave. forge renders CronJobs for `kind = "cron"`, so a
// verifier blind to them reports clean while a drifted cron runs old bytes.
func TestVerifyEnvImages_CronJobDriftIsCaught(t *testing.T) {
	running := []cluster.WorkloadImage{
		{Kind: "CronJob", Name: "nightly-reconcile", Container: "worker", Image: "ghcr.io/acme/control-plane@" + digestRunning},
	}
	declared := map[string]string{"control-plane": digestDeclared}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 || results[0].State != imageDrift {
		t.Fatalf("expected CronJob drift to be caught, got %+v", results)
	}
	if len(results[0].Workloads) != 1 || !strings.Contains(results[0].Workloads[0], "CronJob/nightly-reconcile") {
		t.Errorf("verdict must name the CronJob workload, got %v", results[0].Workloads)
	}
}

// TestVerifyEnvImages_PartialRolloutIsDrift covers two workloads sharing an
// image name but running different digests — a stuck rollout. Reporting the
// first digest found would make the verdict depend on map/slice ordering,
// which is how a check passes for the wrong reason.
func TestVerifyEnvImages_PartialRolloutIsDrift(t *testing.T) {
	running := []cluster.WorkloadImage{
		deployImage("api-server", "ghcr.io/acme/reliant@"+digestDeclared),
		deployImage("worker", "ghcr.io/acme/reliant@"+digestRunning),
	}
	declared := map[string]string{"reliant": digestDeclared}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 || results[0].State != imageDrift {
		t.Fatalf("expected DRIFT when workloads run different digests, got %+v", results)
	}
	// Both digests must appear, or the reader cannot see which workload is
	// behind.
	if !strings.Contains(results[0].Detail, digestDeclared) || !strings.Contains(results[0].Detail, digestRunning) {
		t.Errorf("partial-rollout detail must name both digests, got: %s", results[0].Detail)
	}
}

// TestVerifyEnvImages_ConfigPinnedImageIsNotMissing is a REGRESSION TEST for a
// false positive found by running this command against real prod.
//
// control-plane's release ships `workspace-base`, which no workload runs: the
// workspace-controller launches those pods on demand and pins the image in its
// DAEMON_IMAGE env var. Reading only container images reported MISSING and
// exited 1 against a completely healthy prod — a permanently-red gate on a
// correct environment, which is the failure mode that gets a check deleted.
//
// The digest IS pinned and IS checkable, so it verifies like any other image.
func TestVerifyEnvImages_ConfigPinnedImageIsNotMissing(t *testing.T) {
	running := []cluster.WorkloadImage{
		deployImage("workspace-controller", "ghcr.io/acme/control-plane@"+digestDeclared),
		{
			Kind: "Deployment", Name: "workspace-controller", Container: "workspace-controller",
			Image:  "ghcr.io/acme/workspace-base@" + digestRunning,
			EnvVar: "DAEMON_IMAGE",
		},
	}
	declared := map[string]string{
		"control-plane":  digestDeclared,
		"workspace-base": digestRunning,
	}

	results := verifyEnvImages(running, declared)
	byImage := map[string]imageVerification{}
	for _, r := range results {
		byImage[r.Image] = r
	}
	if got := byImage["workspace-base"].State; got != imageMatch {
		t.Errorf("a digest-pinned config reference must verify as MATCH, got %s", got)
	}
	if tally := tallyEnvStatusRelease(results); tally.Missing != 0 {
		t.Errorf("config-pinned image must not be MISSING, tally = %+v", tally)
	}
}

// TestVerifyEnvImages_ConfigPinnedDriftIsCaught is the other half: catching the
// false positive above must NOT make a drifted operator image invisible.
func TestVerifyEnvImages_ConfigPinnedDriftIsCaught(t *testing.T) {
	running := []cluster.WorkloadImage{
		{
			Kind: "Deployment", Name: "workspace-controller", Container: "workspace-controller",
			Image:  "ghcr.io/acme/workspace-base@" + digestRunning,
			EnvVar: "DAEMON_IMAGE",
		},
	}
	declared := map[string]string{"workspace-base": digestDeclared}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 || results[0].State != imageDrift {
		t.Fatalf("a drifted config-pinned image must be caught, got %+v", results)
	}
	// The report must say WHERE the reference lives, or the reader looks for
	// a running container that does not exist.
	if len(results[0].Workloads) != 1 || !strings.Contains(results[0].Workloads[0], "$DAEMON_IMAGE") {
		t.Errorf("verdict must name the env var carrying the reference, got %v", results[0].Workloads)
	}
}

// TestUnreachableVerifications pins that a failed cluster read produces
// UNREACHABLE for every declared image and NEVER drift. A VPN drop must not be
// reported as a release defect.
func TestUnreachableVerifications(t *testing.T) {
	declared := map[string]string{"control-plane": digestDeclared, "reliant": digestRunning}
	results := unreachableVerifications(declared, errors.New("dial tcp: i/o timeout"))

	if len(results) != 2 {
		t.Fatalf("expected a verdict per declared image, got %d", len(results))
	}
	for _, r := range results {
		if r.State != imageUnreachable {
			t.Errorf("%s = %s, want UNREACHABLE", r.Image, r.State)
		}
		if !strings.Contains(r.Detail, "i/o timeout") {
			t.Errorf("%s detail must carry the cause, got: %s", r.Image, r.Detail)
		}
	}
	tally := tallyEnvStatusRelease(results)
	if tally.Unreachable != 2 || tally.Drift != 0 || tally.Missing != 0 {
		t.Errorf("unreachable must never count as drift or missing, tally = %+v", tally)
	}
}

// TestParseImageRef covers the reference shapes a real cluster serves. The
// registry-port case is the one that breaks a naive `strings.Split(ref, ":")`:
// "localhost:5000/api" must not read as image "localhost".
func TestParseImageRef(t *testing.T) {
	cases := []struct {
		ref        string
		wantName   string
		wantDigest string
		wantTag    string
	}{
		{"ghcr.io/acme/control-plane@" + digestDeclared, "control-plane", digestDeclared, ""},
		{"ghcr.io/acme/control-plane:v1.5.15", "control-plane", "", "v1.5.15"},
		{"localhost:5000/api:dev", "api", "", "dev"},
		{"localhost:5000/api@" + digestDeclared, "api", digestDeclared, ""},
		{"postgres", "postgres", "", ""},
		{"postgres:16", "postgres", "", "16"},
		{"ghcr.io/acme/api:v1@" + digestDeclared, "api", digestDeclared, "v1"},
	}
	for _, tc := range cases {
		got := parseImageRef(tc.ref)
		if got.Name != tc.wantName || got.Digest != tc.wantDigest || got.Tag != tc.wantTag {
			t.Errorf("parseImageRef(%q) = {name:%q digest:%q tag:%q}, want {name:%q digest:%q tag:%q}",
				tc.ref, got.Name, got.Digest, got.Tag, tc.wantName, tc.wantDigest, tc.wantTag)
		}
	}
}

// ── Exit codes ──────────────────────────────────────────────────────────────

// runEnvVerifyInDir runs the command against a project dir containing a
// hand-written binding ledger, with the cluster read stubbed and the
// context/namespace injected (a temp dir has no KCL to render).
func runEnvVerifyInDir(t *testing.T, dir, envName string, lister clusterImageLister) error {
	t.Helper()
	t.Chdir(dir)
	return runEnvStatusRelease(context.Background(), envName, envStatusOptions{
		Lister:   lister,
		Resolver: stubResolver{target: envTarget{KubeContext: "test-context", Namespace: "test-ns"}},
	})
}

// stubResolver stands in for the KCL render, which needs a whole project on
// disk. It resolves through the SAME field the production command writes, so
// these tests exercise the real code path rather than a test-only shape.
type stubResolver struct {
	target envTarget
}

func (s stubResolver) Resolve(_ context.Context, _, _ string) envTarget { return s.target }

// writeBinding appends one promotion of envName to the file ledger.
func writeBinding(t *testing.T, dir, envName, release string, resolved map[string]string) {
	t.Helper()
	if _, err := testBindings(t, dir).Append(context.Background(), releasepkg.Promotion{
		Env: envName, Release: release, Kind: releasepkg.KindPromote, Resolved: resolved,
	}, appendGuard{}); err != nil {
		t.Fatalf("write binding ledger: %v", err)
	}
}

func exitCodeOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	return -1 // a plain error: not one of the modelled outcomes
}

// TestRunEnvVerify_DriftExits1 is the headline case end to end: the command
// must FAIL, and the failure must be exit 1 specifically.
func TestRunEnvVerify_DriftExits1(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	lister := &stubLister{images: []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+digestRunning),
	}}

	err := runEnvVerifyInDir(t, dir, "prod", lister)
	if err == nil {
		t.Fatal("a cluster running a different digest must FAIL verification")
	}
	if code := exitCodeOf(t, err); code != 1 {
		t.Errorf("drift exit code = %d, want 1 (got error: %v)", code, err)
	}
	// The read must have been addressed to the declared cluster.
	if len(lister.calls) != 1 || lister.calls[0] != [2]string{"test-context", "test-ns"} {
		t.Errorf("cluster read addressed to %v, want one read of (test-context, test-ns)", lister.calls)
	}
}

// TestRunEnvVerify_MatchExits0 pins the clean path at 0.
func TestRunEnvVerify_MatchExits0(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	lister := &stubLister{images: []cluster.WorkloadImage{
		deployImage("control-plane", "ghcr.io/acme/control-plane@"+digestDeclared),
	}}

	if err := runEnvVerifyInDir(t, dir, "prod", lister); err != nil {
		t.Fatalf("a matching environment must verify clean, got: %v", err)
	}
}

// TestRunEnvVerify_MissingExits1 — declared but not running is a failure, at
// the same exit code as drift. Both mean "the env is not what the ledger says".
func TestRunEnvVerify_MissingExits1(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	err := runEnvVerifyInDir(t, dir, "prod", &stubLister{images: nil})
	if code := exitCodeOf(t, err); code != 1 {
		t.Errorf("missing exit code = %d, want 1 (got error: %v)", code, err)
	}
}

// TestRunEnvVerify_UnreachableExits2 is the distinction that keeps the command
// usable in CI. A cluster that cannot be read is exit 2, NOT exit 1 — a
// network failure is not evidence against a release.
func TestRunEnvVerify_UnreachableExits2(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	lister := &stubLister{err: fmt.Errorf("Unable to connect to the server: dial tcp: i/o timeout")}

	err := runEnvVerifyInDir(t, dir, "prod", lister)
	if err == nil {
		t.Fatal("an unreadable cluster must not report success")
	}
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("unreachable exit code = %d, want 2 — a network failure must not be reported as drift (got error: %v)", code, err)
	}
}

// TestRunEnvVerify_NoBindingExits0 — an env that was never promoted has
// declared nothing, so there is nothing to be wrong about. Exiting non-zero
// here would make the command permanently red for a healthy env that does not
// use releases, and a permanently-red gate is a deleted gate.
func TestRunEnvVerify_NoBindingExits0(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	// "staging" has no binding in this ledger.
	lister := &stubLister{}
	if err := runEnvVerifyInDir(t, dir, "staging", lister); err != nil {
		t.Fatalf("an env with no binding is not a failure, got: %v", err)
	}
	if len(lister.calls) != 0 {
		t.Errorf("an unbound env must not read the cluster at all, got %v", lister.calls)
	}
}

// TestRunEnvVerify_NoDeclaredClusterExits2 covers an env whose KCL names no
// cluster (host-only or compose). Nothing can be learned about what it runs,
// so it is UNREACHABLE and exit 2 — never drift, which would accuse a release
// on the basis of a question that was never asked.
func TestRunEnvVerify_NoDeclaredClusterExits2(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "prod", "v1.5.15", map[string]string{"control-plane": digestDeclared})

	// The env IS declared here — an empty deploy/kcl/prod/main.k stands in
	// for a host-only or compose env. Without this the run would take the
	// "not declared in this checkout" branch instead, and this test would
	// silently stop covering the case it names.
	mainK := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
	if err := os.MkdirAll(filepath.Dir(mainK), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(mainK, []byte("# host-only env: no K8sCluster\n"), 0o600); err != nil {
		t.Fatalf("write main.k: %v", err)
	}

	lister := &stubLister{}
	t.Chdir(dir)
	var err error
	out := captureStdout(t, func() {
		err = runEnvStatusRelease(context.Background(), "prod", envStatusOptions{
			Lister:   lister,
			Resolver: stubResolver{target: envTarget{}}, // nothing declared
		})
	})
	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("undeclared cluster exit code = %d, want 2 (got error: %v)", code, err)
	}
	if !strings.Contains(out, "no forge.K8sCluster.cluster") {
		t.Errorf("a declared env with no cluster field must say which field is missing, got:\n%s", out)
	}
	if len(lister.calls) != 0 {
		t.Errorf("must not attempt a cluster read with no context, got %v", lister.calls)
	}
}

// TestRunEnvVerify_UndeclaredEnvSaysSo pins the diagnostic for an env that is
// bound in the ledger but absent from this checkout — a release cut on a
// branch that declares it, verified from one that does not. Found by running
// against the real control-plane ledger, whose `preprod` and `staging`
// bindings have no deploy/kcl directory here.
//
// Both this and "declared but no cluster field" exit 2, so only the MESSAGE
// distinguishes them. Pointing this reader at deploy/kcl/<env>/main.k would
// name a file they will not find.
func TestRunEnvVerify_UndeclaredEnvSaysSo(t *testing.T) {
	dir := t.TempDir()
	writeBinding(t, dir, "preprod", "v1.3.0", map[string]string{"control-plane": digestDeclared})

	t.Chdir(dir)
	var err error
	out := captureStdout(t, func() {
		err = runEnvStatusRelease(context.Background(), "preprod", envStatusOptions{
			Lister:   &stubLister{},
			Resolver: stubResolver{target: envTarget{}},
		})
	})

	if code := exitCodeOf(t, err); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out, "not declared in this checkout") {
		t.Errorf("must say the env is absent from this checkout, got:\n%s", out)
	}
	if strings.Contains(out, "no forge.K8sCluster.cluster declared") {
		t.Errorf("must NOT blame a missing KCL field when the file does not exist, got:\n%s", out)
	}
}

// TestRunEnvVerify_DriftBeatsUnreachable pins precedence. With one image
// drifted and another unreadable, the exit code must be 1: a proven defect
// outranks an incomplete check, and reporting 2 would let a real drift hide
// behind a flaky read.
func TestRunEnvVerify_DriftBeatsUnreachable(t *testing.T) {
	results := []imageVerification{
		{Image: "a", State: imageDrift},
		{Image: "b", State: imageUnreachable},
	}
	tally := tallyEnvStatusRelease(results)
	if tally.Drift != 1 || tally.Unreachable != 1 {
		t.Fatalf("tally = %+v", tally)
	}
	// The command's switch checks drift/missing first; assert that ordering
	// directly so a reordering of the switch is caught here.
	var code int
	switch {
	case tally.Drift > 0 || tally.Missing > 0:
		code = 1
	case tally.Unreachable > 0:
		code = 2
	}
	if code != 1 {
		t.Errorf("drift must outrank unreachable, got exit %d", code)
	}
}

// TestVerifyEnvImages_DeclaredByRepositoryPath is the regression test for a
// verifier that reported a perfectly healthy production environment as
// entirely undeployed.
//
// THE BUG. Running images were indexed by BARE NAME ("control-plane"), and
// every test in this file declared them the same way, so the comparison looked
// symmetric. A real binding does not: `forge env deploy <env> <version>` writes Resolved
// keyed by the FULL REPOSITORY PATH it pushed to, because that is the
// identity it resolved a digest for. Prod's binding therefore said
//
//	us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod/control-plane
//
// and the lookup asked for "control-plane". No key ever matched, so all four
// images of release v1.7.5 came back MISSING — "never deployed, or deleted
// since" — for a namespace whose fourteen deployments were up, ready, and
// running those exact digests.
//
// WHY THIS IS THE DANGEROUS DIRECTION OF FAILURE. A verifier that cries
// MISSING for a healthy env is not merely noisy: MISSING is indistinguishable
// from a half-delivered release, so the documented response is to deploy
// again. The one command whose job is to prove prod is fine was the command
// arguing for an unnecessary prod deploy.
//
// The fix matches on the repository SUFFIX, so a binding may name an image
// either way and both resolve.
func TestVerifyEnvImages_DeclaredByRepositoryPath(t *testing.T) {
	const repo = "us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod/control-plane"

	running := []cluster.WorkloadImage{
		deployImage("admin-server", repo+"@"+digestDeclared),
	}
	// Keyed exactly as a real promotion ledger keys it.
	declared := map[string]string{repo: digestDeclared}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 {
		t.Fatalf("expected 1 verdict, got %d", len(results))
	}
	if got := results[0]; got.State != imageMatch {
		t.Fatalf("a cluster running the declared digest must MATCH, got %s (%s)", got.State, got.Detail)
	}
}

// TestVerifyEnvImages_RegistryMoveIsNotAMatch pins the limit of that
// suffix rule: sharing a trailing path segment is not identity.
//
// Two registries can both host ".../reliant-prod/control-plane", and a
// binding that names one while the cluster runs the other is a REAL finding —
// the env is pulling from somewhere the release did not publish. Matching on
// the bare last segment would have called that clean, which is how a
// suffix rule turns into a different silent pass.
func TestVerifyEnvImages_RegistryMoveIsNotAMatch(t *testing.T) {
	running := []cluster.WorkloadImage{
		deployImage("admin-server", "other.registry.example/acme/control-plane@"+digestDeclared),
	}
	declared := map[string]string{
		"us-central1-docker.pkg.dev/reliant-labs-475814/reliant-prod/control-plane": digestDeclared,
	}

	results := verifyEnvImages(running, declared)
	if len(results) != 1 {
		t.Fatalf("expected 1 verdict, got %d", len(results))
	}
	if got := results[0]; got.State != imageMissing {
		t.Fatalf("an image from a DIFFERENT registry must not satisfy the binding, got %s", got.State)
	}
}
