package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/pkg/release"
)

// The regression these tests pin, found by control-plane's hosted e2e:
//
//	forge env build <env> --target echo --tag t1 --push
//
// printed `Tag: t1 (explicit --tag flag)` and then ran the ShellBuild with
// ${TAG}=latest — the env's image_tag — and recorded `latest` in build state.
// Every lane settled the tag its own way, and the ShellBuild lane put the
// env's tag above the flag.
//
// So these tests assert AGREEMENT, not one number: the printed Tag line, the
// ref whose digest is captured, the recorded build state and the release ledger
// must all name the same tag. Each one is an observation of what `forge build`
// actually did.
//
// The tag is no longer observed by reading it back out of the command. A cmd is
// plain KCL run verbatim, so in a real project the tag reaches it through the
// render (forge binds it as the `image_tag` input BEFORE rendering, which is
// what makes the rendered cmd carry the right one) — and these fixtures are
// static JSON with no render to bind. What they can still observe, and what
// actually decides whether a deploy finds the image, is the ref forge looked up
// a digest for and the ref it recorded. Those are asserted per lane below;
// image_tag_pin_build_test.go covers agreement through a REAL render.

// tagPrecedenceRegistry is the registry the fixture's cluster target declares.
const tagPrecedenceRegistry = "registry.example/prod"

// tagPrecedenceFixture is an env whose image_tag is `latest` (the default the
// e2e hit), with:
//
//   - echo: an unpinned ShellBuild that records THAT it ran;
//   - gw:   an unpinned DockerBuild;
//   - api:  a ShellBuild whose declared image PINS a tag (`reliant:e2e`).
const tagPrecedenceFixture = `{
  "output": {
    "image_tag": "latest",
    "workloads": [
      {
        "name": "echo", "kind": "service", "image": "registry.example/prod/echo", "build_image": "registry.example/prod/echo",
        "build": {"type": "shell", "cmd": "echo ran > ran-echo.txt"},
        "runtime": {"type": "cluster", "cluster": "c", "namespace": "n", "platform": "amd64"},
        "spec": {"kind": "service"}
      },
      {
        "name": "gw", "kind": "service", "image": "registry.example/prod/gw", "build_image": "registry.example/prod/gw",
        "build": {"type": "docker", "dockerfile": "Dockerfile"},
        "runtime": {"type": "cluster", "cluster": "c", "namespace": "n", "platform": "amd64"},
        "spec": {"kind": "service"}
      },
      {
        "name": "api", "kind": "service", "image": "registry.example/prod/reliant", "build_image": "registry.example/prod/reliant:e2e",
        "build": {"type": "shell", "cmd": "echo ran > ran-api.txt"},
        "runtime": {"type": "cluster", "cluster": "c", "namespace": "n", "platform": "amd64"},
        "spec": {"kind": "service"}
      }
    ]
  }
}`

// digestOf is the fake registry's answer for a ref: deterministic and
// different per ref, so a digest recorded for the WRONG ref is caught.
func digestOf(ref string) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(ref)))
}

// fakeRegistry answers every digest lookup as if ref had been pushed, and
// returns the refs that were looked up.
func fakeRegistry(t *testing.T) *[]string {
	t.Helper()
	var inspected []string
	prev := imagetoolsInspect
	t.Cleanup(func() { imagetoolsInspect = prev })
	imagetoolsInspect = func(_ context.Context, ref, format string) ([]byte, error) {
		if strings.Contains(format, "Manifest.Manifests") {
			return []byte("linux/amd64\n"), nil
		}
		inspected = append(inspected, ref)
		return []byte(digestOf(ref)), nil
	}
	return &inspected
}

