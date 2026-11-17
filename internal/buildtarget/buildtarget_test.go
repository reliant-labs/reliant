package buildtarget

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRunner captures the most recent RunWithEnv invocation so tests
// can assert what `sh -c <expanded>` would have run without spawning
// a real shell. Mirrors the deploytarget tests' fake — same minimum
// shape, no shared interface (the packages are independent).
type fakeRunner struct {
	mu    sync.Mutex
	calls []fakeCall
	err   error // optional canned error returned by RunWithEnv
}

const shellCallName = "<shell>"

type fakeCall struct {
	dir  string
	env  map[string]string
	name string
	args []string
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) error {
	return f.RunWithEnv(context.TODO(), nil, name, args...)
}

func (f *fakeRunner) RunWithEnv(ctx context.Context, env map[string]string, name string, args ...string) error {
	return f.RunInDir(ctx, "", env, name, args...)
}

func (f *fakeRunner) RunInDir(_ context.Context, dir string, env map[string]string, name string, args ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeCall{dir: dir, env: env, name: name, args: append([]string(nil), args...)})
	return f.err
}

func (f *fakeRunner) RunShell(_ context.Context, dir string, env map[string]string, script string) error {
	return f.RunInDir(context.TODO(), dir, env, shellCallName, script)
}

func (f *fakeRunner) last() (fakeCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// TestBuild_RunsCmdVerbatim is the core of the plain-KCL contract: the string
// forge hands `sh -c` is byte-for-byte the string the Spec carried. Nothing is
// substituted, so a ${FOO} in the command reaches the SHELL as ${FOO} and the
// shell resolves it (or does not) from the environment.
//
// This is the assertion that would have caught the old behaviour: under the
// substitution pass, an unknown token was left alone but every known one was
// rewritten — so a command was not the thing the author wrote, and the KCL that
// composed it could not be reasoned about on its own.
func TestBuild_RunsCmdVerbatim(t *testing.T) {
	projDir := t.TempDir()
	fake := &fakeRunner{}
	r := Runner{runner: fake}

	// Every retired token spelling, plus ordinary shell syntax. All of it must
	// survive untouched.
	cmd := `docker build -t ${IMAGE}:${TAG} --build-arg A=${TARGETARCH} ` +
		`--build-arg B=${REGISTRY} --build-arg C=${PROJECT_DIR} ${ENV} ${SERVICE} ` +
		`&& W=$(mktemp -d) && cp -r "$W/x" "${HOME}/y"`
	res := r.Build(context.Background(), Spec{
		Service:    "svc",
		Image:      "ghcr.io/acme/svc",
		Tag:        "v1.2.3",
		ProjectDir: projDir,
		BuildCmd:   cmd,
	})
	if res.Err != nil {
		t.Fatalf("Build: unexpected err: %v", res.Err)
	}
	call, ok := fake.last()
	if !ok {
		t.Fatal("runner was not invoked")
	}
	if len(call.args) != 1 {
		t.Fatalf("runner args: got %v, want [<cmd>]", call.args)
	}
	if call.args[0] != cmd {
		t.Errorf("cmd was not passed verbatim:\n got %q\nwant %q", call.args[0], cmd)
	}
}

// The declared env map is merged onto the process environment for the command
// — which is the ONLY channel forge has for handing the shell a value now that
// substitution is gone, and therefore the documented way to keep a ${NAME}
// spelling working.
func TestBuild_DeclaredEnvReachesTheCommand(t *testing.T) {
	projDir := t.TempDir()
	fake := &fakeRunner{}
	r := Runner{runner: fake}

	res := r.Build(context.Background(), Spec{
		Service:    "edge",
		ProjectDir: projDir,
		BuildCmd:   "build --region ${REGION} --arch ${TARGETARCH}",
		BuildEnv: map[string]string{
			"REGION":     "us-east-1",
			"TARGETARCH": "arm64",
		},
	})
	if res.Err != nil {
		t.Fatalf("Build: unexpected err: %v", res.Err)
	}
	call, _ := fake.last()
	// The command still carries the tokens — forge did not touch them...
	if !strings.Contains(call.args[0], "${REGION}") || !strings.Contains(call.args[0], "${TARGETARCH}") {
		t.Errorf("cmd should be verbatim, got %q", call.args[0])
	}
	// ...and the values ride in the env, where the shell will find them.
	if call.env["REGION"] != "us-east-1" {
		t.Errorf("env REGION: got %q, want us-east-1", call.env["REGION"])
	}
	if call.env["TARGETARCH"] != "arm64" {
		t.Errorf("env TARGETARCH: got %q, want arm64", call.env["TARGETARCH"])
	}
}

// TestBuild_ExecsInDeclaredCwd pins the happy path: the command is handed to
// `sh -c` unchanged, BuildEnv flows through to the runner env overlay, the
// working directory is carried as cmd.Dir rather than a `cd …` shell prefix,
// and the result reports success with the tag it was given.
func TestBuild_ExecsInDeclaredCwd(t *testing.T) {
	projDir := t.TempDir()
	// Create the build_cwd so the runner doesn't trigger skip-with-warn.
	cwd := filepath.Join(projDir, "sibling")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}

	fake := &fakeRunner{}
	r := Runner{runner: fake}
	spec := Spec{
		Service:    "daemon-gateway",
		Image:      "reliant-daemon-gateway",
		Tag:        "v1.2.3",
		ProjectDir: projDir,
		BuildCwd:   "sibling",
		// The reference is composed in KCL, so by the time it reaches a Spec
		// it is a literal — this is what a rendered cmd looks like.
		BuildCmd: "docker build -t localhost:5051/reliant-daemon-gateway:v1.2.3 .",
		BuildEnv: map[string]string{
			"REGION": "us-east-1",
		},
	}
	res := r.Build(context.Background(), spec)
	if res.Err != nil {
		t.Fatalf("Build: unexpected err: %v", res.Err)
	}
	if res.Skipped {
		t.Fatal("Build: unexpected skip")
	}
	if res.Service != "daemon-gateway" {
		t.Errorf("Service: got %q, want daemon-gateway", res.Service)
	}
	if res.Tag != "v1.2.3" {
		t.Errorf("Tag: got %q, want v1.2.3", res.Tag)
	}

	call, ok := fake.last()
	if !ok {
		t.Fatal("runner was not invoked")
	}
	if call.name != shellCallName {
		t.Errorf("runner name: got %q, want shell", call.name)
	}
	if len(call.args) != 1 {
		t.Fatalf("runner args: got %v, want [<cmd>]", call.args)
	}
	// The working directory is carried as cmd.Dir (call.dir), NOT a
	// shell `cd <cwd> && …` prefix — so a path with spaces or shell
	// metacharacters can never break the script.
	if call.args[0] != spec.BuildCmd {
		t.Errorf("cmd: got %q, want %q (verbatim)", call.args[0], spec.BuildCmd)
	}
	if call.dir != cwd {
		t.Errorf("runner dir: got %q, want %q", call.dir, cwd)
	}
	if strings.Contains(call.args[0], "cd ") {
		t.Errorf("expanded cmd should not carry a `cd …` shell prefix; got %q", call.args[0])
	}
	if call.env["REGION"] != "us-east-1" {
		t.Errorf("env REGION: got %q, want us-east-1", call.env["REGION"])
	}
}

