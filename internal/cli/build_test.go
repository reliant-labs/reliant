package cli

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
)

// (TestMergeCoverageProfiles_MultipleDirs was removed with `forge test`.
// mergeCoverageProfiles existed to stitch together the per-directory
// coverage.out files that command's parallel per-service fan-out produced.
// Coverage is now `task coverage`, a single `go test -coverprofile ./...` over
// the whole module, so there is one profile and nothing to merge.)

// TestCountTagsHelper ensures the docker-build tag counter handles the
// canonical `-t a -t b` shape and ignores other flags.
func TestCountTagsHelper(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"empty", nil, 0},
		{"one tag", []string{"build", "-t", "foo:latest"}, 1},
		{"three tags", []string{"build", "-t", "a", "-t", "b", "-t", "c"}, 3},
		{"unrelated -f flag", []string{"build", "-t", "a", "-f", "Dockerfile"}, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := countTags(c.args); got != c.want {
				t.Errorf("want %d, got %d", c.want, got)
			}
		})
	}
}

// TestBuildPushFlagRegistered confirms the --push flag is wired into
// the build command and implies --docker at parse time.
// TestValidateReleaseFlags_RequiresEnv pins Fix 2: `forge env build --release
// <ver>` WITHOUT --env is rejected up front with an actionable message,
// because the release image SET (project images + per-env external
// build_cmd images like reliant/workspace-base) is only discoverable from
// deploy/kcl/<env>/main.k. With --env (or without --release) it passes.
func TestValidateReleaseFlags_RequiresEnv(t *testing.T) {
	// --release without --env: error, and the message must steer the user
	// to --env (not just say "missing flag").
	err := validateReleaseFlags(buildOptions{release: "v1.0.0"})
	if err == nil {
		t.Fatal("--release without --env must error")
	}
	for _, want := range []string{"environment argument", "--release", "build_cmd"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q should mention %q", err.Error(), want)
		}
	}

	// --release WITH --env: allowed.
	if err := validateReleaseFlags(buildOptions{release: "v1.0.0", env: "prod"}); err != nil {
		t.Errorf("--release with --env must be allowed, got %v", err)
	}

	// No --release: --env optional, no error.
	if err := validateReleaseFlags(buildOptions{}); err != nil {
		t.Errorf("a non-release build must not require --env, got %v", err)
	}
}

func TestBuildPushFlagRegistered(t *testing.T) {
	cmd := newBuildCmd()
	f := cmd.Flags().Lookup("push")
	if f == nil {
		t.Fatal("--push flag not registered on build command")
	}
	if f.DefValue != "false" {
		t.Errorf("--push default = %q, want false", f.DefValue)
	}
}

func TestBuildDebugFlagExists(t *testing.T) {
	cmd := newBuildCmd()

	f := cmd.Flags().Lookup("debug")
	if f == nil {
		t.Fatal("--debug flag not registered on build command")
	}

	if f.DefValue != "false" {
		t.Errorf("--debug default = %q, want %q", f.DefValue, "false")
	}
}

func TestBuildDebugFlagParsesTrue(t *testing.T) {
	cmd := newBuildCmd()

	if err := cmd.Flags().Parse([]string{"--debug"}); err != nil {
		t.Fatalf("failed to parse --debug: %v", err)
	}

	val, err := cmd.Flags().GetBool("debug")
	if err != nil {
		t.Fatalf("GetBool(\"debug\") error: %v", err)
	}
	if !val {
		t.Error("expected --debug to be true after parsing --debug")
	}
}

func TestBuildDebugFlagDefaultIsFalse(t *testing.T) {
	cmd := newBuildCmd()

	// Parse with no flags — debug should remain false.
	if err := cmd.Flags().Parse([]string{}); err != nil {
		t.Fatalf("failed to parse empty args: %v", err)
	}

	val, err := cmd.Flags().GetBool("debug")
	if err != nil {
		t.Fatalf("GetBool(\"debug\") error: %v", err)
	}
	if val {
		t.Error("expected --debug to default to false")
	}
}

