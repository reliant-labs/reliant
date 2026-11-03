package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
)

// shellSvc is a test helper building a WorkloadEntity whose effective
// build is a ShellBuild (the single shell escape hatch). cwd/env are
// optional.
func shellSvc(name, image, cmd, cwd string, env map[string]string) WorkloadEntity {
	return WorkloadEntity{
		Name:  name,
		Image: image,
		Build: BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: cmd, Cwd: cwd, Env: env}},
	}
}

// TestKCLHasExternalBuildService_PositiveAndNegative pins the discovery
// helper runBuild uses to decide whether to invoke the external-build
// dispatcher at all. A KCL entity set without any ShellBuild services
// must report false so the dispatcher loop is skipped entirely (no
// state-dir creation, no log noise on projects that don't use the
// escape hatch).
func TestKCLHasExternalBuildService_PositiveAndNegative(t *testing.T) {
	// Positive: one service declares a ShellBuild.
	withCmd := &KCLEntities{
		Workloads: []WorkloadEntity{
			shellSvc("a", "a-img", "docker build .", "", nil),
			{Name: "b"},
		},
	}
	if !kclHasExternalBuildService(withCmd) {
		t.Error("kclHasExternalBuildService: want true when any service has a ShellBuild")
	}

	// Negative: no service declares a ShellBuild (both default to GoBuild).
	noCmd := &KCLEntities{
		Workloads: []WorkloadEntity{{Name: "a"}, {Name: "b"}},
	}
	if kclHasExternalBuildService(noCmd) {
		t.Error("kclHasExternalBuildService: want false when no service has a ShellBuild")
	}

	// Nil-safe: the runBuild path passes entities=nil for projects
	// without a rendered KCL set. Must not panic.
	if kclHasExternalBuildService(nil) {
		t.Error("kclHasExternalBuildService(nil): want false")
	}
}

// TestEffectiveBuildCmd_SingleSource pins the unified resolution: the
// effective ShellBuild's Cmd is the one shell source. A non-shell build
// (GoBuild default, compose/external with no build) returns "".
func TestEffectiveBuildCmd_SingleSource(t *testing.T) {
	cases := []struct {
		name string
		svc  WorkloadEntity
		want string
	}{
		{
			name: "ShellBuild cmd is the source",
			svc:  shellSvc("s", "img", "make image", "", nil),
			want: "make image",
		},
		{
			name: "GoBuild default returns empty",
			svc:  WorkloadEntity{Name: "s", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}},
			want: "",
		},
		{
			name: "prebuilt image with no build returns empty",
			svc:  clusterWL("s", "k3d-dev", "dev", func(w *WorkloadEntity) { w.Build = BuildConfigEntity{} }),
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.svc.EffectiveBuildCmd(); got != c.want {
				t.Errorf("EffectiveBuildCmd: got %q, want %q", got, c.want)
			}
		})
	}
}

// TestEffectiveBuildEnv_FromShell confirms the env-map resolution reads
// the effective ShellBuild's Env (the single source after unification).
func TestEffectiveBuildEnv_FromShell(t *testing.T) {
	svc := shellSvc("s", "img", "make image", "", map[string]string{"FOO": "bar"})
	env := svc.EffectiveBuildEnv()
	if env["FOO"] != "bar" {
		t.Errorf("Shell env: got %q, want bar", env["FOO"])
	}
	// A non-shell build has no shell env.
	if got := (WorkloadEntity{Name: "g", Runtime: RuntimeEntity{Type: RuntimeHost, Host: &HostRuntime{}}}).EffectiveBuildEnv(); got != nil {
		t.Errorf("non-shell build env: got %v, want nil", got)
	}
}

