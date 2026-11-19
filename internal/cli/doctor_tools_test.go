package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/doctor"
)

// stubLookup implements binaryLookupFunc against a map. Missing keys
// return exec.ErrNotFound semantics (any non-nil error suffices here
// because evaluateToolCheck only checks `err != nil`).
type stubLookup map[string]string

func (s stubLookup) Lookup(name string) (string, error) {
	if p, ok := s[name]; ok {
		return p, nil
	}
	return "", errors.New("not found")
}

// stubVersion implements versionRunnerFunc. Keyed by binary name.
type stubVersion struct {
	out map[string]string
	err map[string]error
}

func (s stubVersion) Run(_ context.Context, name string, _ []string) (string, error) {
	return s.out[name], s.err[name]
}

// minimalServiceCfg returns a service-kind project with all features
// at their defaults (enabled) — the baseline state for tool-check
// predicates.
func minimalServiceCfg() *config.ProjectConfig {
	return &config.ProjectConfig{Name: "demo", Kind: config.ProjectKindService}
}

// TestRunToolChecks_AllPresent — every required tool resolves on
// PATH and reports a version above its min floor.
func TestRunToolChecks_AllPresent(t *testing.T) {
	checks := []toolCheck{
		{
			Name:        "kcl",
			Required:    requiredAlways,
			VersionArgs: []string{"version"},
			MinVersion:  "0.10.0",
			InstallHints: map[string]string{
				"darwin": "brew install kcl",
			},
		},
	}
	lookup := stubLookup{"kcl": "/usr/local/bin/kcl"}.Lookup
	vr := stubVersion{out: map[string]string{"kcl": "kcl version 0.10.5"}}.Run

	results := runToolChecks(context.Background(), checks, minimalServiceCfg(), "", lookup, vr)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Status != doctor.StatusPass {
		t.Errorf("status = %q, want pass; evidence=%q", r.Status, r.Evidence)
	}
	if !strings.Contains(r.Message, "0.10.5") {
		t.Errorf("message should mention parsed version 0.10.5; got %q", r.Message)
	}
}

// TestRunToolChecks_Missing — required tool not on PATH → fail with
// OS-specific install hint in evidence.
func TestRunToolChecks_Missing(t *testing.T) {
	checks := []toolCheck{
		{
			Name:        "kcl",
			Description: "KCL renderer",
			Required:    requiredAlways,
			VersionArgs: []string{"version"},
			InstallHints: map[string]string{
				"darwin":  "brew install kcl",
				"linux":   "curl ... | bash",
				"windows": "scoop install kcl",
			},
			UpstreamURL: "https://kcl-lang.io/",
		},
	}
	lookup := stubLookup{}.Lookup // empty — every lookup fails
	vr := stubVersion{}.Run

	results := runToolChecks(context.Background(), checks, minimalServiceCfg(), "", lookup, vr)
	r := results[0]
	if r.Status != doctor.StatusFail {
		t.Fatalf("status = %q, want fail", r.Status)
	}
	if !strings.Contains(r.Evidence, "install:") {
		t.Errorf("evidence should include install line; got %q", r.Evidence)
	}
	// Evidence must contain ONE of the OS hints. We can't pin which
	// because runtime.GOOS varies, but every map entry is a valid
	// outcome.
	hits := 0
	for _, want := range []string{"brew install kcl", "curl ... | bash", "scoop install kcl", "https://kcl-lang.io/"} {
		if strings.Contains(r.Evidence, want) {
			hits++
		}
	}
	if hits == 0 {
		t.Errorf("evidence should contain at least one install hint; got %q", r.Evidence)
	}
}

// TestRunToolChecks_BelowMin — present, version below MinVersion → warn.
func TestRunToolChecks_BelowMin(t *testing.T) {
	checks := []toolCheck{
		{
			Name:         "go",
			Required:     requiredAlways,
			VersionArgs:  []string{"version"},
			MinVersion:   "1.22.0",
			InstallHints: map[string]string{"darwin": "brew install go"},
		},
	}
	lookup := stubLookup{"go": "/usr/local/bin/go"}.Lookup
	vr := stubVersion{out: map[string]string{"go": "go version go1.21.0 darwin/arm64"}}.Run

	r := runToolChecks(context.Background(), checks, minimalServiceCfg(), "", lookup, vr)[0]
	if r.Status != doctor.StatusWarn {
		t.Fatalf("status = %q, want warn; evidence=%q", r.Status, r.Evidence)
	}
	if !strings.Contains(r.Evidence, "1.21.0") || !strings.Contains(r.Evidence, "1.22.0") {
		t.Errorf("evidence should mention current + min versions; got %q", r.Evidence)
	}
}