// TestK3dMirrorRepositories covers the k3d-aware mirror tag: a
// localhost:<port> REPOSITORY also gets the same path under
// registry.localhost:<port> (the in-cluster pull reference); every other host
// gets none. The path is kept, so two images that differ only in their org
// path stay distinct after the swap.
func TestK3dMirrorRepositories(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"a bare name has no host, so no mirror", "api", nil},
		{
			name: "localhost mirrors, path kept",
			in:   "localhost:5051/api",
			want: []string{"registry.localhost:5051/api"},
		},
		{
			name: "a multi-segment path is kept whole",
			in:   "localhost:5050/acme/api",
			want: []string{"registry.localhost:5050/acme/api"},
		},
		{
			name: "a real registry gets no mirror",
			in:   "ghcr.io/acme/api",
			want: nil,
		},
		{
			name: "127.0.0.1 is NOT auto-mirrored (only literal localhost)",
			in:   "127.0.0.1:5051/api",
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := k3dMirrorRepositories(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("k3dMirrorRepositories(%q) = %v, want %v", c.in, got, c.want)
			}
			for i, v := range got {
				if v != c.want[i] {
					t.Errorf("k3dMirrorRepositories(%q)[%d] = %q, want %q", c.in, i, v, c.want[i])
				}
			}
		})
	}
}

func TestBuildAllFlagsRegistered(t *testing.T) {
	cmd := newBuildCmd()

	expected := []struct {
		name     string
		defValue string
	}{
		{"output", "bin"},
		{"target", "all"},
		{"parallel", "true"},
		{"docker", "false"},
		{"debug", "false"},
		{"target-arch", ""},
	}

	for _, tt := range expected {
		f := cmd.Flags().Lookup(tt.name)
		if f == nil {
			t.Errorf("flag --%s not registered", tt.name)
			continue
		}
		if f.DefValue != tt.defValue {
			t.Errorf("flag --%s default = %q, want %q", tt.name, f.DefValue, tt.defValue)
		}
	}
}

// TestResolveBuildArch covers the three branches of the resolveBuildArch
// helper: host matches target → empty (no cross-compile), host differs
// from target → resolved arch, explicit flag overrides the cfg default.
//
// We use runtime.GOARCH for the "host matches" cases so the test is
// portable across CI archs (both arm64 macOS and amd64 Linux runners).
// We use a guaranteed-mismatch string ("xyz") for the "host differs"
// cases — any non-matching arch token triggers the cross-compile branch.
func TestResolveBuildArch(t *testing.T) {
	otherArch := "amd64"
	if runtime.GOARCH == "amd64" {
		otherArch = "arm64"
	}

	cases := []struct {
		name      string
		cfgArch   string
		flagArch  string
		dockerCtx bool
		want      string
	}{
		{
			name:      "plain go build, no target → host arch (empty)",
			cfgArch:   "",
			flagArch:  "",
			dockerCtx: false,
			want:      "",
		},
		{
			name:      "docker build, no target → amd64 default",
			cfgArch:   "",
			flagArch:  "",
			dockerCtx: true,
			want: func() string {
				if runtime.GOARCH == "amd64" {
					return ""
				}
				return "amd64"
			}(),
		},
		{
			name:      "docker build with cfg arch differs from host → cross-compile",
			cfgArch:   otherArch,
			flagArch:  "",
			dockerCtx: true,
			want:      otherArch,
		},
		{
			name:      "host matches cfg target → empty",
			cfgArch:   runtime.GOARCH,
			flagArch:  "",
			dockerCtx: true,
			want:      "",
		},
		{
			name:      "explicit flag overrides cfg",
			cfgArch:   runtime.GOARCH, // would otherwise match host → empty
			flagArch:  otherArch,
			dockerCtx: false,
			want:      otherArch,
		},
		{
			name:      "flag matching host returns empty (no-op cross-compile)",
			cfgArch:   "",
			flagArch:  runtime.GOARCH,
			dockerCtx: false,
			want:      "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveBuildArch(c.cfgArch, c.flagArch, c.dockerCtx)
			if got != c.want {
				t.Errorf("resolveBuildArch(cfg=%q, flag=%q, docker=%v) = %q, want %q",
					c.cfgArch, c.flagArch, c.dockerCtx, got, c.want)
			}
		})
	}
}