// TestParseKCLEntities_ShellBuildCwdEnv confirms a ShellBuild's cwd + env
// survive the JSON round-trip into ShellBuild.Cwd/Env and are surfaced by
// the Effective* accessors.
func TestParseKCLEntities_ShellBuildCwdEnv(t *testing.T) {
	js := `{"output":{"workloads":[{"name":"trader","kind":"service","image":"ghcr.io/x","runtime":{"type":"hosted"},"build":{"type":"shell","cmd":"docker build -t ${IMAGE}:${TAG} ${PROJECT_DIR}","cwd":"../sib","env":{"REGION":"iad"}},"spec":{"kind":"service"}}]}}`
	entities, err := parseKCLEntities([]byte(js))
	if err != nil {
		t.Fatalf("parseKCLEntities: %v", err)
	}
	svc := entities.FindWorkload("trader")
	if svc == nil {
		t.Fatal("trader not found")
	}
	if got := svc.EffectiveBuildCmd(); got != "docker build -t ${IMAGE}:${TAG} ${PROJECT_DIR}" {
		t.Errorf("EffectiveBuildCmd: got %q", got)
	}
	if got := svc.EffectiveBuildCwd(); got != "../sib" {
		t.Errorf("EffectiveBuildCwd: got %q, want ../sib", got)
	}
	if got := svc.EffectiveBuildEnv()["REGION"]; got != "iad" {
		t.Errorf("EffectiveBuildEnv[REGION]: got %q, want iad", got)
	}
}

// TestKCLHasExternalBuildService_DetectsShellOnAnyRuntime confirms the
// discovery helper sees a ShellBuild whatever runtime the workload binds
// (build and runtime are orthogonal): a hosted workload with a shell build
// is the kalshi-trader shape.
func TestKCLHasExternalBuildService_DetectsShellOnAnyRuntime(t *testing.T) {
	ext := &KCLEntities{
		Workloads: []WorkloadEntity{
			{
				Name:    "trader",
				Image:   "ghcr.io/x",
				Runtime: RuntimeEntity{Type: RuntimeHosted},
				Build:   BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "docker build ."}},
			},
		},
	}
	if !kclHasExternalBuildService(ext) {
		t.Error("want true when a service declares a ShellBuild")
	}
	got := externalBuildServices(ext)
	if len(got) != 1 || got[0].Name != "trader" {
		t.Errorf("externalBuildServices: got %v, want [trader]", got)
	}

	// A workload with no build (a prebuilt image) must NOT be detected:
	// nothing synthesizes a default build, so EffectiveBuildCmd is "".
	noBuild := &KCLEntities{
		Workloads: []WorkloadEntity{hostedWL("trader")},
	}
	if kclHasExternalBuildService(noBuild) {
		t.Error("want false when a workload declares no ShellBuild")
	}
}

// TestExternalBuildServices_FiltersShellBuilds confirms the dispatcher's
// filter returns only services whose effective build is a ShellBuild.
// Services that default to GoBuild flow through the regular Go-build path
// and must NOT appear in the external dispatcher's input set.
func TestExternalBuildServices_FiltersShellBuilds(t *testing.T) {
	entities := &KCLEntities{
		Workloads: []WorkloadEntity{
			shellSvc("edge", "edge-img", "docker build .", "", nil),
			{Name: "api"}, // GoBuild default
			shellSvc("daemon", "daemon-img", "go build && docker push", "", nil),
		},
	}
	got := externalBuildServices(entities)
	if len(got) != 2 {
		t.Fatalf("len: got %d, want 2", len(got))
	}
	names := map[string]bool{}
	for _, s := range got {
		names[s.Name] = true
	}
	if !names["edge"] || !names["daemon"] {
		t.Errorf("filter dropped a ShellBuild service: got %v", names)
	}
	if names["api"] {
		t.Error("filter included a service with no ShellBuild")
	}
}

// TestBuildExternalServices_WritesStateAndReturnsResults exercises the
// dispatcher end-to-end against a temp project dir. Asserts:
//
//   - Each declared external service produces one buildResult with
//     kind=="external".
//   - The state file lands at
//     .forge/state/build-<env>-<service>.json after a successful build.
//
// We can't easily inject a fake runner from this package (the runner
// indirection is package-private to buildtarget by design), so we run
// against a real `sh -c true` shell. CI runners always have /bin/sh,
// and `true` is a fast deterministic no-op that exits 0.
func TestBuildExternalServices_WritesStateAndReturnsResults(t *testing.T) {
	if testing.Short() {
		t.Skip("runs shell build commands as subprocesses; runs in task test")
	}
	projDir := t.TempDir()
	services := []WorkloadEntity{
		shellSvc("edge", "localhost:5051/edge-img", "true", "", nil), // cwd empty so no mkdir
	}
	opts := buildOptions{env: "dev", parallel: false}
	results := buildExternalServices(
		context.Background(),
		services,
		opts,
		"v1.2.3", // tag
		projDir,
	)
	if len(results) != 1 {
		t.Fatalf("results: got %d, want 1", len(results))
	}
	r := results[0]
	if r.err != nil {
		t.Errorf("err: got %v, want nil", r.err)
	}
	if r.kind != "external" {
		t.Errorf("kind: got %q, want external", r.kind)
	}
	if !strings.Contains(r.name, "edge") {
		t.Errorf("name: got %q, want a name containing 'edge'", r.name)
	}

	// State file should have landed at the canonical path.
	statePath := filepath.Join(projDir, ".forge", "state", "build-dev-edge.json")
	if _, err := os.Stat(statePath); err != nil {
		t.Errorf("state file at %s: %v", statePath, err)
	}
}

