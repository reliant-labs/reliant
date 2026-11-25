package deploytarget

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// fakeHooks is a HookRunner that answers by FORGE_HOOK and records every
// invocation, so a test states what each hook does without exec'ing anything.
type fakeHooks struct {
	// status answers are consumed in order; the last one repeats.
	status []fakeAnswer
	plan   fakeAnswer
	deploy fakeAnswer
	health fakeAnswer
	calls  []HookInvocation
}

type fakeAnswer struct {
	stdout string
	code   int
	err    error
}

func (f *fakeHooks) RunHook(_ context.Context, inv HookInvocation) (int, error) {
	f.calls = append(f.calls, inv)
	var a fakeAnswer
	switch inv.Env["FORGE_HOOK"] {
	case HookStatus:
		if len(f.status) == 0 {
			return -1, errors.New("fake: no status answer")
		}
		a = f.status[0]
		if len(f.status) > 1 {
			f.status = f.status[1:]
		}
	case HookPlan:
		a = f.plan
	case HookDeploy:
		a = f.deploy
	case HookHealth:
		a = f.health
	}
	if a.err != nil {
		return -1, a.err
	}
	_, _ = io.WriteString(inv.Stdout, a.stdout)
	return a.code, nil
}

func (f *fakeHooks) hooksRun() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Env["FORGE_HOOK"])
	}
	return out
}

const (
	extDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	extDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func statusJSON(digest string) string {
	return fmt.Sprintf(`{"digest":%q,"version":"v1","health":"healthy"}`, digest)
}

func fullSpec() ExternalSpec {
	return ExternalSpec{
		Runtime: ExternalRuntime{
			Status: []string{"./status.sh"},
			Plan:   []string{"helm", "diff", "--set", "image=$FORGE_IMAGE"},
			Deploy: []string{"helm", "upgrade", "--set", "image=${FORGE_IMAGE}"},
			Health: []string{"curl", "-f", "https://x/readyz"},
			Env:    map[string]string{"KUBECONFIG": "/k", "SHARED": "runtime"},
		},
		EnvName:     "prod",
		Workload:    "api",
		Project:     "acme",
		ProjectDir:  "/repo",
		Image:       "ghcr.io/acme/api@" + extDigestB,
		Version:     "v1.2.3",
		Release:     "20261111.1",
		WorkloadEnv: map[string]string{"SHARED": "workload", "LOG_LEVEL": "info"},
	}
}

func extGroup(specs ...ExternalSpec) ServiceGroup {
	g := ServiceGroup{Env: "prod", ProviderID: ExternalProviderID}
	for i := range specs {
		s := specs[i]
		g.Services = append(g.Services, ResolvedService{Name: s.Workload, External: &s})
	}
	return g
}

func TestExternalRuntime_ValidateMirrorsTheSchema(t *testing.T) {
	cases := []struct {
		name string
		rt   ExternalRuntime
		want string
	}{
		{"record-only", ExternalRuntime{}, ""},
		{"full", ExternalRuntime{Status: []string{"s"}, Plan: []string{"p"}, Deploy: []string{"d"}, Health: []string{"h"}, Platform: "arm64"}, ""},
		{"empty argv", ExternalRuntime{Deploy: []string{}}, "OnExternal.deploy is an empty argv"},
		{"blank program", ExternalRuntime{Status: []string{"", "x"}}, "OnExternal.status[0] is empty"},
		{"reserved env", ExternalRuntime{Env: map[string]string{"FORGE_IMAGE": "x"}}, "OnExternal.env may not set [FORGE_IMAGE]"},
		{"platform", ExternalRuntime{Platform: "386"}, "OnExternal.platform must be"},
	}
	for _, c := range cases {
		err := c.rt.Validate()
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: unexpected error %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: error %v, want it to contain %q", c.name, err, c.want)
		}
	}
	if !(ExternalRuntime{}).RecordOnly() || (ExternalRuntime{Health: []string{"h"}}).RecordOnly() {
		t.Error("RecordOnly must be true exactly when no hook is declared")
	}
}