// TestResolveBuildArchForImage locks the COPY-pattern image arch resolution:
// it NEVER returns "" (the caller always pairs it with GOOS=linux), precedence
// is flag > per-env platform > host arch, and an UNSET platform tracks the host
// (so a local arm64 build produces a linux/arm64 image+binary, not a native
// darwin/arm64 binary nor a silently cross-built amd64 image).
func TestResolveBuildArchForImage(t *testing.T) {
	host := runtime.GOARCH
	cases := []struct {
		name              string
		cfgArch, flagArch string
		want              string
	}{
		{"unset tracks host arch (local k3d)", "", "", host},
		{"per-env platform wins over host", "amd64", "", "amd64"},
		{"flag overrides per-env platform", "amd64", "arm64", "arm64"},
		{"flag wins when platform unset", "", "arm64", "arm64"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := resolveBuildArchForImage(c.cfgArch, c.flagArch)
			if got != c.want {
				t.Errorf("resolveBuildArchForImage(cfg=%q, flag=%q) = %q, want %q",
					c.cfgArch, c.flagArch, got, c.want)
			}
			if got == "" {
				t.Error("resolveBuildArchForImage must never return \"\" (caller pairs it with GOOS=linux)")
			}
		})
	}
}

// Note: TestHostDevTargetServices was removed when services[].dev_target
// moved to the KCL layer in feat/kcl-orchestration. The replacement
// host-mode filter reads rendered KCL via [hostServicesFromKCL] — see
// build_kcl_test.go for the KCL-side equivalent coverage.

// TestResolveBuildContext covers the three branches of the helper that
// normalises a forge.yaml `docker.build_contexts` value into the string
// `docker buildx --build-context name=…` expects: scheme passthrough,
// absolute-path passthrough, and project-root-relative resolution.
//
// Scheme detection is `strings.Contains(value, "://")` — wide on
// purpose so any buildkit-supported scheme (`docker-image://`,
// `oci-layout://`, `https://`, future additions) passes through
// without forge needing a per-scheme allowlist.
func TestResolveBuildContext(t *testing.T) {
	const root = "/tmp/proj"
	// Absolute on the host OS: "/abs/..." is only ROOTED on Windows (no
	// volume), so filepath.IsAbs rejects it there and the case would test
	// the relative branch instead.
	absShared := filepath.Join(t.TempDir(), "shared")
	cases := []struct {
		name  string
		value string
		root  string
		want  string
	}{
		{
			name:  "docker-image scheme passes through",
			value: "docker-image://my-base:latest",
			root:  root,
			want:  "docker-image://my-base:latest",
		},
		{
			name:  "oci-layout scheme passes through",
			value: "oci-layout://./layout",
			root:  root,
			want:  "oci-layout://./layout",
		},
		{
			name:  "https scheme passes through",
			value: "https://example.com/ctx.tar",
			root:  root,
			want:  "https://example.com/ctx.tar",
		},
		{
			name:  "absolute path passes through",
			value: absShared,
			root:  root,
			want:  absShared,
		},
		{
			name:  "relative path resolves against project root",
			value: "../shared-libs",
			root:  root,
			want:  filepath.Join(root, "../shared-libs"),
		},
		{
			name:  "dot-prefixed relative path resolves",
			value: "./subdir/extra",
			root:  root,
			want:  filepath.Join(root, "./subdir/extra"),
		},
		{
			name:  "empty project root defaults to cwd-relative",
			value: "../sibling",
			root:  "",
			want:  filepath.Join(".", "../sibling"),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveBuildContext(c.value, c.root); got != c.want {
				t.Errorf("resolveBuildContext(%q, %q) = %q, want %q",
					c.value, c.root, got, c.want)
			}
		})
	}
}