// TestRunToolChecks_NoMinVersion — present, no floor declared → pass
// without version comparison.
func TestRunToolChecks_NoMinVersion(t *testing.T) {
	checks := []toolCheck{{
		Name:        "git",
		Required:    requiredAlways,
		VersionArgs: []string{"--version"},
		// MinVersion empty
	}}
	lookup := stubLookup{"git": "/usr/bin/git"}.Lookup
	vr := stubVersion{out: map[string]string{"git": "git version 2.40.0"}}.Run

	r := runToolChecks(context.Background(), checks, minimalServiceCfg(), "", lookup, vr)[0]
	if r.Status != doctor.StatusPass {
		t.Fatalf("status = %q, want pass", r.Status)
	}
	if !strings.Contains(r.Message, "2.40.0") {
		t.Errorf("message should mention version; got %q", r.Message)
	}
}

// TestRunToolChecks_VersionUnknown — version command fails or
// produces unparsable output. Tool is present, so it's a pass with
// "version unknown" wording.
func TestRunToolChecks_VersionUnknown(t *testing.T) {
	checks := []toolCheck{{
		Name:        "docker",
		Required:    requiredAlways,
		VersionArgs: []string{"--version"},
		MinVersion:  "20.0.0",
	}}
	lookup := stubLookup{"docker": "/usr/bin/docker"}.Lookup
	vr := stubVersion{err: map[string]error{"docker": errors.New("daemon not running")}}.Run

	r := runToolChecks(context.Background(), checks, minimalServiceCfg(), "", lookup, vr)[0]
	if r.Status != doctor.StatusPass {
		t.Fatalf("status = %q, want pass (presence-only)", r.Status)
	}
	if !strings.Contains(r.Message, "version unknown") {
		t.Errorf("message should mention 'version unknown'; got %q", r.Message)
	}
}

// TestRunToolChecks_NotRequired — predicate returns false → skip.
func TestRunToolChecks_NotRequired(t *testing.T) {
	checks := []toolCheck{{
		Name:        "mkcert",
		Required:    func(_ *config.ProjectConfig, _ string) bool { return false },
		VersionArgs: []string{"-version"},
	}}
	// Stubs don't even need to be populated — they should never be
	// consulted because the predicate short-circuits to skip.
	r := runToolChecks(context.Background(), checks, minimalServiceCfg(), "", stubLookup{}.Lookup, stubVersion{}.Run)[0]
	if r.Status != doctor.StatusSkip {
		t.Fatalf("status = %q, want skip", r.Status)
	}
}

// TestInstallHintForOS_KnownAndUnknown — known GOOS picks the
// matching hint; unknown GOOS falls back to the upstream URL.
func TestInstallHintForOS_KnownAndUnknown(t *testing.T) {
	tc := toolCheck{
		Name: "k3d",
		InstallHints: map[string]string{
			"darwin":  "brew install k3d",
			"linux":   "curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash",
			"windows": "choco install k3d",
		},
		UpstreamURL: "https://k3d.io/",
	}
	if got := installHintForOS(tc, "darwin"); got != "brew install k3d" {
		t.Errorf("darwin hint = %q", got)
	}
	if got := installHintForOS(tc, "linux"); !strings.Contains(got, "install.sh") {
		t.Errorf("linux hint = %q", got)
	}
	if got := installHintForOS(tc, "windows"); got != "choco install k3d" {
		t.Errorf("windows hint = %q", got)
	}
	if got := installHintForOS(tc, "freebsd"); got != "see https://k3d.io/" {
		t.Errorf("unknown-OS fallback should use upstream URL; got %q", got)
	}
	// Tool with no upstream URL → generic message.
	if got := installHintForOS(toolCheck{Name: "x"}, "plan9"); !strings.Contains(got, "install x") {
		t.Errorf("no-URL fallback should mention tool name; got %q", got)
	}
}

