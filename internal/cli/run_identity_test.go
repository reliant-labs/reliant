package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/spf13/pflag"

	"github.com/reliant-labs/forge/pkg/release"
)

// envMap turns a map into the getenv function resolve takes, so these tests
// never touch the process environment (t.Setenv forbids t.Parallel, and a
// stray CI variable in a developer's shell would otherwise change results).
func envMap(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

// TestRunFromEnvironment_GitHubDefault pins §3.A's documented shape exactly.
// It is a CONTRACT, not a formatting choice: the id is the join key across a
// release, its promotions and its gates, so every step of one pipeline must
// compute the identical string.
func TestRunFromEnvironment_GitHubDefault(t *testing.T) {
	t.Parallel()
	run := runFromEnvironment(envMap(map[string]string{
		envGitHubRepository: "acme/app",
		envGitHubRunID:      "12345",
		envGitHubRunAttempt: "2",
		envGitHubServerURL:  "https://github.com",
	}))
	if want := "github:acme/app/12345/2"; run.ID != want {
		t.Errorf("run id = %q, want %q", run.ID, want)
	}
	if want := "https://github.com/acme/app/actions/runs/12345"; run.URL != want {
		t.Errorf("run url = %q, want %q", run.URL, want)
	}
	if run.Provider != runProviderGitHub {
		t.Errorf("provider = %q", run.Provider)
	}
	if err := run.Validate(); err != nil {
		t.Errorf("a defaulted run must be valid: %v", err)
	}
}

// TestRunFromEnvironment_AttemptIsPartOfTheID is the subtle one.
//
// GitHub keeps GITHUB_RUN_ID stable across re-runs and increments
// GITHUB_RUN_ATTEMPT. The gate idempotency key is (promotion, name, run id),
// so if the attempt were not in the id, a re-run's gates would collide with
// the first attempt's and RecordGate would hand back the old rows — the
// re-run's evidence would silently be the failed run's evidence.
func TestRunFromEnvironment_AttemptIsPartOfTheID(t *testing.T) {
	t.Parallel()
	base := map[string]string{envGitHubRepository: "acme/app", envGitHubRunID: "12345"}

	first := runFromEnvironment(envMap(map[string]string{
		envGitHubRepository: "acme/app", envGitHubRunID: "12345", envGitHubRunAttempt: "1",
	}))
	second := runFromEnvironment(envMap(map[string]string{
		envGitHubRepository: "acme/app", envGitHubRunID: "12345", envGitHubRunAttempt: "2",
	}))
	if first.ID == second.ID {
		t.Fatalf("two attempts of one run must have distinct ids; both were %q", first.ID)
	}
	// Both attempts link to the same run page: the URL is the run, the id
	// is the attempt.
	if first.URL != second.URL {
		t.Errorf("attempts of one run should share a url: %q vs %q", first.URL, second.URL)
	}
	// An absent attempt means the first one, and must produce the SAME id
	// shape so no parser ever meets two forms.
	absent := runFromEnvironment(envMap(base))
	if absent.ID != first.ID {
		t.Errorf("an absent attempt should read as attempt 1: %q vs %q", absent.ID, first.ID)
	}
}

// TestRunFromEnvironment_GitHubServerURLIsHonoured — GitHub Enterprise sets
// a different server, and a hardcoded github.com would link an enterprise
// run to a page that does not exist.
func TestRunFromEnvironment_GitHubServerURLIsHonoured(t *testing.T) {
	t.Parallel()
	run := runFromEnvironment(envMap(map[string]string{
		envGitHubRepository: "acme/app",
		envGitHubRunID:      "7",
		envGitHubRunAttempt: "1",
		envGitHubServerURL:  "https://ghe.acme.internal/",
	}))
	if want := "https://ghe.acme.internal/acme/app/actions/runs/7"; run.URL != want {
		t.Errorf("run url = %q, want %q", run.URL, want)
	}
	// An unset server falls back to github.com rather than producing a
	// relative URL.
	fallback := runFromEnvironment(envMap(map[string]string{
		envGitHubRepository: "acme/app", envGitHubRunID: "7",
	}))
	if !strings.HasPrefix(fallback.URL, "https://github.com/") {
		t.Errorf("unset server should fall back to github.com; got %q", fallback.URL)
	}
}

// TestRunFromEnvironment_GitLabIsTheSameFewLines: §3.A's claim that there
// are no provider-specific code paths beyond the default.
func TestRunFromEnvironment_GitLabIsTheSameFewLines(t *testing.T) {
	t.Parallel()
	run := runFromEnvironment(envMap(map[string]string{
		envGitLabPipelineID:  "889",
		envGitLabProjectPath: "acme/app",
		envGitLabPipelineURL: "https://gitlab.com/acme/app/-/pipelines/889",
	}))
	if want := "gitlab:acme/app/889"; run.ID != want {
		t.Errorf("run id = %q, want %q", run.ID, want)
	}
	if run.Provider != runProviderGitLab {
		t.Errorf("provider = %q", run.Provider)
	}
	if err := run.Validate(); err != nil {
		t.Errorf("a defaulted gitlab run must be valid: %v", err)
	}
	// A pipeline id with no project path still yields a usable id.
	bare := runFromEnvironment(envMap(map[string]string{envGitLabPipelineID: "889"}))
	if bare.ID != "gitlab:889" {
		t.Errorf("bare gitlab id = %q", bare.ID)
	}
}

// TestRunFromEnvironment_GitHubWinsOverGitLab: both sets present is a
// pathological shell, and the resolution must be deterministic rather than
// map-order dependent.
func TestRunFromEnvironment_GitHubWinsOverGitLab(t *testing.T) {
	t.Parallel()
	run := runFromEnvironment(envMap(map[string]string{
		envGitHubRepository: "acme/app", envGitHubRunID: "1", envGitHubRunAttempt: "1",
		envGitLabPipelineID: "889",
	}))
	if run.Provider != runProviderGitHub {
		t.Errorf("provider = %q, want a deterministic github win", run.Provider)
	}
}

// TestRunFromEnvironment_NoCIIsTheZeroRun — a human at a terminal. NOT an
// error: a promotion that belongs to no run is still a promotion.
func TestRunFromEnvironment_NoCIIsTheZeroRun(t *testing.T) {
	t.Parallel()
	run := runFromEnvironment(envMap(nil))
	if !run.Zero() {
		t.Errorf("no CI environment must yield the zero run; got %+v", run)
	}
	if err := run.Validate(); err != nil {
		t.Errorf("the zero run must be valid: %v", err)
	}
	// A partial environment is not a run: a repository with no run id
	// cannot be joined to anything.
	partial := runFromEnvironment(envMap(map[string]string{envGitHubRepository: "acme/app"}))
	if !partial.Zero() {
		t.Errorf("a partial CI environment must not invent a run; got %+v", partial)
	}
}

// TestRunOptions_FlagsOverrideTheDefault covers the flag layer: an explicit
// id wins, --no-run suppresses, and the contradictions are refused rather
// than guessed.
func TestRunOptions_FlagsOverrideTheDefault(t *testing.T) {
	t.Parallel()
	ci := envMap(map[string]string{
		envGitHubRepository: "acme/app", envGitHubRunID: "12345", envGitHubRunAttempt: "1",
	})

	// No flags: the environment's run.
	defaulted, err := runOptions{}.resolve(ci)
	if err != nil {
		t.Fatal(err)
	}
	if defaulted.ID != "github:acme/app/12345/1" {
		t.Errorf("defaulted id = %q", defaulted.ID)
	}

	// An explicit id overrides, and keeps the detected URL: a pipeline
	// that names its run differently still links to the same page.
	overridden, err := runOptions{ID: "release-train-7"}.resolve(ci)
	if err != nil {
		t.Fatal(err)
	}
	if overridden.ID != "release-train-7" {
		t.Errorf("overridden id = %q", overridden.ID)
	}
	if overridden.URL != defaulted.URL {
		t.Errorf("an id override should keep the detected url; got %q", overridden.URL)
	}

	// An explicit URL overrides too.
	withURL, err := runOptions{ID: "r1", URL: "https://ci.acme/run/1"}.resolve(ci)
	if err != nil {
		t.Fatal(err)
	}
	if withURL.URL != "https://ci.acme/run/1" {
		t.Errorf("url = %q", withURL.URL)
	}

	// --no-run means no run, even inside CI: a human who does not want
	// their shell's stray variables attributed needs a way to say so.
	none, err := runOptions{None: true}.resolve(ci)
	if err != nil {
		t.Fatal(err)
	}
	if !none.Zero() {
		t.Errorf("--no-run must suppress the default; got %+v", none)
	}
}

// TestRunOptions_ContradictionsAreRefused: a pipeline that states both has a
// bug, and guessing which it meant would hide it.
func TestRunOptions_ContradictionsAreRefused(t *testing.T) {
	t.Parallel()
	if _, err := (runOptions{None: true, ID: "r1"}).resolve(envMap(nil)); err == nil {
		t.Error("--no-run with --run-id must be refused")
	} else if !strings.Contains(err.Error(), "--no-run") {
		t.Errorf("the message should name the flags; got %v", err)
	}

	// A URL with no id, outside CI, is detail about a run nobody can
	// find. release.Run.Validate refuses it; the message must name the
	// flag rather than the model.
	_, err := (runOptions{URL: "https://ci.acme/run/1"}).resolve(envMap(nil))
	if err == nil {
		t.Fatal("--run-url without an id must be refused")
	}
	if !strings.Contains(err.Error(), "--run-id") {
		t.Errorf("the message should point at --run-id; got %v", err)
	}
}

// TestRunOptions_ResolveRunReadsTheRealEnvironment pins that resolveRun —
// the entry point a verb calls — is the same rules as resolve, just bound to
// os.Getenv. Two spellings that could drift is the whole reason the logic
// lives in one injected function.
func TestRunOptions_ResolveRunReadsTheRealEnvironment(t *testing.T) {
	// No t.Parallel: this reads the process environment, and asserting
	// agreement with resolve(os.Getenv) must see one consistent snapshot.
	for _, opts := range []runOptions{
		{},
		{ID: "release-train-7"},
		{None: true},
	} {
		fromEnv, envErr := opts.resolveRun()
		injected, injErr := opts.resolve(os.Getenv)
		if (envErr == nil) != (injErr == nil) {
			t.Fatalf("%+v: resolveRun err=%v but resolve err=%v", opts, envErr, injErr)
		}
		if fromEnv != injected {
			t.Errorf("%+v: resolveRun = %+v but resolve(os.Getenv) = %+v", opts, fromEnv, injected)
		}
	}
}

// TestRegisterRunFlags_DeclaresTheWritingVerbSurface: §3.A says every verb
// that WRITES accepts --run-id and --run-url, and `env build --release` is one. The
// flags must actually reach runOptions, or the defaults are decoration.
func TestRegisterRunFlags_DeclaresTheWritingVerbSurface(t *testing.T) {
	t.Parallel()
	var opts runOptions
	flags := pflag.NewFlagSet("cut", pflag.ContinueOnError)
	registerRunFlags(flags, &opts)

	for _, name := range []string{"run-id", "run-url", "no-run"} {
		if flags.Lookup(name) == nil {
			t.Errorf("--%s was not declared", name)
		}
	}
	if err := flags.Parse([]string{"--run-id", "r1", "--run-url", "https://ci/1"}); err != nil {
		t.Fatal(err)
	}
	if opts.ID != "r1" || opts.URL != "https://ci/1" {
		t.Errorf("the flags must bind into runOptions; got %+v", opts)
	}
	if opts.None {
		t.Error("--no-run defaults to false")
	}
}

// TestEnvBuildCmd_AcceptsTheRunFlags pins the wiring end to end: the real
// command declares them, so the run identity a release records is reachable
// from the CLI rather than only from a struct literal in a test.
//
// This was `forge release cut`'s test. That command folded into
// `forge env build <env> --release <v> [--no-build]`, which is now the one
// verb that records a release — so it is the verb that must carry the run
// identity.
func TestEnvBuildCmd_AcceptsTheRunFlags(t *testing.T) {
	t.Parallel()
	cmd := newEnvBuildCmd()
	for _, name := range []string{"run-id", "run-url", "no-run"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("`forge env build` must accept --%s (§3.A: every writing verb does)", name)
		}
	}
}

// TestRunWireFields_OmitsAZeroRun — the `run` member must be ABSENT for a
// run-less write, not present and empty. An empty id on the wire is a claim
// that a run exists, and the server would store it as the join key nothing
// else carries.
func TestRunWireFields_OmitsAZeroRun(t *testing.T) {
	t.Parallel()
	if got := runWireFields(release.Run{}); got != nil {
		t.Errorf("a zero run must render as nil, got %+v", got)
	}
	got := runWireFields(release.Run{ID: "github:acme/app/1/1", URL: "https://x", Provider: "github"})
	if got["id"] != "github:acme/app/1/1" || got["url"] != "https://x" || got["provider"] != "github" {
		t.Errorf("run wire fields = %+v", got)
	}
	// Optional members are omitted rather than sent empty.
	idOnly := runWireFields(release.Run{ID: "manual-1"})
	if _, present := idOnly["url"]; present {
		t.Errorf("an absent url must be omitted; got %+v", idOnly)
	}
	if _, present := idOnly["provider"]; present {
		t.Errorf("an absent provider must be omitted; got %+v", idOnly)
	}
}