// fakeDocker puts a `docker` on PATH that records each invocation's argv, one
// line per call, and succeeds. Returns the log path.
func fakeDocker(t *testing.T) string {
	t.Helper()
	requirePOSIXFake(t, "docker")
	bin := t.TempDir()
	logPath := filepath.Join(bin, "docker.log")
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

func tagPrecedenceBuild(target, tag, rel string) buildOptions {
	return buildOptions{
		env: "prod", buildTarget: target, tag: tag, release: rel,
		push: true, buildDocker: true, outputDir: "bin", skipGenerate: true,
	}
}

func readTrim(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}

// assertRecorded checks the build state service recorded names image at tag,
// and that its digest is the registry's answer for the ref carrying THAT tag.
func assertRecorded(t *testing.T, dir, service, image, tag string) {
	t.Helper()
	st, err := buildtarget.ReadState(dir, "prod", service)
	if err != nil || st == nil {
		t.Fatalf("%s: no build state recorded (err %v) — deploy and the release ledger cannot see this build", image, err)
	}
	if st.Tag != tag {
		t.Errorf("%s: recorded tag %q, want %q", image, st.Tag, tag)
	}
	// The recorded Image is the full REPOSITORY the workload declared — host
	// included, because that is the ledger key and the push target.
	wantImage := tagPrecedenceRegistry + "/" + image
	if st.Image != wantImage {
		t.Errorf("%s: recorded image %q, want %q (the deploy and ledger key)", service, st.Image, wantImage)
	}
	wantRef := wantImage + ":" + tag
	if st.Digest != digestOf(wantRef) {
		t.Errorf("%s: recorded digest %q is not the digest of the pushed ref %s (%s)", image, st.Digest, wantRef, digestOf(wantRef))
	}
}

// TestBuildTag_ExplicitTagWinsForShellBuild is the e2e's exact command.
func TestBuildTag_ExplicitTagWinsForShellBuild(t *testing.T) {
	dir := planProject(t, tagPrecedenceFixture)
	inspected := fakeRegistry(t)

	var err error
	out := captureStdout(t, func() { err = runBuild(context.Background(), tagPrecedenceBuild("echo", "t1", "")) })
	if err != nil {
		t.Fatalf("runBuild: %v\n%s", err, out)
	}

	if !strings.Contains(out, "Tag:      t1 (explicit --tag flag)") {
		t.Errorf("printed Tag line should name t1 from --tag:\n%s", out)
	}
	if _, serr := os.Stat(filepath.Join(dir, "ran-echo.txt")); serr != nil {
		t.Errorf("the ShellBuild command did not run: %v", serr)
	}
	if len(*inspected) != 1 || (*inspected)[0] != tagPrecedenceRegistry+"/echo:t1" {
		t.Errorf("digest captured for %v, want exactly the pushed ref %s/echo:t1", *inspected, tagPrecedenceRegistry)
	}
	assertRecorded(t, dir, "echo", "echo", "t1")
	if st, _ := ReadBuildState(dir, "prod"); st == nil || st.Tag != "t1" {
		t.Errorf("deploy-readable build state = %+v, want tag t1", st)
	}
}

// TestBuildTag_ExplicitTagWinsForDockerBuild: the same agreement through the
// DockerBuild lane — the tag docker builds and pushes, and the one recorded.
func TestBuildTag_ExplicitTagWinsForDockerBuild(t *testing.T) {
	dir := planProject(t, tagPrecedenceFixture)
	fakeRegistry(t)
	dockerLog := fakeDocker(t)

	var err error
	out := captureStdout(t, func() { err = runBuild(context.Background(), tagPrecedenceBuild("gw", "t1", "")) })
	if err != nil {
		t.Fatalf("runBuild: %v\n%s", err, out)
	}

	calls := readTrim(t, dockerLog)
	if !strings.Contains(calls, "-t "+tagPrecedenceRegistry+"/gw:t1") {
		t.Errorf("docker build did not tag gw:t1:\n%s", calls)
	}
	if !strings.Contains(calls, "push "+tagPrecedenceRegistry+"/gw:t1") {
		t.Errorf("docker did not push gw:t1:\n%s", calls)
	}
	assertRecorded(t, dir, "gw", "gw", "t1")
}

// TestBuildTag_ExplicitTagOnPinnedWorkloadIsRefused: `api` pins reliant:e2e,
// so the deploy pulls reliant:e2e. Building reliant:t1 would push a ref
// nothing deploys, so the build is refused before anything runs, naming the
// workload and both tags.
func TestBuildTag_ExplicitTagOnPinnedWorkloadIsRefused(t *testing.T) {
	dir := planProject(t, tagPrecedenceFixture)
	fakeRegistry(t)
	fakeDocker(t)

	for _, target := range []string{"api", "all"} {
		var err error
		out := captureStdout(t, func() { err = runBuild(context.Background(), tagPrecedenceBuild(target, "t1", "")) })
		if err == nil {
			t.Fatalf("--target %s --tag t1 over a pinned workload: want a refusal, got success:\n%s", target, out)
		}
		for _, want := range []string{`"api"`, `"t1"`, `"e2e"`, "reliant:e2e"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("--target %s: refusal must name %s: %v", target, want, err)
			}
		}
		for _, svc := range []string{"echo", "api"} {
			if _, serr := os.Stat(filepath.Join(dir, "ran-"+svc+".txt")); serr == nil {
				t.Errorf("--target %s: %s built before the refusal — refuse up front", target, svc)
			}
		}
	}
}

// TestBuildTag_NoFlagUsesPinThenEnvTag: without --tag, a pinned workload
// builds its pin and an unpinned one the env's image_tag.
func TestBuildTag_NoFlagUsesPinThenEnvTag(t *testing.T) {
	dir := planProject(t, tagPrecedenceFixture)
	fakeRegistry(t)
	fakeDocker(t)

	var err error
	out := captureStdout(t, func() { err = runBuild(context.Background(), tagPrecedenceBuild("all", "", "")) })
	if err != nil {
		t.Fatalf("runBuild: %v\n%s", err, out)
	}
	// The pinned workload records its pin; the unpinned one the env's tag.
	// assertRecorded also checks the digest is the one for THAT ref, so a
	// build that pushed one tag and recorded another cannot pass.
	assertRecorded(t, dir, "api", "reliant", "e2e")
	assertRecorded(t, dir, "echo", "echo", "latest")
	assertRecorded(t, dir, "gw", "gw", "latest")
}

