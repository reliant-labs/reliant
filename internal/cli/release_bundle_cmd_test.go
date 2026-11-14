package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/release"
)

// `forge release bundle <env> <version>` pulls exactly the bundle the release
// recorded, unpacks it, and with --live-diff runs one client-side kubectl diff
// per cluster, reporting every object that would change. It writes nothing to
// any cluster.
func TestReleaseBundle_PullsTheRecordedBundleAndDiffsItLive(t *testing.T) {
	if testing.Short() {
		t.Skip("writes the on-disk release ledger for real; runs in task test")
	}
	dir := newLedgerTestProject(t, "bundle-pull")
	stubEnvShape(t, "bundle-pull")
	ctx := context.Background()
	written, err := writeEnvBundle(ctx, dir, "prod", bundleBuildInputs{
		Release: "v1", Pins: release.BundlePins{Images: map[string]string{"api": "sha256:" + rep64('1')}},
		Now: bundleTestNow, errOut: io.Discard,
	})
	if err != nil {
		t.Fatalf("record the bundle: %v", err)
	}

	type call struct{ context, dir string }
	var calls []call
	prev := kubectlLiveDiff
	kubectlLiveDiff = func(_ context.Context, kubeContext, path string) (string, int, error) {
		calls = append(calls, call{kubeContext, path})
		return "diff -u -N /tmp/LIVE-1/apps.v1.Deployment.app.api /tmp/MERGED-2/apps.v1.Deployment.app.api\n" +
			"--- /tmp/LIVE-1/apps.v1.Deployment.app.api\n+++ /tmp/MERGED-2/apps.v1.Deployment.app.api\n" +
			"-  replicas: 1\n+  replicas: 2\n", 1, nil
	}
	t.Cleanup(func() { kubectlLiveDiff = prev })

	out := t.TempDir()
	var buf bytes.Buffer
	if err := runReleaseBundle(ctx, &buf, dir, "prod", "v1", releaseBundleOptions{Dir: out, LiveDiff: true}); err != nil {
		t.Fatalf("release bundle: %v\n%s", err, buf.String())
	}
	got := buf.String()
	if !strings.Contains(got, shortDigest(written.Digest)) || !strings.Contains(got, "release v1") {
		t.Errorf("the summary does not name the release's bundle %s:\n%s", shortDigest(written.Digest), got)
	}
	if _, err := os.Stat(filepath.Join(out, "bundle.json")); err != nil {
		t.Errorf("bundle.json not written: %v", err)
	}
	if len(calls) != 1 || calls[0].context != "prod-ctx" || !strings.HasPrefix(calls[0].dir, filepath.Join(out, "manifests")) {
		t.Fatalf("kubectl diff calls = %+v; want one, against prod-ctx, over the unpacked manifests", calls)
	}
	if entries, _ := os.ReadDir(calls[0].dir); len(entries) == 0 {
		t.Errorf("the diffed directory %s is empty: the manifests were not unpacked there", calls[0].dir)
	}
	if !strings.Contains(got, "1 object(s) would change") || !strings.Contains(got, "apps.v1.Deployment.app.api") {
		t.Errorf("the live diff summary does not name the changed object:\n%s", got)
	}

	// The digest form reaches the same bundle.
	buf.Reset()
	if err := runReleaseBundle(ctx, &buf, dir, "prod", written.Digest, releaseBundleOptions{Dir: t.TempDir()}); err != nil {
		t.Fatalf("release bundle by digest: %v", err)
	}
	if !strings.Contains(buf.String(), shortDigest(written.Digest)) {
		t.Errorf("by digest:\n%s", buf.String())
	}
}