// The FORGE_* names are a contract stated twice — in the KCL schema (which
// refuses them in OnExternal.env) and here (which sets them). Read the KCL
// list and hold the two equal.
func TestExternalHookEnvMatchesKCL(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "kcl", "workload.k"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)EXTERNAL_HOOK_ENV: \[str\] = \[(.*?)\]`).FindSubmatch(src)
	if block == nil {
		t.Fatal("kcl/workload.k declares no EXTERNAL_HOOK_ENV list")
	}
	var kcl []string
	for _, m := range regexp.MustCompile(`"([A-Z_]+)"`).FindAllSubmatch(block[1], -1) {
		kcl = append(kcl, string(m[1]))
	}
	if !slices.Equal(kcl, ExternalHookEnvNames()) {
		t.Errorf("kcl EXTERNAL_HOOK_ENV = %v, Go sets %v: keep them equal", kcl, ExternalHookEnvNames())
	}
	set := fullSpec().forgeEnv(HookDeploy, "")
	for _, n := range ExternalHookEnvNames() {
		if _, ok := set[n]; !ok {
			t.Errorf("documented variable %s is never set for a hook", n)
		}
	}
	if len(set) != len(ExternalHookEnvNames()) {
		t.Errorf("a hook gets %d FORGE_* variables, the contract documents %d", len(set), len(ExternalHookEnvNames()))
	}
}

func TestExpandHookArgv_OnlyForgeNamesAndOneArgumentEach(t *testing.T) {
	vars := map[string]string{"FORGE_IMAGE": "ghcr.io/a/b@" + extDigestA, "FORGE_ENV": "prod env; rm -rf /"}
	got := expandHookArgv([]string{"deploy", "--image=$FORGE_IMAGE", "${FORGE_ENV}", "$HOME", "$FORGE_UNKNOWN", "${PATH}"}, vars)
	want := []string{"deploy", "--image=ghcr.io/a/b@" + extDigestA, "prod env; rm -rf /", "$HOME", "$FORGE_UNKNOWN", "${PATH}"}
	if !slices.Equal(got, want) {
		t.Errorf("expandHookArgv = %q\nwant %q", got, want)
	}
}

func TestHookEnv_ForgeWinsAndPassEnvIsOptIn(t *testing.T) {
	s := fullSpec()
	env := s.hookEnv(HookDeploy, extDigestA)
	if env["SHARED"] != "runtime" || env["LOG_LEVEL"] != "" {
		t.Errorf("pass_env off: workload env leaked into the hook: %v", env)
	}
	s.Runtime.PassEnv = true
	env = s.hookEnv(HookDeploy, extDigestA)
	if env["SHARED"] != "workload" || env["LOG_LEVEL"] != "info" {
		t.Errorf("pass_env on: want the workload env over the runtime env, got %v", env)
	}
	s.WorkloadEnv["FORGE_IMAGE"] = "evil"
	env = s.hookEnv(HookDeploy, extDigestA)
	if env["FORGE_IMAGE"] != s.Image || env["FORGE_DIGEST"] != extDigestB || env["FORGE_PREVIOUS_DIGEST"] != extDigestA ||
		env["FORGE_RELEASE"] != "20261111.1" || env["FORGE_VERSION"] != "v1.2.3" || env["FORGE_WORKLOAD"] != "api" ||
		env["FORGE_ENV"] != "prod" || env["FORGE_PROJECT"] != "acme" || env["FORGE_PROJECT_DIR"] != "/repo" || env["FORGE_HOOK"] != HookDeploy {
		t.Errorf("FORGE_* must be forge's and win: %v", env)
	}
}

func TestParseExternalStatus(t *testing.T) {
	st, err := ParseExternalStatus([]byte("  " + `{"digest":"ghcr.io/a/b@` + extDigestA + `","version":"v2","image":"ghcr.io/a/b","health":"Degraded"}` + "\n"))
	if err != nil || st.Digest != extDigestA || st.Version != "v2" || st.ObservedHealth() != HealthDegraded {
		t.Fatalf("parse = %+v, %v", st, err)
	}
	if st, _ := ParseExternalStatus([]byte(`{"digest":"` + extDigestA + `","health":"great"}`)); st.ObservedHealth() != HealthUnknown {
		t.Error("an unrecognised health must read as unknown, never healthy")
	}
	for name, in := range map[string]string{
		"no json":    "deployed v2",
		"two values": statusJSON(extDigestA) + statusJSON(extDigestB),
		"trailing }": statusJSON(extDigestA) + "}",
		"trailing ]": statusJSON(extDigestA) + " ]",
		"trailing 1": statusJSON(extDigestA) + "\n1",
		"tag digest": `{"digest":"v1.2.3"}`,
	} {
		if _, err := ParseExternalStatus([]byte(in)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

// An inherited FORGE_* variable must not survive beside the overlay's: a
// child's getenv returns the first occurrence, so a duplicate would let the
// parent's FORGE_IMAGE override the release's pinned image.
func TestHookEnviron_OverlayReplacesInherited(t *testing.T) {
	base := []string{"PATH=/bin", "FORGE_IMAGE=evil@sha256:0", "HOME=/h", "FORGE_ENV=dev", "NOEQUALS"}
	got := hookEnviron(base, map[string]string{"FORGE_IMAGE": "good@" + extDigestA, "FORGE_ENV": "prod"})
	want := []string{"PATH=/bin", "HOME=/h", "NOEQUALS", "FORGE_ENV=prod", "FORGE_IMAGE=good@" + extDigestA}
	if !slices.Equal(got, want) {
		t.Fatalf("hookEnviron = %q\nwant          %q", got, want)
	}
	if got := hookEnviron(base, nil); !slices.Equal(got, base) {
		t.Errorf("an empty overlay must pass the environment through; got %q", got)
	}
}

func TestExternalProvider_RecordOnlyRunsNothing(t *testing.T) {
	fake := &fakeHooks{}
	var out bytes.Buffer
	s := fullSpec()
	s.Runtime = ExternalRuntime{}
	p := ExternalProvider{Hooks: ExternalHooks{Runner: fake}, Out: &out}
	if err := p.Deploy(context.Background(), extGroup(s)); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("record-only ran hooks: %v", fake.hooksRun())
	}
	if !strings.Contains(out.String(), "record-only") {
		t.Errorf("record-only must say so; printed %q", out.String())
	}
}

func TestExternalProvider_DeployRunsStatusDeployHealthStatus(t *testing.T) {
	fake := &fakeHooks{status: []fakeAnswer{{stdout: statusJSON(extDigestA)}, {stdout: statusJSON(extDigestB)}}}
	var out bytes.Buffer
	p := ExternalProvider{Hooks: ExternalHooks{Runner: fake, Stderr: io.Discard}, Out: &out}
	if err := p.Deploy(context.Background(), extGroup(fullSpec())); err != nil {
		t.Fatal(err)
	}
	if got := fake.hooksRun(); !slices.Equal(got, []string{HookStatus, HookDeploy, HookHealth, HookStatus}) {
		t.Fatalf("hooks run = %v", got)
	}
	deploy := fake.calls[1]
	if deploy.Env["FORGE_PREVIOUS_DIGEST"] != extDigestA {
		t.Errorf("FORGE_PREVIOUS_DIGEST = %q, want what status read before the deploy", deploy.Env["FORGE_PREVIOUS_DIGEST"])
	}
	if deploy.Argv[3] != "image=ghcr.io/acme/api@"+extDigestB || deploy.Dir != "/repo" {
		t.Errorf("deploy argv/dir = %q %q", deploy.Argv, deploy.Dir)
	}
	if !strings.Contains(out.String(), "running "+extDigestB) {
		t.Errorf("a confirmed deploy names the digest now running; printed %q", out.String())
	}
}

func TestExternalProvider_DeployFailureStopsBeforeHealth(t *testing.T) {
	fake := &fakeHooks{status: []fakeAnswer{{stdout: statusJSON(extDigestA)}}, deploy: fakeAnswer{code: 3}}
	p := ExternalProvider{Hooks: ExternalHooks{Runner: fake, Stderr: io.Discard}, Out: io.Discard}
	err := p.Deploy(context.Background(), extGroup(fullSpec()))
	if err == nil || !strings.Contains(err.Error(), "deploy hook exited 3") {
		t.Fatalf("err = %v", err)
	}
	if slices.Contains(fake.hooksRun(), HookHealth) {
		t.Error("health ran after a failed deploy")
	}
}

func TestExternalProvider_HealthFailureFailsTheDeploy(t *testing.T) {
	fake := &fakeHooks{status: []fakeAnswer{{stdout: statusJSON(extDigestA)}}, health: fakeAnswer{code: 1}}
	p := ExternalProvider{Hooks: ExternalHooks{Runner: fake, Stderr: io.Discard}, Out: io.Discard}
	if err := p.Deploy(context.Background(), extGroup(fullSpec())); err == nil || !strings.Contains(err.Error(), "health hook exited 1") {
		t.Fatalf("err = %v", err)
	}
}

func TestExternalProvider_UnconvergedStatusWarnsNotFails(t *testing.T) {
	fake := &fakeHooks{status: []fakeAnswer{{stdout: statusJSON(extDigestA)}}}
	var out bytes.Buffer
	p := ExternalProvider{Hooks: ExternalHooks{Runner: fake, Stderr: io.Discard}, Out: &out}
	if err := p.Deploy(context.Background(), extGroup(fullSpec())); err != nil {
		t.Fatalf("an asynchronous applier that has not converged yet is a warning: %v", err)
	}
	if !strings.Contains(out.String(), "warning") || !strings.Contains(out.String(), "forge env verify prod") {
		t.Errorf("printed %q", out.String())
	}
}

func TestExternalProvider_DryRunRunsNothing(t *testing.T) {
	fake := &fakeHooks{}
	var out bytes.Buffer
	g := extGroup(fullSpec())
	g.DryRun = true
	if err := (ExternalProvider{Hooks: ExternalHooks{Runner: fake}, Out: &out}).Deploy(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 0 || !strings.Contains(out.String(), "would run deploy hook") {
		t.Errorf("dry run ran %v, printed %q", fake.hooksRun(), out.String())
	}
}

func TestExternalProvider_Observe(t *testing.T) {
	ctx := context.Background()
	noStatus := fullSpec()
	noStatus.Workload, noStatus.Runtime.Status = "worker", nil

	// No workload is readable: unsupported, every item unknown with a reason.
	obs, err := (ExternalProvider{Hooks: ExternalHooks{Runner: &fakeHooks{}}}).Observe(ctx, extGroup(noStatus))
	if !errors.Is(err, ErrObservationUnsupported) || len(obs.Items) != 1 || obs.Items[0].Health != HealthUnknown || obs.Items[0].Detail == "" {
		t.Fatalf("no status hook: obs=%+v err=%v", obs, err)
	}

	// One readable, one not: the readable one reports its digest; the other
	// is unknown with a reason, never dropped.
	fake := &fakeHooks{status: []fakeAnswer{{stdout: statusJSON(extDigestA)}}}
	obs, err = (ExternalProvider{Hooks: ExternalHooks{Runner: fake, Stderr: io.Discard}}).Observe(ctx, extGroup(fullSpec(), noStatus))
	if err != nil {
		t.Fatal(err)
	}
	if obs.ProviderID != ExternalProviderID || obs.ObservedAt.IsZero() || len(obs.Items) != 2 {
		t.Fatalf("obs = %+v", obs)
	}
	if obs.Items[0].Digest != extDigestA || obs.Items[0].Health != HealthHealthy {
		t.Errorf("api = %+v", obs.Items[0])
	}
	if obs.Items[1].Health != HealthUnknown || !strings.Contains(obs.Items[1].Detail, "no status hook") {
		t.Errorf("worker = %+v", obs.Items[1])
	}

	// A status hook that fails is an error and an unknown item.
	fake = &fakeHooks{status: []fakeAnswer{{code: 1}}}
	obs, err = (ExternalProvider{Hooks: ExternalHooks{Runner: fake, Stderr: io.Discard}}).Observe(ctx, extGroup(fullSpec()))
	if err == nil || obs.Items[0].Health != HealthUnknown {
		t.Errorf("failing status: obs=%+v err=%v", obs, err)
	}
}

func TestExternalHooks_Plan(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		code    int
		want    PlanOutcome
		wantErr bool
	}{{0, PlanNoChange, false}, {2, PlanChanges, false}, {1, PlanNoChange, true}} {
		fake := &fakeHooks{plan: fakeAnswer{stdout: "~ image", code: c.code}}
		var out bytes.Buffer
		got, err := (ExternalHooks{Runner: fake}).Plan(ctx, fullSpec(), &out)
		if (err != nil) != c.wantErr || got != c.want {
			t.Errorf("exit %d: outcome %v err %v", c.code, got, err)
		}
		if out.String() != "~ image" {
			t.Errorf("exit %d: the plan's own output must reach the caller, got %q", c.code, out.String())
		}
	}
	s := fullSpec()
	s.Runtime.Plan = nil
	if _, err := (ExternalHooks{Runner: &fakeHooks{}}).Plan(ctx, s, io.Discard); !errors.Is(err, ErrNoHook) {
		t.Errorf("no plan hook: err = %v, want ErrNoHook", err)
	}
}

// The production runner, for real: argv with no shell, the env overlay, the
// exit code as an answer rather than an error, and a timeout as an error.
func TestExecHookRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("execs sh; runs in task test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	ctx := context.Background()
	var out bytes.Buffer
	code, err := ExecHookRunner{}.RunHook(ctx, HookInvocation{
		Argv:   []string{"sh", "-c", `printf %s "$FORGE_IMAGE"; exit 2`},
		Env:    map[string]string{"FORGE_IMAGE": "img; echo injected"},
		Stdout: &out, Stderr: io.Discard,
	})
	if err != nil || code != 2 || out.String() != "img; echo injected" {
		t.Errorf("code=%d err=%v out=%q", code, err, out.String())
	}
	code, err = ExecHookRunner{}.RunHook(ctx, HookInvocation{Argv: []string{"sleep", "5"}, Stdout: io.Discard, Stderr: io.Discard, Timeout: 50e6})
	if err == nil || code != -1 {
		t.Errorf("timeout: code=%d err=%v", code, err)
	}
	if _, err := (ExecHookRunner{}).RunHook(ctx, HookInvocation{Argv: []string{"forge-no-such-hook-binary"}, Stdout: io.Discard, Stderr: io.Discard}); err == nil {
		t.Error("a missing program must be an error, not an exit code")
	}
}