// TestBuildTag_ReleaseWinsAndReachesTheLedger: a release version beats the
// pin and the env tag, and the ledger records the digest of exactly the ref
// each build pushed under it.
func TestBuildTag_ReleaseWinsAndReachesTheLedger(t *testing.T) {
	if testing.Short() {
		t.Skip("drives a fake docker and registry through shell subprocesses; runs in task test")
	}
	dir := useTestLedger(t, planProject(t, tagPrecedenceFixture))
	fakeRegistry(t)
	fakeDocker(t)

	var err error
	out := captureStdout(t, func() { err = runBuild(context.Background(), tagPrecedenceBuild("all", "", "v2.0.0")) })
	if err != nil {
		t.Fatalf("runBuild --release: %v\n%s", err, out)
	}
	rel, err := testGetRelease(t, dir, "v2.0.0")
	if err != nil || rel == nil {
		t.Fatalf("read release ledger: %v", err)
	}
	for service, image := range map[string]string{"echo": "echo", "gw": "gw", "api": "reliant"} {
		assertRecorded(t, dir, service, image, "v2.0.0")
		// The ledger is keyed by the declared REPOSITORY, host included.
		art, ok := rel.Artifacts[tagPrecedenceRegistry+"/"+image]
		if !ok {
			t.Errorf("release ledger has no %s/%s artifact", tagPrecedenceRegistry, image)
			continue
		}
		if want := digestOf(tagPrecedenceRegistry + "/" + image + ":v2.0.0"); art.Digests[release.SharedVariant] != want {
			t.Errorf("ledger %s digest %q, want the digest of %s:v2.0.0", image, art.Digests[release.SharedVariant], image)
		}
	}
}

// TestBuildTagFor_Precedence pins the one ordering every lane applies.
func TestBuildTagFor_Precedence(t *testing.T) {
	cases := []struct {
		name           string
		opts           buildOptions
		pin, buildWide string
		want           string
	}{
		{"release beats everything", buildOptions{release: "v2", tag: "v2"}, "e2e", "latest", "v2"},
		{"--tag beats the build-wide tag", buildOptions{tag: "t1"}, "", "latest", "t1"},
		{"--tag equal to the pin", buildOptions{tag: "e2e"}, "e2e", "latest", "e2e"},
		{"pin beats the build-wide tag", buildOptions{}, "e2e", "latest", "e2e"},
		{"build-wide tag last", buildOptions{}, "", "latest", "latest"},
	}
	for _, c := range cases {
		if got := buildTagFor(c.opts, c.pin, c.buildWide); got != c.want {
			t.Errorf("%s: buildTagFor = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestCheckExplicitTagAgainstPins_Edges: what counts as a conflict.
func TestCheckExplicitTagAgainstPins_Edges(t *testing.T) {
	goPinned := WorkloadEntity{Name: "admin", Image: "cp", BuildImage: "cp:e2e",
		Build: BuildConfigEntity{Type: "go", Go: &GoBuild{Cmd: "./cmd/cp"}}}
	shellPinned := WorkloadEntity{Name: "api", Image: "reliant", BuildImage: "reliant:e2e",
		Build: BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "true"}}}
	ents := &KCLEntities{Workloads: []WorkloadEntity{goPinned, shellPinned}}

	if err := checkExplicitTagAgainstPins(ents, buildOptions{tag: "e2e"}, "cp", true); err != nil {
		t.Errorf("--tag equal to every pin is no conflict: %v", err)
	}
	if err := checkExplicitTagAgainstPins(ents, buildOptions{tag: "t1", release: "t1"}, "cp", true); err != nil {
		t.Errorf("a release owns the tag (validateReleaseFlags); not a pin conflict: %v", err)
	}
	onlyGo := &KCLEntities{Workloads: []WorkloadEntity{goPinned}}
	if err := checkExplicitTagAgainstPins(onlyGo, buildOptions{tag: "t1"}, "cp", false); err != nil {
		t.Errorf("a pinned GoBuild whose project image is not built is no conflict: %v", err)
	}
	err := checkExplicitTagAgainstPins(onlyGo, buildOptions{tag: "t1", env: "prod"}, "cp", true)
	if err == nil || !strings.Contains(err.Error(), `"admin"`) || !strings.Contains(err.Error(), "cp:e2e") {
		t.Errorf("a pinned project image that IS built conflicts with --tag, naming the workload: %v", err)
	}
}