// TestBuildExternalServices_FailsWhenCwdMissing pins the gotcha-B
// contract: a service that IS in the current env (it reached the
// dispatcher) but whose build_cwd is missing must FAIL the build (a
// buildResult with err set), not skip. Skipping reported success while
// producing no image, so a following `forge env deploy` referenced an
// unpushed tag → ImagePullBackOff.
//
// Critically: a FAILED build must NOT write a state file either — that
// would still let a downstream `forge env deploy` pin a tag for an image
// that was never pushed.
func TestBuildExternalServices_FailsWhenCwdMissing(t *testing.T) {
	projDir := t.TempDir()
	services := []WorkloadEntity{
		shellSvc("edge", "localhost:5051/edge-img", "false", "missing-sibling", nil), // missing-cwd check short-circuits
	}
	opts := buildOptions{env: "dev", parallel: false}
	results := buildExternalServices(
		context.Background(),
		services,
		opts,
		"v1",
		projDir)
	if len(results) != 1 {
		t.Fatalf("results: got %d, want 1", len(results))
	}
	r := results[0]
	if r.err == nil {
		t.Fatal("err: got nil, want a failure for a missing build_cwd (skip-masquerading-as-success regression)")
	}
	if !strings.Contains(r.err.Error(), "missing-sibling") {
		t.Errorf("err: got %q, want it to name the missing path", r.err.Error())
	}
	if r.kind != "external" {
		t.Errorf("kind: got %q, want external (a real failure, not external-skip)", r.kind)
	}
	// Failed builds MUST NOT write a state file — that would let a
	// downstream `forge env deploy` pin a tag for an image that was never
	// pushed.
	statePath := filepath.Join(projDir, ".forge", "state", "build-dev-edge.json")
	if _, err := os.Stat(statePath); err == nil {
		t.Errorf("state file at %s exists; failed builds must not write state", statePath)
	}
}

// TestBuildExternalServices_RegistryMissOverwritesPriorDigest pins the
// "NO fallback to the previous build-state digest" half of the remote-built
// digest contract. A first build resolves the pushed ref to a digest and
// persists it. A SECOND build of the same env/service then has its registry
// query MISS (remote-built image not yet/never in the registry, or an
// unreachable registry). The miss must overwrite the persisted state with an
// EMPTY digest — never silently retain the prior build's digest, which would
// keep pinning deploy to a stale image (the wrong image or one the registry
// no longer has → ImagePullBackOff, surfacing as a deploy exit 1).
func TestBuildExternalServices_RegistryMissOverwritesPriorDigest(t *testing.T) {
	projDir := t.TempDir()

	const priorDigest = "sha256:" + "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	orig := externalImageDigestResolver
	t.Cleanup(func() { externalImageDigestResolver = orig })

	services := []WorkloadEntity{
		shellSvc("reliant-api-server", "reliant", "true", "", nil),
	}
	opts := buildOptions{env: "prod", parallel: false}

	// First build: registry resolves a digest, which gets persisted.
	externalImageDigestResolver = func(_ context.Context, _ string) (string, []string, error) {
		return priorDigest, []string{"linux/amd64"}, nil
	}
	if r := buildExternalServices(context.Background(), services, opts,
		"prod", projDir); len(r) != 1 || r[0].err != nil {
		t.Fatalf("first build: %+v", r)
	}
	if dst, err := ReadBuildState(projDir, "prod"); err != nil || dst == nil || dst.Digest != priorDigest {
		t.Fatalf("after first build: want digest %q, got dst=%+v err=%v", priorDigest, dst, err)
	}

	// Second build: registry query MISSES. The persisted digest MUST be
	// overwritten with empty — no fall-through to the prior build-state digest.
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		return "", nil, fmt.Errorf("not in registry: %s", ref)
	}
	if r := buildExternalServices(context.Background(), services, opts,
		"prod", projDir); len(r) != 1 || r[0].err != nil {
		t.Fatalf("second build: %+v", r)
	}

	// Deploy-readable aggregate: digest cleared, so deploy falls back to the tag.
	dst, err := ReadBuildState(projDir, "prod")
	if err != nil || dst == nil {
		t.Fatalf("ReadBuildState after miss: dst=%+v err=%v", dst, err)
	}
	if dst.Digest != "" {
		t.Errorf("deploy-aggregate digest after registry miss: got %q, want empty (no stale fallback)", dst.Digest)
	}
	// Per-service state too.
	st, err := buildtarget.ReadState(projDir, "prod", "reliant-api-server")
	if err != nil || st == nil {
		t.Fatalf("ReadState after miss: st=%+v err=%v", st, err)
	}
	if st.Digest != "" {
		t.Errorf("per-service digest after registry miss: got %q, want empty (no stale fallback)", st.Digest)
	}

	// And deploy resolves to the mutable tag, not the stale digest.
	digests, derr := resolveDeployImageDigests(projDir, "prod", false)
	if derr != nil {
		t.Fatalf("resolveDeployImageDigests: %v", derr)
	}
	if d := digests["reliant"]; d != "" {
		t.Errorf("reliant image digest after registry miss: got %q, want empty (deploy must use the tag)", d)
	}
}