// The live diff runs against prod and is documented as writing nothing, so it
// is a CLIENT-side `kubectl diff` and nothing else. --server-side makes it a
// server-side apply request, which is how field ownership is taken, and
// --force-conflicts takes it from every other manager: a read-only prod diff
// never carries either (the owner's standing rule since 2026-09-30). This
// drives the real exec through a kubectl stand-in that records its argv, so
// the seam cannot be the thing that looks right.
func TestLiveDiff_IsAClientSideKubectlDiff(t *testing.T) {
	requirePOSIXFake(t, "kubectl")
	bin := t.TempDir()
	argvFile := filepath.Join(bin, "argv")
	stub := "#!/bin/sh\nprintf '%s\\n' \"$@\" > '" + argvFile + "'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "manifests", "prod-ctx"), 0o755); err != nil {
		t.Fatal(err)
	}
	diffs, err := liveDiffBundle(context.Background(), dir,
		[]release.BundleClusterTree{{Cluster: "prod-ctx", Path: "manifests/prod-ctx"}})
	if err != nil {
		t.Fatalf("live diff: %v (%+v)", err, diffs)
	}
	raw, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatalf("kubectl was never run: %v", err)
	}
	argv := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	for _, a := range argv {
		if strings.HasPrefix(a, "--server-side") || strings.HasPrefix(a, "--force-conflicts") || strings.HasPrefix(a, "--field-manager") {
			t.Errorf("the live diff passes %s: it must be a client-side kubectl diff (argv %q)", a, argv)
		}
	}
	want := []string{"--context", "prod-ctx", "diff", "-R", "-f", filepath.Join(dir, "manifests", "prod-ctx")}
	if strings.Join(argv, " ") != strings.Join(want, " ") {
		t.Errorf("kubectl argv = %q\nwant          %q", argv, want)
	}
}

func TestReleaseBundle_IsAReleaseSubcommand(t *testing.T) {
	cmd, _, err := newReleaseCmd().Find([]string{"bundle"})
	if err != nil || cmd == nil || cmd.Name() != "bundle" {
		t.Fatalf("forge release bundle is not registered: %v", err)
	}
	if cmd.Flags().Lookup("live-diff") == nil || cmd.Flags().Lookup("dir") == nil {
		t.Error("forge release bundle must take --dir and --live-diff")
	}
}

// One object kubectl cannot diff client-side — an env var moving from
// valueFrom to value on an object forge applied server-side (ae15fc11) — must
// not hide the rest of the cluster's diff behind COULD NOT DIFF. The cluster is
// re-diffed object by object: the changed objects are still listed, and only
// the undiffable one is named, with kubectl's reason.
func TestLiveDiff_OneUndiffableObjectDoesNotHideTheCluster(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "manifests", "prod-ctx")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"apps.v1.Deployment.cp.workers.yaml", "apps.v1.Deployment.cp.api.yaml", "v1.ConfigMap.cp.flags.yaml"} {
		if err := os.WriteFile(filepath.Join(tree, f), []byte("kind: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const invalid = `The Deployment "control-plane-workers" is invalid: spec.template.spec.containers[0].env[26].valueFrom: Invalid value: "": may not be specified when ` + "`value`" + ` is not empty`
	prev := kubectlLiveDiff
	kubectlLiveDiff = func(_ context.Context, _, path string) (string, int, error) {
		switch filepath.Base(path) {
		case "prod-ctx", "apps.v1.Deployment.cp.workers.yaml":
			return "", -1, errors.New(invalid)
		case "apps.v1.Deployment.cp.api.yaml":
			return "+++ /tmp/MERGED-2/apps.v1.Deployment.cp.api\n-  replicas: 1\n+  replicas: 2\n", 1, nil
		}
		return "", 0, nil
	}
	t.Cleanup(func() { kubectlLiveDiff = prev })

	diffs, err := liveDiffBundle(context.Background(), dir, []release.BundleClusterTree{{Cluster: "prod-ctx", Path: "manifests/prod-ctx"}})
	if err == nil {
		t.Error("a partly diffed cluster must still be reported as not fully diffed")
	}
	if len(diffs) != 1 {
		t.Fatalf("diffs = %+v", diffs)
	}
	d := diffs[0]
	if d.Error != "" {
		t.Fatalf("the whole cluster was reported as COULD NOT DIFF: %s", d.Error)
	}
	if len(d.Changed) != 1 || d.Changed[0] != "apps.v1.Deployment.cp.api" {
		t.Errorf("changed = %v, want the api Deployment", d.Changed)
	}
	if len(d.Undiffable) != 1 || d.Undiffable[0].File != "apps.v1.Deployment.cp.workers.yaml" || !strings.Contains(d.Undiffable[0].Error, "valueFrom") {
		t.Errorf("undiffable = %+v, want only the workers Deployment with kubectl's reason", d.Undiffable)
	}
	var buf bytes.Buffer
	renderLiveDiff(&buf, diffs)
	for _, want := range []string{"1 object(s) would change", "~ apps.v1.Deployment.cp.api", "? apps.v1.Deployment.cp.workers.yaml"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("render is missing %q:\n%s", want, buf.String())
		}
	}
	if strings.Contains(buf.String(), "COULD NOT DIFF:") {
		t.Errorf("render still hides the cluster:\n%s", buf.String())
	}
}

// A cluster where every object fails (unreachable, no credentials) is still
// one COULD NOT DIFF line, not a list of every object.
func TestLiveDiff_UnreachableClusterIsOneError(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "manifests", "prod-ctx")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "v1.ConfigMap.cp.flags.yaml"), []byte("kind: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := kubectlLiveDiff
	kubectlLiveDiff = func(context.Context, string, string) (string, int, error) {
		return "", -1, errors.New("dial tcp 10.0.0.1:443: i/o timeout")
	}
	t.Cleanup(func() { kubectlLiveDiff = prev })
	diffs, err := liveDiffBundle(context.Background(), dir, []release.BundleClusterTree{{Cluster: "prod-ctx", Path: "manifests/prod-ctx"}})
	if err == nil || len(diffs) != 1 || !strings.Contains(diffs[0].Error, "i/o timeout") || len(diffs[0].Undiffable) != 0 {
		t.Fatalf("diffs = %+v, err = %v; want one cluster error", diffs, err)
	}
}