// TestBuild_FailsWhenCwdMissing locks in the corrected contract: a
// missing build_cwd is a HARD FAILURE (Err set, naming the missing path
// and the service), not a warn-skip. A Spec only reaches Build for a
// service that is IN the current env, so a missing source tree means a
// build that was supposed to run can't — and reporting success would let
// a following deploy reference an unpushed image (the gotcha-B outage).
// The runner is NEVER invoked (no shell spawned for a doomed build), and
// the error must name the missing path so the user can act on it.
func TestBuild_FailsWhenCwdMissing(t *testing.T) {
	projDir := t.TempDir()
	fake := &fakeRunner{}
	r := Runner{runner: fake}
	spec := Spec{
		Service:    "edge",
		Image:      "edge",
		Tag:        "v1",
		ProjectDir: projDir,
		BuildCwd:   "does-not-exist",
		BuildCmd:   "docker build .",
	}
	res := r.Build(context.Background(), spec)
	if res.Err == nil {
		t.Fatal("Build: want Err set when build_cwd missing, got nil (skip-masquerading-as-success regression)")
	}
	if res.Skipped {
		t.Error("Build: missing build_cwd must NOT be a skip")
	}
	if !strings.Contains(res.Err.Error(), "does not exist") {
		t.Errorf("Err: got %q, want a 'does not exist' message", res.Err.Error())
	}
	// The error must name the missing path so the user knows what to
	// check out.
	wantPath := filepath.Join(projDir, "does-not-exist")
	if !strings.Contains(res.Err.Error(), wantPath) {
		t.Errorf("Err: got %q, want it to name the missing path %q", res.Err.Error(), wantPath)
	}
	// And name the service so the error is actionable in a multi-service
	// build.
	if !strings.Contains(res.Err.Error(), "edge") {
		t.Errorf("Err: got %q, want it to name the service %q", res.Err.Error(), "edge")
	}
	if _, ok := fake.last(); ok {
		t.Error("runner should not have been invoked for a doomed build")
	}
}