// TestBuildTargetExternalFlag_Accepted confirms the --target flag
// accepts the new "external" literal alongside the legacy "all" and
// per-service values. Wires the user-facing entry point — if the
// flag is renamed or dropped, this test catches it before users hit
// "unknown flag value" from the CLI.
func TestBuildTargetExternalFlag_Accepted(t *testing.T) {
	cmd := newBuildCmd()
	if err := cmd.Flags().Parse([]string{"--target", "external"}); err != nil {
		t.Fatalf("parse --target external: %v", err)
	}
	got, err := cmd.Flags().GetString("target")
	if err != nil {
		t.Fatalf("GetString(target): %v", err)
	}
	if got != "external" {
		t.Errorf("target: got %q, want external", got)
	}
}

// The flag > cfg > runtime-host precedence that forge binds as the
// `target_arch` KCL input, which a ShellBuild reads via forge.target_arch().
//
// The runtime fallback is load-bearing and is the reason this is asserted on a
// resolver that NEVER returns empty: an empty arch reaches a rendered command
// as `--platform=linux/` (buildx rejects it) or, far worse, as `GOARCH=`, which
// Go reads as "unset" and silently builds for the host.
func TestResolveBuildArchForImage_Precedence(t *testing.T) {
	cases := []struct {
		name     string
		cfgArch  string
		flagArch string
		// We can't assert against runtime.GOARCH directly because the
		// fallback case's expectation is "anything non-empty"; the
		// test runs on whatever CI arch is current.
		wantEqualFlag    bool // when true, expect == flagArch
		wantEqualCfg     bool // when true, expect == cfgArch
		wantNonEmptyOnly bool // when true, just assert non-empty
	}{
		{name: "flag wins over cfg", cfgArch: "amd64", flagArch: "arm64", wantEqualFlag: true},
		{name: "cfg used when flag empty", cfgArch: "arm64", flagArch: "", wantEqualCfg: true},
		{name: "runtime fallback when both empty", cfgArch: "", flagArch: "", wantNonEmptyOnly: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveBuildArchForImage(c.cfgArch, c.flagArch)
			if c.wantEqualFlag && got != c.flagArch {
				t.Errorf("got %q, want flagArch %q", got, c.flagArch)
			}
			if c.wantEqualCfg && got != c.cfgArch {
				t.Errorf("got %q, want cfgArch %q", got, c.cfgArch)
			}
			if c.wantNonEmptyOnly && got == "" {
				t.Errorf("got empty; runtime fallback should never produce empty")
			}
		})
	}
}

