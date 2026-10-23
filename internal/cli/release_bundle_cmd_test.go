package cli

import (
	"bytes"
	"context"
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
