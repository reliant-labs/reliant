package hostlaunch

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestBuildCmd_RunnerMatrix pins the host argv DERIVATION (ADR 0002 §2,
// P2 map §3.3): the command a host workload runs is computed from its
// runner, its GoBuild and its args. It is never authored per runtime, and
// there is no `server <name>` convention any more — the args a workload
// declares are the args it gets, on the host exactly as in its container.
//
// Derived for linux, so the expected argv is the same on every host: the only
// per-OS difference is the .exe suffix, which TestBuildCmdForWindowsBinaryAndDelve
// pins on its own.
func TestBuildCmd_RunnerMatrix(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		svc  string
		spec RunnerSpec
		want []string
	}{
		{
			name: "empty runner is go-run",
			svc:  "api",
			spec: RunnerSpec{GoPkg: "./cmd/acme", Args: []string{"api"}},
			want: []string{"go", "run", "./cmd/acme", "api"},
		},
		{
			name: "go-run: go run <build.cmd> <args...>",
			svc:  "migrate",
			spec: RunnerSpec{Runner: "go-run", GoPkg: "./cmd/acme", Args: []string{"db", "migrate", "up"}},
			want: []string{"go", "run", "./cmd/acme", "db", "migrate", "up"},
		},
		{
			name: "go-run with no args runs the bare binary",
			svc:  "api",
			spec: RunnerSpec{Runner: "go-run", GoPkg: "./cmd/acme"},
			want: []string{"go", "run", "./cmd/acme"},
		},
		{
			name: "air default config; args are the air config's business",
			svc:  "api",
			spec: RunnerSpec{Runner: "air", GoPkg: "./cmd/acme", Args: []string{"api"}},
			want: []string{"air", "-c", ".air.toml"},
		},
		{
			name: "air custom config",
			svc:  "api",
			spec: RunnerSpec{Runner: "air", AirConfig: "configs/api.air.toml"},
			want: []string{"air", "-c", "configs/api.air.toml"},
		},
		{
			name: "binary: ./bin/<output> <args...>",
			svc:  "api",
			spec: RunnerSpec{Runner: "binary", GoPkg: "./cmd/acme", OutputName: "acme", Args: []string{"api"}},
			want: []string{"./bin/acme", "api"},
		},
		{
			name: "binary output defaults to the workload name",
			svc:  "admin-server",
			spec: RunnerSpec{Runner: "binary", GoPkg: "./cmd/admin-server"},
			want: []string{"./bin/admin-server"},
		},
		{
			name: "delve default port, args after --",
			svc:  "api",
			spec: RunnerSpec{Runner: "delve", GoPkg: "./cmd/acme", OutputName: "acme", Args: []string{"api"}},
			want: []string{
				"dlv", "exec", "--headless", "--listen=:2345",
				"--api-version=2", "--accept-multiclient", "--continue",
				"./bin/acme", "--", "api",
			},
		},
		{
			name: "delve custom port, no args means no --",
			svc:  "api",
			spec: RunnerSpec{Runner: "delve", DelvePort: 4567, GoPkg: "./cmd/acme", OutputName: "acme"},
			want: []string{
				"dlv", "exec", "--headless", "--listen=:4567",
				"--api-version=2", "--accept-multiclient", "--continue",
				"./bin/acme",
			},
		},
		{
			name: "explicit command + args run verbatim (a sibling binary)",
			svc:  "reliant-api",
			spec: RunnerSpec{Runner: "go-run", Command: []string{"go", "run", "./cmd/reliant"}, Args: []string{"server", "api"}},
			want: []string{"go", "run", "./cmd/reliant", "server", "api"},
		},
		{
			name: "explicit command needs no build",
			svc:  "tool",
			spec: RunnerSpec{Runner: "binary", Command: []string{"/usr/local/bin/tool"}, Args: []string{"--x"}},
			want: []string{"/usr/local/bin/tool", "--x"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, err := buildCmdFor(ctx, "linux", c.svc, c.spec)
			if err != nil {
				t.Fatalf("BuildCmd: %v", err)
			}
			if strings.Join(cmd.Args, " ") != strings.Join(c.want, " ") {
				t.Fatalf("args:\n got  %q\n want %q", cmd.Args, c.want)
			}
		})
	}
}