// TestAppendBuildContexts verifies the helper that injects the
// `--build-context name=value` pairs into a docker buildx arg list:
//
//   - An empty BuildContexts map is a no-op (existing projects unchanged).
//   - Entries appear in lexicographic key order so cache keys + arg
//     ordering are deterministic across runs.
//   - Local paths resolve against the project root.
//   - Scheme-bearing values pass through unchanged.
func TestAppendBuildContexts(t *testing.T) {
	t.Run("no-op when empty", func(t *testing.T) {
		cfg := &config.ProjectConfig{}
		args := []string{"build", "-t", "foo:latest"}
		got := appendBuildContexts(args, cfg, "/tmp/proj")
		if len(got) != len(args) {
			t.Errorf("expected no-op, got %d args (started with %d): %v",
				len(got), len(args), got)
		}
	})

	t.Run("deterministic order, path resolution, scheme passthrough", func(t *testing.T) {
		// Host-absolute, for the reason TestResolveBuildContext gives.
		absCtx := filepath.Join(t.TempDir(), "already-absolute")
		cfg := &config.ProjectConfig{
			Docker: config.DockerConfig{
				BuildContexts: map[string]config.BuildContext{
					"shared": {Path: "../shared-libs"},
					"base":   {Path: "docker-image://my-base:latest"},
					"abs":    {Path: absCtx},
				},
			},
		}
		args := appendBuildContexts(nil, cfg, "/tmp/proj")

		// 3 contexts × 2 args ("--build-context", "k=v") = 6
		if len(args) != 6 {
			t.Fatalf("want 6 args, got %d: %v", len(args), args)
		}

		// Lexicographic order: abs < base < shared.
		want := []string{
			"--build-context", "abs=" + absCtx,
			"--build-context", "base=docker-image://my-base:latest",
			"--build-context", "shared=" + filepath.Join("/tmp/proj", "../shared-libs"),
		}
		for i, w := range want {
			if args[i] != w {
				t.Errorf("args[%d] = %q, want %q (full args: %v)", i, args[i], w, args)
			}
		}
	})
}

// TestProjectGoBuildTarget_RawProjectName verifies the env-less fallback
// target points at cmd/<raw project name> — the directory the scaffold
// actually writes — for hyphenated, snake, and plain names alike. The
// regression this guards: naming.ServicePackage mangled hyphens to
// underscores ("control-plane" → "./cmd/control_plane"), so env-less
// `forge build` failed with "directory not found" on any hyphenated
// project even though `forge build ...` (KCL ./cmd/<name> default)
// worked fine.
func TestProjectGoBuildTarget_RawProjectName(t *testing.T) {
	cases := []struct {
		name    string
		project string
		wantCmd string
		wantBin string
	}{
		{"hyphenated", "control-plane", "./cmd/control-plane", "control-plane"},
		{"snake", "workspace_proxy", "./cmd/workspace_proxy", "workspace_proxy"},
		{"plain", "trader", "./cmd/trader", "trader"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := projectGoBuildTarget(&config.ProjectConfig{Name: c.project})
			if got.cmd != c.wantCmd {
				t.Errorf("cmd = %q, want %q", got.cmd, c.wantCmd)
			}
			if got.outputName != c.wantBin {
				t.Errorf("outputName = %q, want %q", got.outputName, c.wantBin)
			}
		})
	}
}

// TestResolveGoTargets_EnvLessHyphenatedProject pins the full env-less
// resolution path (entities == nil): one target, at the raw hyphenated
// cmd dir — consistent with what the KCL EffectiveBuild default would
// produce for the same name under --env.
func TestResolveGoTargets_EnvLessHyphenatedProject(t *testing.T) {
	cfg := &config.ProjectConfig{Name: "control-plane"}
	targets := resolveGoTargets(true, nil, cfg)
	if len(targets) != 1 {
		t.Fatalf("resolveGoTargets returned %d targets, want 1", len(targets))
	}
	if targets[0].cmd != "./cmd/control-plane" {
		t.Errorf("cmd = %q, want %q", targets[0].cmd, "./cmd/control-plane")
	}
	if targets[0].outputName != "control-plane" {
		t.Errorf("outputName = %q, want %q", targets[0].outputName, "control-plane")
	}
}