// TestExtractVersion exercises the regex-free version scraper
// against representative real-world output of each tool we ship.
func TestExtractVersion(t *testing.T) {
	cases := []struct{ in, want string }{
		{"go version go1.22.3 darwin/arm64", "1.22.3"},
		{"kcl version 0.10.5", "0.10.5"},
		{"Client Version: v1.29.4", "1.29.4"},
		{"Docker version 25.0.3, build 4debf41", "25.0.3"},
		{"buf 1.34.0", "1.34.0"},
		{"k3d version v5.6.0", "5.6.0"},
		{"v4.2.3+g43e8b7f", "4.2.3"},
		{"git version 2.40.0", "2.40.0"},
		{"v1.4.4", "1.4.4"},
		{"10.5.0", "10.5.0"},
		{"1.21", "1.21.0"}, // 2-segment → padded to 3
		{"no version here", ""},
	}
	for _, c := range cases {
		got := extractVersion(c.in)
		if got != c.want {
			t.Errorf("extractVersion(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestCompareVersions covers the dotted-int comparator, including
// pre-release suffix handling.
func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.22.0", "1.22.0", 0, true},
		{"1.22.1", "1.22.0", 1, true},
		{"1.21.5", "1.22.0", -1, true},
		{"2.0.0", "1.99.99", 1, true},
		{"1.34.0-rc.1", "1.34.0", 0, true}, // suffix stripped
		{"v1.22.0", "1.22.0", 0, true},
		{"1.22", "1.22.0", 0, true},
		{"garbage", "1.0.0", 0, false},
	}
	for _, c := range cases {
		got, ok := compareVersions(c.a, c.b)
		if ok != c.ok {
			t.Errorf("compareVersions(%q,%q) ok=%v, want %v", c.a, c.b, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// TestRequiredWhen — the feature-gate predicate skips when a feature
// is disabled and requires when it's enabled. Deploy is a stable flag:
// explicit true requires the tool, explicit false skips it — the
// stable nil-default case is covered by the Build variant below.
func TestRequiredWhen(t *testing.T) {
	pred := requiredWhen(func(f config.FeaturesConfig) bool { return f.DeployEnabled() })

	cfgOn := &config.ProjectConfig{
		Features: config.FeaturesConfig{}.With(config.FeatureDeploy, true),
	}
	cfgOff := &config.ProjectConfig{
		Features: config.FeaturesConfig{}.With(config.FeatureDeploy, false),
	}

	if !pred(cfgOn, "") {
		t.Errorf("Deploy=true should require tool")
	}
	if pred(cfgOff, "") {
		t.Errorf("Deploy=false should not require tool")
	}
	if pred(nil, "") {
		t.Errorf("nil cfg should never require tool")
	}

	// Stable opt-out: Build defaults on, can be opted out.
	stablePred := requiredWhen(func(f config.FeaturesConfig) bool { return f.BuildEnabled() })
	cfgBuildOff := &config.ProjectConfig{Features: config.FeaturesConfig{}.With(config.FeatureBuild, false)}
	cfgBuildNil := &config.ProjectConfig{}
	if stablePred(cfgBuildOff, "") {
		t.Errorf("Build=false should not require tool")
	}
	if !stablePred(cfgBuildNil, "") {
		t.Errorf("Build unset (default-on) should require tool")
	}
}

// TestRequiredForMkcert_NoKCL — when the project has no
// deploy/kcl/<env>/main.k, mkcert is not required (we don't want a
// scary "missing mkcert" fail on cli/library-kind projects).
func TestRequiredForMkcert_NoKCL(t *testing.T) {
	tmp := t.TempDir()
	cfg := minimalServiceCfg()
	if requiredForMkcert(cfg, tmp) {
		t.Errorf("mkcert should not be required when no envs are declared")
	}
}

// TestRequiredForMkcert_IngressDisabled — features.ingress=false
// short-circuits to "not required" even if KCL declares mkcert mode.
// Avoids us asking for mkcert on projects that have turned ingress
// off entirely.
func TestRequiredForMkcert_IngressDisabled(t *testing.T) {
	tmp := t.TempDir()
	// Write a KCL file declaring mkcert mode. We don't actually
	// render it — the predicate short-circuits on the feature gate
	// before invoking RenderKCL.
	envDir := filepath.Join(tmp, "deploy", "kcl", "dev")
	if err := os.MkdirAll(envDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(envDir, "main.k"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	// Ingress is experimental and default off — no explicit opt-out
	// needed to exercise the "ingress off" mkcert short-circuit.
	cfg := &config.ProjectConfig{
		Name: "demo",
		Kind: config.ProjectKindService,
	}
	if requiredForMkcert(cfg, tmp) {
		t.Errorf("mkcert must not be required when features.experimental.ingress is off")
	}
}

// TestDefaultToolChecks_PredicatesCoverDocumentedFeatures — sanity
// check that every default tool has a non-nil predicate and a
// non-empty install hint map. Keeps the list well-formed as new
// tools are added.
func TestDefaultToolChecks_PredicatesCoverDocumentedFeatures(t *testing.T) {
	for _, tc := range defaultToolChecks(runtime.GOOS) {
		if tc.Required == nil {
			t.Errorf("tool %q: Required predicate is nil", tc.Name)
		}
		if len(tc.InstallHints) == 0 && tc.UpstreamURL == "" {
			t.Errorf("tool %q: must declare InstallHints or UpstreamURL", tc.Name)
		}
		// Hint values must be non-empty when keys are present.
		for goos, hint := range tc.InstallHints {
			if strings.TrimSpace(hint) == "" {
				t.Errorf("tool %q: empty hint for GOOS=%q", tc.Name, goos)
			}
		}
	}
}

// TestRunToolChecks_DeterministicOrder — results are sorted by name
// so JSON output and human reports are reproducible.
func TestRunToolChecks_DeterministicOrder(t *testing.T) {
	checks := []toolCheck{
		{Name: "zeta", Required: requiredAlways},
		{Name: "alpha", Required: requiredAlways},
		{Name: "middle", Required: requiredAlways},
	}
	lookup := stubLookup{"zeta": "/x", "alpha": "/x", "middle": "/x"}.Lookup
	vr := stubVersion{}.Run
	results := runToolChecks(context.Background(), checks, minimalServiceCfg(), "", lookup, vr)
	if len(results) != 3 {
		t.Fatalf("got %d, want 3", len(results))
	}
	if results[0].Name != "tool: alpha" || results[1].Name != "tool: middle" || results[2].Name != "tool: zeta" {
		t.Errorf("results out of order: %v / %v / %v", results[0].Name, results[1].Name, results[2].Name)
	}
}

// TestRunToolDoctorChecks_SignalFilter — an unrelated signal filter
// suppresses tool checks entirely.
func TestRunToolDoctorChecks_SignalFilter(t *testing.T) {
	tmp := t.TempDir()
	cfg := minimalServiceCfg()
	if got := runToolDoctorChecks(context.Background(), cfg, tmp, "traces"); len(got) != 0 {
		t.Errorf("signal=traces should suppress tool checks; got %d", len(got))
	}
}

// TestRunToolDoctorChecks_LiveSmoke walks the real PATH and reports
// presence/absence of each documented tool. Gated by env var so it's
// opt-in and never fails CI: this is a developer-ergonomics check
// for "did the inventory list still match reality after I bumped a
// dependency?", not a correctness test.
func TestRunToolDoctorChecks_LiveSmoke(t *testing.T) {
	if os.Getenv("FORGE_TEST_TOOLS_LIVE") != "1" {
		t.Skip("set FORGE_TEST_TOOLS_LIVE=1 to run the live PATH smoke test")
	}
	cfg := minimalServiceCfg()
	results := runToolDoctorChecks(context.Background(), cfg, t.TempDir(), "")
	if len(results) == 0 {
		t.Fatal("expected at least one tool check")
	}
	for _, r := range results {
		t.Logf("%s — %s (%s)", r.Name, r.Status, r.Message)
	}
}