// TestBuildCmd_RefusesUnderivableArgv: a runner that needs a Go build to
// derive its argv, handed none, is an ERROR naming the workload. The old
// behaviour — fall back to `go run ./cmd server <name>` — launched a
// command nobody declared, and an unknown runner silently became go-run.
func TestBuildCmd_RefusesUnderivableArgv(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name string
		spec RunnerSpec
		want string
	}{
		{"go-run without build", RunnerSpec{Runner: "go-run"}, "no Go build"},
		{"binary without build", RunnerSpec{Runner: "binary"}, "no Go build"},
		{"delve without build", RunnerSpec{Runner: "delve"}, "no Go build"},
		{"unknown runner", RunnerSpec{Runner: "tilt", GoPkg: "./cmd/x"}, "unknown host runner"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := BuildCmd(ctx, "api", c.spec)
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "api") {
				t.Fatalf("err = %v, want one naming the workload and containing %q", err, c.want)
			}
		})
	}
}

// TestAirIgnoresArgs: under air the air config owns the argv (full_bin /
// args_bin), so declared args cannot reach the process. That is reported
// to the caller rather than silently dropped.
func TestAirIgnoresArgs(t *testing.T) {
	if !(RunnerSpec{Runner: "air", Args: []string{"api"}}).IgnoresArgs() {
		t.Error("air with args: IgnoresArgs() = false, want true")
	}
	if (RunnerSpec{Runner: "air"}).IgnoresArgs() {
		t.Error("air without args: IgnoresArgs() = true, want false")
	}
	if (RunnerSpec{Runner: "go-run", Args: []string{"api"}}).IgnoresArgs() {
		t.Error("go-run: IgnoresArgs() = true, want false")
	}
}