// TestBuild_NoCwd confirms an UNSET BuildCwd resolves explicitly to the
// project root (spec.ProjectDir) — the single documented cwd contract for
// the shell hatch — rather than inheriting the host cwd. The command is
// passed via cmd.Dir (not a `cd <abs> && ` shell prefix). ProjectDir is
// trusted to exist, so no existence check fires for the no-cwd case.
func TestBuild_NoCwd(t *testing.T) {
	fake := &fakeRunner{}
	r := Runner{runner: fake}
	spec := Spec{
		Service:    "x",
		Image:      "x",
		Tag:        "v1",
		ProjectDir: "/proj",
		BuildCmd:   "echo x",
	}
	res := r.Build(context.Background(), spec)
	if res.Err != nil {
		t.Fatalf("Build: unexpected err: %v", res.Err)
	}
	call, _ := fake.last()
	if call.dir != "/proj" {
		t.Errorf("no BuildCwd should resolve to ProjectDir; got %q, want /proj", call.dir)
	}
	if strings.HasPrefix(call.args[0], "cd ") {
		t.Errorf("no BuildCwd should not produce a `cd …` prefix; got %q", call.args[0])
	}
	if call.args[0] != spec.BuildCmd {
		t.Errorf("cmd: got %q, want %q (verbatim)", call.args[0], spec.BuildCmd)
	}
}

// TestBuild_EmptyBuildCmdIsDispatcherBug pins the contract: callers
// should NOT construct a Spec without BuildCmd set — the dispatcher
// filters those out before reaching Runner.Build. If one slips through,
// surface a loud error rather than silently no-op.
func TestBuild_EmptyBuildCmdIsDispatcherBug(t *testing.T) {
	r := Runner{runner: &fakeRunner{}}
	spec := Spec{Service: "x", Image: "x", Tag: "v1"} // BuildCmd left empty
	res := r.Build(context.Background(), spec)
	if res.Err == nil {
		t.Fatal("Build: expected error for empty BuildCmd")
	}
	if !strings.Contains(res.Err.Error(), "dispatcher bug") {
		t.Errorf("Err: got %v, want a 'dispatcher bug' message", res.Err)
	}
}

// TestWriteAndReadState round-trips a State through disk to confirm
// the per-service file layout and JSON shape. Pins the path so a
// future refactor of statePath catches the consumer (forge env deploy
// reads the file by path and would silently miss a relocation).
func TestWriteAndReadState(t *testing.T) {
	projDir := t.TempDir()
	want := State{
		Service: "daemon-gateway",
		// Image is the full repository, host included: the registry is part of
		// the image, so State has no separate Registry field to disagree with.
		Image:    "localhost:5051/reliant-daemon-gateway",
		Tag:      "v1.2.3",
		PushedAt: "2026-06-05T16:00:00Z",
	}
	if err := WriteState(projDir, "dev", want); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	// Confirm the path layout — one file per (env, service).
	wantPath := filepath.Join(projDir, ".forge", "state", "build-dev-daemon-gateway.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("state file at %s: %v", wantPath, err)
	}
	// And that the on-disk shape is JSON (round-trip via the public API).
	got, err := ReadState(projDir, "dev", "daemon-gateway")
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if got == nil {
		t.Fatal("ReadState: got nil for an existing file")
	}
	if got.Service != want.Service || got.Image != want.Image || got.Tag != want.Tag ||
		got.PushedAt != want.PushedAt {
		t.Errorf("round-trip mismatch:\n got: %+v\nwant: %+v", *got, want)
	}
	// Verify ReadState returns (nil, nil) for a missing file —
	// the "deploy without build" path the dispatcher relies on.
	none, nerr := ReadState(projDir, "dev", "missing-service")
	if nerr != nil {
		t.Errorf("ReadState missing file: unexpected err %v", nerr)
	}
	if none != nil {
		t.Errorf("ReadState missing file: want nil, got %+v", *none)
	}
}

// TestStatePath_EmptyEnvCollapsesToDefault pins the path-stability
// contract: an empty env still produces a usable, stable filename so
// projects that haven't migrated to per-env state can still write one
// canonical file rather than landing on path/build--service.json.
func TestStatePath_EmptyEnvCollapsesToDefault(t *testing.T) {
	got := StatePath("/proj", "", "svc")
	want := filepath.Join("/proj", ".forge", "state", "build-default-svc.json")
	if got != want {
		t.Errorf("StatePath empty env: got %q, want %q", got, want)
	}
}

// TestWriteState_JSONShape pins the on-disk JSON shape — fields are
// snake_case so a user can eyeball the file by hand without a
// formatter. A future serializer change (camel-case, type rename)
// trips this test before it ships and breaks downstream consumers.
func TestWriteState_JSONShape(t *testing.T) {
	projDir := t.TempDir()
	state := State{
		Service:  "edge",
		Image:    "edge",
		Tag:      "v1",
		PushedAt: "2026-06-05T16:00:00Z",
	}
	if err := WriteState(projDir, "dev", state); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	data, err := os.ReadFile(StatePath(projDir, "dev", "edge"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// No "registry" key: the registry is part of "image", which holds the full
	// repository. A separate key would be a second place for it to disagree.
	for _, k := range []string{"service", "image", "tag", "pushed_at"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing key %q in: %s", k, string(data))
		}
	}
	if _, ok := raw["registry"]; ok {
		t.Errorf("state carries a separate registry key; the image holds it: %s", string(data))
	}
}