// The object-by-object fallback diffs only what `kubectl diff -R -f` reads
// (.json, .yaml, .yml): an ancillary file in the tree is never handed to
// kubectl, so it can neither be named undiffable nor make a diffable cluster
// look unreachable.
func TestLiveDiff_FallbackSkipsAncillaryFiles(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "manifests", "prod-ctx")
	if err := os.MkdirAll(tree, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"apps.v1.Deployment.cp.api.yaml", "README.md", "metadata.txt"} {
		if err := os.WriteFile(filepath.Join(tree, f), []byte("kind: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var asked []string
	prev := kubectlLiveDiff
	kubectlLiveDiff = func(_ context.Context, _, path string) (string, int, error) {
		base := filepath.Base(path)
		asked = append(asked, base)
		switch base {
		case "prod-ctx":
			return "", -1, errors.New("forced recursive failure")
		case "apps.v1.Deployment.cp.api.yaml":
			return "+++ /tmp/MERGED-2/apps.v1.Deployment.cp.api\n-  replicas: 1\n+  replicas: 2\n", 1, nil
		}
		return "", -1, errors.New("error: unable to decode " + base)
	}
	t.Cleanup(func() { kubectlLiveDiff = prev })

	diffs, err := liveDiffBundle(context.Background(), dir, []release.BundleClusterTree{{Cluster: "prod-ctx", Path: "manifests/prod-ctx"}})
	if err != nil {
		t.Errorf("a cluster whose every manifest diffed must not be reported as failed: %v", err)
	}
	if len(diffs) != 1 || diffs[0].Error != "" || len(diffs[0].Undiffable) != 0 {
		t.Fatalf("diffs = %+v; want a clean diff with no undiffable objects", diffs)
	}
	if len(diffs[0].Changed) != 1 || diffs[0].Changed[0] != "apps.v1.Deployment.cp.api" {
		t.Errorf("changed = %v, want the api Deployment", diffs[0].Changed)
	}
	for _, a := range asked {
		if a == "README.md" || a == "metadata.txt" {
			t.Errorf("kubectl was asked to diff ancillary file %s", a)
		}
	}
}
