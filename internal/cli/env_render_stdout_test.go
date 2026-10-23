package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/kclplugin"
	releasepkg "github.com/reliant-labs/forge/pkg/release"
)

// Tests for the one guarantee `forge env render` makes about its streams:
// STDOUT CARRIES ONLY THE MANIFESTS. See runEnvRender.
//
// The documented use is `forge env render prod | kubectl diff -f -`. One
// prose line on stdout and kubectl reads it as the first YAML document and
// rejects the whole stream — which is exactly what the release-override
// `Note:` did to control-plane's prod render.

const (
	// renderBuiltDigest is what `forge env build --push` recorded for the image.
	renderBuiltDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	// renderReleaseDigest is what the promoted release pins for it. They
	// DIFFER on purpose: that disagreement is what makes resolveDeployDigests
	// print its "deploying the RELEASE" Note — the line that broke prod.
	renderReleaseDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

// writeRenderStdoutProject writes a minimal forge project whose prod env
// renders one Deployment and one one-shot Job, with a build state and a
// promotion that disagree about the image's digest.
func writeRenderStdoutProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("forge.yaml", "name: rendertest\nmodule_path: github.com/example/rendertest\nversion: \"0.1.0\"\n")
	write("deploy/kcl/kcl.mod", "[package]\nname = \"rendertest-deploy\"\nedition = \"v0.11.0\"\nversion = \"0.0.1\"\n")
	write("deploy/kcl/prod/main.k", `import forge
import forge.workloads as fw

_target = forge.ClusterTarget {
    cluster = "k3d-rendertest"
    namespace = "rendertest-prod"
}

_bundle = forge.Bundle {
    project = "rendertest"
    cluster_target = _target
    workloads = [_w | {runtime = forge.OnCluster {target = _target}} if not _w.runtime else _w for _w in [
        fw.Workload {name = "api", image = "reg.example.com/rendertest"}
        # A standalone job (no before): a batch/v1 Job in the stream.
        fw.Workload {name = "migrate", kind = "job", image = "reg.example.com/rendertest", args = ["db", "migrate", "up"]}
    ]]
}

output = forge.render(_bundle)
`)
	write(".forge/state/build-prod.json", `{"image": "reg.example.com/rendertest", "tag": "abc1234", "pushed": true, "pushed_at": "2026-09-24T00:00:00Z", "digest": "`+renderBuiltDigest+`"}`)
	if _, err := testBindings(t, dir).Append(context.Background(), releasepkg.Promotion{
		Env: "prod", Release: "v1.0.0", Kind: releasepkg.KindPromote,
		Resolved: map[string]string{"reg.example.com/rendertest": renderReleaseDigest},
	}, appendGuard{}); err != nil {
		t.Fatalf("write promotion: %v", err)
	}
	return dir
}

// runRenderCapturingProcessStdout runs `forge env render <env>` with cobra's
// output left UNSET — the real CLI's configuration, where cmd.OutOrStdout()
// is os.Stdout — and returns everything the PROCESS wrote to stdout and to
// stderr. Capturing the process's file descriptors, not a cobra buffer, is the
// point: the Note was a fmt.Printf, which never goes near cobra's writer.
func runRenderCapturingProcessStdout(t *testing.T, dir, env string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)

	outR, outW, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	errR, errW, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	realOut, realErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC := make(chan string)
	errC := make(chan string)
	go func() { b, _ := io.ReadAll(outR); outC <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errC <- string(b) }()

	cmd := newEnvRenderCmd()
	cmd.SetArgs(append([]string{env}, args...))
	cmd.SetContext(context.Background())
	err = cmd.Execute()

	os.Stdout, os.Stderr = realOut, realErr
	outW.Close()
	errW.Close()
	return <-outC, <-errC, err
}

// TestEnvRender_StdoutIsOnlyManifests is the regression test for the prod
// render that `kubectl apply --dry-run=client` could not parse.
//
// It renders a project whose promoted release overrides a fresher build — the
// exact condition that prints the release-override Note — and then parses the
// process's stdout as a YAML document stream. Every line must belong to a
// Kubernetes document (or be forge's own `# cluster:` comment, which YAML
// ignores); the Note must be on stderr instead, still readable.
//
// Mutation that fails it: remove the os.Stdout divert in runEnvRender — the
// Note lands on stdout above the first document.
func TestEnvRender_StdoutIsOnlyManifests(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
	dir := writeRenderStdoutProject(t)

	stdout, stderr, err := runRenderCapturingProcessStdout(t, dir, "prod")
	if err != nil {
		t.Fatalf("forge env render prod: %v\nstderr:\n%s", err, stderr)
	}

	// The precondition that makes this test mean something: the Note fired.
	// Without it, a clean stdout would prove nothing.
	if !strings.Contains(stdout+stderr, "deploying the RELEASE") {
		t.Fatalf("the release-override Note never fired, so this test is not exercising the defect — check the fixture's build state and promotion\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if !strings.Contains(stderr, "deploying the RELEASE") {
		t.Errorf("the Note is still true and must stay visible — on stderr, got stderr:\n%s", stderr)
	}

	docs := assertYAMLManifestStream(t, stdout)
	kinds := map[string]bool{}
	for _, d := range docs {
		kinds[d.Kind] = true
	}
	for _, want := range []string{"Deployment", "Job"} {
		if !kinds[want] {
			t.Errorf("stdout must carry the rendered %s, got kinds %v", want, kinds)
		}
	}
	// And the manifests really are the release's bytes, not the build's.
	if !strings.Contains(stdout, renderReleaseDigest) {
		t.Errorf("the rendered image must be the RELEASE digest %s", renderReleaseDigest)
	}
}

// manifestHead is the part of each document the stream check reads.
type manifestHead struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
}

// assertYAMLManifestStream fails the test unless s is a YAML document stream
// in which every document is a Kubernetes object. A stray prose line either
// breaks the stream's parse or becomes a scalar/foreign document, and both
// are reported with the offending text.
func assertYAMLManifestStream(t *testing.T, s string) []manifestHead {
	t.Helper()
	if strings.TrimSpace(s) == "" {
		t.Fatal("stdout is empty: expected the rendered manifests")
	}
	dec := yaml.NewDecoder(bytes.NewReader([]byte(s)))
	var docs []manifestHead
	for i := 0; ; i++ {
		var node yaml.Node
		err := dec.Decode(&node)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stdout is not a YAML stream (document %d): %v\nfirst lines:\n%s", i, err, firstLines(s, 5))
		}
		var head manifestHead
		if err := node.Decode(&head); err != nil || head.APIVersion == "" || head.Kind == "" || head.Metadata.Name == "" {
			t.Fatalf("stdout document %d is not a Kubernetes object (err=%v, head=%+v)\nfirst lines:\n%s", i, err, head, firstLines(s, 5))
		}
		docs = append(docs, head)
	}
	return docs
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