// TestBuildExternalServices_CapturesDigest pins the external-build half of
// deploy-by-digest: when the pushed ref resolves to a content-addressed
// manifest digest, that digest lands in BOTH the per-service State file
// (forge project audit/doctor) AND the deploy-readable aggregate build-<env>.json
// (what resolveDeployImageTag reads) — so reliant/workspace-base get pinned
// to `<image>@sha256:...` instead of the mutable env tag. The resolver is
// faked so the test never shells out to docker; it also asserts the resolver
// was queried with the exact ${REGISTRY}/${IMAGE}:${TAG} ref the build_cmd
// pushed.
func TestBuildExternalServices_CapturesDigest(t *testing.T) {
	projDir := t.TempDir()

	const wantDigest = "sha256:" + "abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabc00"
	var gotRef string
	orig := externalImageDigestResolver
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		gotRef = ref
		return wantDigest, []string{"linux/amd64"}, nil
	}
	t.Cleanup(func() { externalImageDigestResolver = orig })

	services := []WorkloadEntity{
		shellSvc("reliant-api-server", "ghcr.io/reliant-labs/reliant", "true", "", nil),
	}
	opts := buildOptions{env: "staging", parallel: false}
	results := buildExternalServices(
		context.Background(), services, opts, "staging", projDir)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("results: %+v", results)
	}

	// Resolver must have been asked about the exact pushed ref.
	if want := "ghcr.io/reliant-labs/reliant:staging"; gotRef != want {
		t.Errorf("resolver ref: got %q, want %q", gotRef, want)
	}

	// Per-service State carries the digest.
	st, err := buildtarget.ReadState(projDir, "staging", "reliant-api-server")
	if err != nil || st == nil {
		t.Fatalf("ReadState: st=%+v err=%v", st, err)
	}
	if st.Digest != wantDigest {
		t.Errorf("per-service digest: got %q, want %q", st.Digest, wantDigest)
	}
	if len(st.Platforms) != 1 || st.Platforms[0] != "linux/amd64" {
		t.Errorf("per-service platforms: got %v", st.Platforms)
	}

	// Deploy-readable aggregate carries the digest too — this is the file
	// resolveDeployImageTag actually consumes.
	dst, err := ReadBuildState(projDir, "staging")
	if err != nil || dst == nil {
		t.Fatalf("ReadBuildState: dst=%+v err=%v", dst, err)
	}
	if dst.Digest != wantDigest {
		t.Errorf("deploy-aggregate digest: got %q, want %q", dst.Digest, wantDigest)
	}

	// resolveDeployImageTag returns the mutable tag (the env-wide image_tag
	// fallback + the External/Compose ${TAG}); the digest is NOT stamped here
	// anymore.
	ref, plain, _, rerr := resolveDeployImageTag(context.Background(), projDir, "staging", "", false)
	if rerr != nil {
		t.Fatalf("resolveDeployImageTag: %v", rerr)
	}
	if ref != "staging" || plain != "staging" {
		t.Errorf("resolved imageRef=%q plainTag=%q, want both the mutable tag %q", ref, plain, "staging")
	}

	// resolveDeployImageDigests pins the per-IMAGE digest — the reliant image
	// resolves to ITS captured digest (from the per-service external state).
	digests, derr := resolveDeployImageDigests(projDir, "staging", false)
	if derr != nil {
		t.Fatalf("resolveDeployImageDigests: %v", derr)
	}
	// The pin map is keyed by the declared REFERENCE, host included — the same
	// key kcl/lib/images.k looks an image up under.
	if digests["ghcr.io/reliant-labs/reliant"] != wantDigest {
		t.Errorf("reliant image digest: got %q, want %q", digests["ghcr.io/reliant-labs/reliant"], wantDigest)
	}
}

// TestBuildExternalServices_NoDigestSafeFallback pins the safety contract:
// when the pushed ref has no resolvable digest (local-only ref — the e2e
// workspace-base/reliant case — or an unreachable registry), the build still
// succeeds and records NO digest, so deploy falls back to the mutable tag
// exactly as before. A digest lookup failure must never break the build or
// the local-registry/e2e path.
func TestBuildExternalServices_NoDigestSafeFallback(t *testing.T) {
	projDir := t.TempDir()

	orig := externalImageDigestResolver
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		return "", nil, fmt.Errorf("no registry manifest for %s (local-only)", ref)
	}
	t.Cleanup(func() { externalImageDigestResolver = orig })

	services := []WorkloadEntity{
		// A bare image: no registry host, so the pushed ref is local-only and
		// the digest lookup cannot resolve it. The build must still succeed.
		shellSvc("workspace-base", "workspace-base", "true", "", nil),
	}
	opts := buildOptions{env: "e2e", parallel: false}
	results := buildExternalServices(
		context.Background(), services, opts, "e2e", projDir)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("build must still succeed on a digest miss: %+v", results)
	}

	st, err := buildtarget.ReadState(projDir, "e2e", "workspace-base")
	if err != nil || st == nil {
		t.Fatalf("ReadState: st=%+v err=%v", st, err)
	}
	if st.Digest != "" {
		t.Errorf("per-service digest: got %q, want empty (safe fallback)", st.Digest)
	}

	dst, err := ReadBuildState(projDir, "e2e")
	if err != nil || dst == nil {
		t.Fatalf("ReadBuildState: dst=%+v err=%v", dst, err)
	}
	if dst.Digest != "" {
		t.Errorf("deploy-aggregate digest: got %q, want empty (safe fallback)", dst.Digest)
	}

	// Deploy resolves to the plain mutable tag, not a digest.
	ref, _, _, rerr := resolveDeployImageTag(context.Background(), projDir, "e2e", "", false)
	if rerr != nil {
		t.Fatalf("resolveDeployImageTag: %v", rerr)
	}
	if ref != "e2e" {
		t.Errorf("resolved imageRef: got %q, want the tag %q (no digest pinned)", ref, "e2e")
	}
}