// TestBuildCmd_WorkingDir pins the cross-repo cmd.Dir contract. The
// motivating case: a host workload declares `working_dir = "../sibling"`
// with an Air config that lives in the sibling repo and resolves build
// paths relative to that repo's root. With ProjectDir set to the caller's
// forge project root, the launched subprocess must chdir to the sibling so
// Air's `build_cmd` paths resolve correctly.
// The dev env's API runs `server` under air, and the scaffolded .air.toml's
// entrypoint is `[./tmp/<bin>, server]` — the declared args, honoured by the
// config. forge must not tell the user, on every `forge env up`, that they
// are "not passed". Anything that does not prove it (different args, no
// entrypoint, no file) keeps the note.
func TestAirRunsArgs(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".air.toml", "root = \".\"\n\n[build]\n  cmd = \"go build -o ./tmp/acme ./cmd/acme\"\n  entrypoint = [\"./tmp/acme\", \"server\"]\n\n[build.windows]\n  entrypoint = [\"./tmp/acme.exe\", \"server\"]\n")
	write("noentry.toml", "[build]\n  cmd = \"go build\"\n")
	write("broken.toml", "[build\n")

	for _, tc := range []struct {
		name string
		spec RunnerSpec
		want bool
	}{
		{"the scaffolded config runs server", RunnerSpec{Runner: "air", ProjectDir: dir, Args: []string{"server"}}, true},
		{"one service's subcommand is not what it runs", RunnerSpec{Runner: "air", ProjectDir: dir, Args: []string{"orders"}}, false},
		{"no entrypoint", RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "noentry.toml", Args: []string{"server"}}, false},
		{"unparseable", RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "broken.toml", Args: []string{"server"}}, false},
		{"missing", RunnerSpec{Runner: "air", ProjectDir: dir, AirConfig: "nope.toml", Args: []string{"server"}}, false},
		{"relative to the working dir", RunnerSpec{Runner: "air", ProjectDir: filepath.Dir(dir), WorkingDir: filepath.Base(dir), Args: []string{"server"}}, true},
	} {
		if got := tc.spec.AirRunsArgs(); got != tc.want {
			t.Errorf("%s: AirRunsArgs() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBuildCmd_WorkingDir(t *testing.T) {
	ctx := context.Background()
	// Host-absolute roots: "/forge/project" is only ROOTED on Windows (no
	// volume), so filepath.IsAbs would send the "absolute" case down the
	// relative branch there.
	base := t.TempDir()
	projectDir := filepath.Join(base, "forge", "project")
	absSibling := filepath.Join(base, "abs", "sibling")
	cases := []struct {
		name       string
		workingDir string
		projectDir string
		wantDir    string
	}{
		{"empty WorkingDir leaves cmd.Dir empty (inherit parent cwd)", "", projectDir, ""},
		{"absolute WorkingDir is used verbatim", absSibling, projectDir, absSibling},
		{"relative WorkingDir resolves against ProjectDir", "../sibling", projectDir, filepath.Join(base, "forge", "sibling")},
		{"relative WorkingDir with empty ProjectDir falls through verbatim", "../sibling", "", "../sibling"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd, err := BuildCmd(ctx, "api", RunnerSpec{Runner: "air", WorkingDir: c.workingDir, ProjectDir: c.projectDir})
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Dir != c.wantDir {
				t.Errorf("cmd.Dir: got %q, want %q", cmd.Dir, c.wantDir)
			}
		})
	}
}

// TestBuildCmd_WorkingDir_AppliesAcrossRunners confirms the cwd is set
// for every runner dispatch path, not just the runner that surfaced the
// cross-repo use case.
func TestBuildCmd_WorkingDir_AppliesAcrossRunners(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir() // host-absolute; see TestBuildCmd_WorkingDir
	wantDir := filepath.Join(base, "forge", "sibling")
	for _, runner := range []string{"air", "binary", "delve", "go-run", ""} {
		t.Run("runner="+runner, func(t *testing.T) {
			cmd, err := BuildCmd(ctx, "api", RunnerSpec{
				Runner: runner, GoPkg: "./cmd/api",
				WorkingDir: "../sibling", ProjectDir: filepath.Join(base, "forge", "project"),
			})
			if err != nil {
				t.Fatal(err)
			}
			if cmd.Dir != wantDir {
				t.Errorf("cmd.Dir: got %q, want %q", cmd.Dir, wantDir)
			}
		})
	}
}

// TestIsKnownRunner: only the four documented runners are known; empty
// counts as known (it means go-run).
func TestIsKnownRunner(t *testing.T) {
	for _, ok := range []string{"", "go-run", "air", "binary", "delve"} {
		if !IsKnownRunner(ok) {
			t.Errorf("IsKnownRunner(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"tilt", "Air", "GO-RUN", "x"} {
		if IsKnownRunner(bad) {
			t.Errorf("IsKnownRunner(%q) = true, want false", bad)
		}
	}
}

// TestLoadSecretsFile covers the three branches of the secrets-file
// loader: empty path → no-op; valid file → parsed; missing/unreadable
// → propagated error. The "secrets stay external to KCL" design relies
// on the empty-path no-op so services without secrets don't need to
// declare anything.
func TestLoadSecretsFile_EmptyPath(t *testing.T) {
	got, err := LoadSecretsFile("")
	if err != nil {
		t.Fatalf("empty path: want (nil, nil), got err=%v", err)
	}
	if got != nil {
		t.Errorf("empty path: want nil map, got %v", got)
	}
}

func TestLoadSecretsFile_Parses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env.dev.secrets")
	if err := os.WriteFile(path, []byte("STRIPE=sk_test_x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := LoadSecretsFile(path)
	if err != nil {
		t.Fatalf("LoadSecretsFile: %v", err)
	}
	if got["STRIPE"] != "sk_test_x" {
		t.Errorf("got %v", got)
	}
}

func TestLoadSecretsFile_Missing(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadSecretsFile(filepath.Join(dir, "does-not-exist"))
	if !os.IsNotExist(err) {
		t.Errorf("want os.ErrNotExist, got %v", err)
	}
}

// TestLoadSecretsFile_CrossRepoPath pins the cross-repo dev-loop
// contract: `HostDeploy.secrets_file = "../sibling/.env"` resolves
// against the caller's working directory and loads the file even
// though the path escapes the project root. Multi-checkout setups
// (forge project here, second binary in a sibling repo) rely on
// this — a future path-traversal sanity check that rejects `..`
// segments would silently break them.
//
// We construct a parent + sibling layout under t.TempDir(), chdir
// into the "project" dir, point secrets_file at "../sibling/.env",
// and assert the loader reads it. Symmetric with the WorkingDir
// cross-repo case already pinned in TestResolveWorkingDir.
func TestLoadSecretsFile_CrossRepoPath(t *testing.T) {
	parent := t.TempDir()
	project := filepath.Join(parent, "project")
	sibling := filepath.Join(parent, "sibling")
	for _, d := range []string{project, sibling} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	const secretsBody = "DATABASE_URL=postgres://sibling/db\nNATS_PASSWORD=hunter2\n"
	if err := os.WriteFile(filepath.Join(sibling, ".env"), []byte(secretsBody), 0o644); err != nil {
		t.Fatalf("seed sibling .env: %v", err)
	}

	// LoadSecretsFile resolves relative paths against the caller's
	// working directory, mirroring how `forge env up` invokes it after
	// chdir'ing into the project dir. Restore cwd on cleanup so the
	// rest of the suite isn't affected.
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(project); err != nil {
		t.Fatalf("chdir project: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	got, err := LoadSecretsFile("../sibling/.env")
	if err != nil {
		t.Fatalf("LoadSecretsFile(../sibling/.env): %v", err)
	}
	if got["DATABASE_URL"] != "postgres://sibling/db" {
		t.Errorf("DATABASE_URL = %q, want postgres://sibling/db", got["DATABASE_URL"])
	}
	if got["NATS_PASSWORD"] != "hunter2" {
		t.Errorf("NATS_PASSWORD = %q, want hunter2", got["NATS_PASSWORD"])
	}
}

// TestLayerHostEnv pins the layering contract: projectConfig first,
// then secrets on top, then env_vars (KCL) on top so KCL wins on
// conflict; base os.Environ() always wins last (developer shell
// override). The conflict ordering is the load-bearing reproducibility
// invariant — config can't drift because the secrets file accidentally
// shadows a KCL value, and forge.yaml config never silently shadows
// `.env.<env>` developer overrides.
func TestLayerHostEnv(t *testing.T) {
	base := []string{"PATH=/usr/bin", "EDITOR=vim"}
	projectConfig := map[string]string{
		"LOG_LEVEL":   "info",      // overridden by secrets then envVars
		"ENVIRONMENT": "developmt", // pass-through (typo intentional to spot leaks)
	}
	secrets := map[string]string{
		"LOG_LEVEL": "trace",     // overridden by envVars; overrides projectConfig
		"STRIPE":    "sk_test_x", // pass-through
	}
	envVars := map[string]string{
		"LOG_LEVEL":    "debug",       // wins over secrets and projectConfig
		"DATABASE_URL": "postgres://", // pass-through
		"PATH":         "/should/lose",
	}
	got := LayerHostEnv(base, projectConfig, secrets, envVars)

	want := []string{
		"PATH=/usr/bin", // base wins
		"LOG_LEVEL=debug",
		"STRIPE=sk_test_x",
		"DATABASE_URL=postgres://",
		"ENVIRONMENT=developmt",
	}
	for _, w := range want {
		found := false
		for _, kv := range got {
			if kv == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	// Verify the override didn't leak.
	for _, kv := range got {
		if kv == "LOG_LEVEL=trace" {
			t.Errorf("secrets value leaked: %v", got)
		}
		if kv == "LOG_LEVEL=info" {
			t.Errorf("projectConfig value leaked: %v", got)
		}
		if kv == "PATH=/should/lose" {
			t.Errorf("base PATH overridden: %v", got)
		}
	}
}

// TestLayerHostEnv_ProjectConfigOverriddenBySecrets pins the
// `.env.<env>` > forge.yaml config precedence: when both layers
// declare the same key, the gitignored dotenv (developer-local
// override) wins so a developer can shadow a committed forge.yaml
// value without editing tracked files.
func TestLayerHostEnv_ProjectConfigOverriddenBySecrets(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	projectConfig := map[string]string{
		"LOG_LEVEL": "info", // from forge.yaml
	}
	secrets := map[string]string{
		"LOG_LEVEL": "debug", // from .env.<env> — wins
	}
	got := LayerHostEnv(base, projectConfig, secrets, nil)

	for _, kv := range got {
		if kv == "LOG_LEVEL=info" {
			t.Errorf("forge.yaml value leaked through .env.<env>: %v", got)
		}
	}
	found := false
	for _, kv := range got {
		if kv == "LOG_LEVEL=debug" {
			found = true
		}
	}
	if !found {
		t.Errorf("LOG_LEVEL=debug missing from final env: %v", got)
	}
}

// TestLayerHostEnv_NilProjectConfig confirms the new layer is optional
// — passing nil keeps the legacy two-layer (secrets, envVars) shape so
// callers that don't surface forge.yaml config (e.g. `forge env up` host
// phase before it adopts the new layer) compile and behave unchanged.
func TestLayerHostEnv_NilProjectConfig(t *testing.T) {
	base := []string{"PATH=/usr/bin"}
	secrets := map[string]string{"STRIPE": "sk_test_x"}
	envVars := map[string]string{"LOG_LEVEL": "debug"}
	got := LayerHostEnv(base, nil, secrets, envVars)

	wants := []string{"PATH=/usr/bin", "STRIPE=sk_test_x", "LOG_LEVEL=debug"}
	for _, w := range wants {
		found := false
		for _, kv := range got {
			if kv == w {
				found = true
			}
		}
		if !found {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

// TestPIDPath confirms the canonical $HOME/.cache/forge/run/<svc>.pid
// location — `forge run <svc> stop` depends on this convention.
func TestPIDPath(t *testing.T) {
	got, err := PIDPath("admin-server")
	if err != nil {
		t.Fatalf("PIDPath: %v", err)
	}
	want := filepath.Join(".cache", "forge", "run", "admin-server.pid")
	if !strings.HasSuffix(got, string(filepath.Separator)+want) {
		t.Errorf("want path ending in %s, got %q", want, got)
	}
}

func TestExeName(t *testing.T) {
	for _, c := range []struct{ goos, in, want string }{
		{"windows", "api", "api.exe"},
		{"windows", "api.exe", "api.exe"},
		{"windows", "API.EXE", "API.EXE"},
		{"linux", "api", "api"},
		{"darwin", "api", "api"},
	} {
		if got := ExeName(c.goos, c.in); got != c.want {
			t.Errorf("ExeName(%q, %q) = %q, want %q", c.goos, c.in, got, c.want)
		}
	}
}

func TestBuildCmdForWindowsBinaryAndDelve(t *testing.T) {
	base := RunnerSpec{GoPkg: "./cmd/app", OutputName: "app-bin", Args: []string{"server"}}
	for _, c := range []struct {
		goos, runner string
		want         []string
	}{
		{"windows", "binary", []string{"./bin/app-bin.exe", "server"}},
		{"linux", "binary", []string{"./bin/app-bin", "server"}},
		{"windows", "delve", []string{"dlv", "exec", "--headless", "--listen=:2345", "--api-version=2", "--accept-multiclient", "--continue", "./bin/app-bin.exe", "--", "server"}},
		{"darwin", "delve", []string{"dlv", "exec", "--headless", "--listen=:2345", "--api-version=2", "--accept-multiclient", "--continue", "./bin/app-bin", "--", "server"}},
	} {
		spec := base
		spec.Runner = c.runner
		cmd, err := buildCmdFor(context.Background(), c.goos, "svc", spec)
		if err != nil {
			t.Fatalf("%s/%s: %v", c.goos, c.runner, err)
		}
		if !reflect.DeepEqual(cmd.Args, c.want) {
			t.Errorf("%s/%s argv = %v, want %v", c.goos, c.runner, cmd.Args, c.want)
		}
	}
	// No OutputName: falls back to the workload name, still suffixed.
	cmd, _ := buildCmdFor(context.Background(), "windows", "svc", RunnerSpec{Runner: "binary", GoPkg: "./cmd/app"})
	if cmd.Args[0] != "./bin/svc.exe" {
		t.Errorf("fallback name argv0 = %q, want ./bin/svc.exe", cmd.Args[0])
	}
}