// TestBuildExternalServices_ResolvesBareHostedImageAgainstPushBase pins the
// ADR-0003 F1 resolution on the SHELL side.
//
// A hosted workload declares its image BARE (`echo`) — the default and
// correct form, because the control plane admits exactly one registry subtree
// and so the author does not transcribe it. forge resolves that to
// `<push base>/echo`, and the ShellBuild's rendered `cmd` pushes there,
// because the reference was composed in the same render.
//
// Before this, the dispatcher read the UNRESOLVED `svc.Image` for both the
// digest lookup and the state it wrote. So the registry query ran against the
// bare name — which names no host, resolves nothing, and misses every time —
// and the build state recorded `e2eh/<run>/echo` with `digest: ""`. The push
// itself was correct; what was wrong was forge's record OF it, which left a
// release cut with nothing to pin and a deploy on the mutable tag.
func TestBuildExternalServices_ResolvesBareHostedImageAgainstPushBase(t *testing.T) {
	projDir := t.TempDir()

	const (
		pushBase = "registry.example.com/acme/shop"
		digest   = "sha256:" + "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	)

	// The resolver stands in for the registry. It records the ref it was
	// asked about, which IS the assertion: a lookup against the bare name is
	// the defect.
	var askedFor string
	orig := externalImageDigestResolver
	t.Cleanup(func() { externalImageDigestResolver = orig })
	externalImageDigestResolver = func(_ context.Context, ref string) (string, []string, error) {
		askedFor = ref
		if ref != pushBase+"/echo:e2e" {
			return "", nil, fmt.Errorf("not in registry: %s", ref)
		}
		return digest, []string{"linux/amd64"}, nil
	}

	svc := shellSvc("echo", "echo", "true", "", nil)
	svc.Runtime = RuntimeEntity{Type: RuntimeHosted}
	opts := buildOptions{env: "e2eh", pushPlan: pushPlan{env: "e2eh", push: true, pushBase: pushBase}}

	results := buildExternalServices(context.Background(), []WorkloadEntity{svc}, opts, "e2e", projDir)
	if len(results) != 1 || results[0].err != nil {
		t.Fatalf("build: %+v", results)
	}

	if want := pushBase + "/echo:e2e"; askedFor != want {
		t.Errorf("digest lookup ref: got %q, want %q (the RESOLVED ref the cmd pushed)", askedFor, want)
	}

	st, err := buildtarget.ReadState(projDir, "e2eh", "echo")
	if err != nil || st == nil {
		t.Fatalf("ReadState: st=%+v err=%v", st, err)
	}
	if st.Image != pushBase+"/echo" {
		t.Errorf("per-service image: got %q, want the resolved %q", st.Image, pushBase+"/echo")
	}
	if st.Digest != digest {
		t.Errorf("per-service digest: got %q, want %q", st.Digest, digest)
	}

	dst, err := ReadBuildState(projDir, "e2eh")
	if err != nil || dst == nil {
		t.Fatalf("ReadBuildState: dst=%+v err=%v", dst, err)
	}
	if dst.Image != pushBase+"/echo" {
		t.Errorf("deploy-aggregate image: got %q, want the resolved %q", dst.Image, pushBase+"/echo")
	}
	if dst.Digest != digest {
		t.Errorf("deploy-aggregate digest: got %q, want %q (a cut has nothing to pin without it)", dst.Digest, digest)
	}
}
