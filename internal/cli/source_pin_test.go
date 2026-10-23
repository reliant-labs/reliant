package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildtarget"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/pkg/release"
)

// writeGoMod writes a project go.mod into a fresh directory.
func writeGoMod(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// taggedCheckout is a one-commit repository tagged with tags, as a Source.
func taggedCheckout(t *testing.T, module, moduleDir string, tags ...string) (*buildtarget.Source, string) {
	t.Helper()
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{"README.md": "x\n"})
	for _, tag := range tags {
		gitIn(t, dir, "tag", tag)
	}
	head := gitOut(t, dir, "rev-parse", "HEAD")
	return &buildtarget.Source{Dir: dir, Module: module, ModuleDir: moduleDir, Commit: head}, head
}

func TestGoModPin(t *testing.T) {
	ctx := context.Background()
	src, head := taggedCheckout(t, "example.com/sib", ".", "v1.2.3")

	t.Run("pseudo-version carries the commit", func(t *testing.T) {
		dir := writeGoMod(t, "module m\n\nrequire example.com/sib v0.0.0-20260101000000-0123456789ab\n")
		p, ok := goModPin(ctx, dir, src)
		if !ok || p.commit != "0123456789ab" || !strings.Contains(p.declared, "require example.com/sib v0.0.0-") {
			t.Fatalf("pin = %+v, %v", p, ok)
		}
		if p.admits(head) {
			t.Errorf("%s does not start with the pinned 0123456789ab", head)
		}
	})
	t.Run("release version resolves its tag in the checkout", func(t *testing.T) {
		dir := writeGoMod(t, "module m\n\nrequire example.com/sib v1.2.3\n")
		p, ok := goModPin(ctx, dir, src)
		if !ok || p.commit != head || !p.admits(head) {
			t.Fatalf("pin = %+v, %v; want the tag's commit %s", p, ok, head)
		}
	})
	t.Run("a tag the checkout lacks is unresolved, not a mismatch", func(t *testing.T) {
		dir := writeGoMod(t, "module m\n\nrequire example.com/sib v1.9.0\n")
		p, ok := goModPin(ctx, dir, src)
		if !ok || p.commit != "" || !strings.Contains(p.unresolved, "v1.9.0") {
			t.Fatalf("pin = %+v, %v", p, ok)
		}
	})
	t.Run("a replace by version pins the replacement", func(t *testing.T) {
		dir := writeGoMod(t, "module m\n\nrequire example.com/sib v1.9.0\n\nreplace example.com/sib => example.com/fork v1.2.3\n")
		p, ok := goModPin(ctx, dir, src)
		if !ok || p.commit != head || !strings.Contains(p.declared, "replace example.com/sib => example.com/fork v1.2.3") {
			t.Fatalf("pin = %+v, %v", p, ok)
		}
	})
	t.Run("a replace by directory pins nothing", func(t *testing.T) {
		dir := writeGoMod(t, "module m\n\nrequire example.com/sib v1.2.3\n\nreplace example.com/sib => ../sib\n")
		if p, ok := goModPin(ctx, dir, src); ok {
			t.Fatalf("Go builds the directory, so there is no commit to hold the checkout to; got %+v", p)
		}
	})
	t.Run("a module go.mod does not require pins nothing", func(t *testing.T) {
		dir := writeGoMod(t, "module m\n\nrequire example.com/other v1.0.0\n")
		if p, ok := goModPin(ctx, dir, src); ok {
			t.Fatalf("got %+v", p)
		}
	})
	t.Run("a module in a subdirectory tags with its directory", func(t *testing.T) {
		sub, subHead := taggedCheckout(t, "example.com/sib/tool", "tool", "tool/v0.4.0")
		dir := writeGoMod(t, "module m\n\nrequire example.com/sib/tool v0.4.0\n")
		p, ok := goModPin(ctx, dir, sub)
		if !ok || p.commit != subHead {
			t.Fatalf("pin = %+v, %v; want tool/v0.4.0's commit", p, ok)
		}
	})
}

func TestModuleTagPrefix(t *testing.T) {
	for _, tc := range []struct{ module, dir, want string }{
		{"example.com/r", ".", ""},
		{"example.com/r/sub", "sub", "sub/"},
		{"example.com/r/v2", "v2", ""},
		{"example.com/r/sub/v2", "sub/v2", "sub/"},
		{"example.com/r/sub/v2", "sub", "sub/"},
	} {
		got := moduleTagPrefix(&buildtarget.Source{Module: tc.module, ModuleDir: tc.dir})
		if got != tc.want {
			t.Errorf("moduleTagPrefix(%s in %s) = %q, want %q", tc.module, tc.dir, got, tc.want)
		}
	}
}

// refTableResolver answers Resolve from a table keyed by ref.
type refTableResolver map[string]gitsource.Resolution

func (s refTableResolver) Resolve(_ context.Context, src gitsource.Source) (gitsource.Resolution, error) {
	r, ok := s[src.Ref]
	if !ok {
		return gitsource.Resolution{}, errors.New("no such ref")
	}
	return r, nil
}

func TestGitSourcePins(t *testing.T) {
	commit := strings.Repeat("c", 40)
	check := releaseSourceCheck{
		buildSources: buildSourceDeclarations(nil, &KCLEntities{Frontends: []FrontendEntity{
			{Name: "web", Source: &config.GitSource{Repo: "https://github.com/example/sib.git", Ref: commit}},
			{Name: "docs", Source: &config.GitSource{Repo: "github.com/example/sib", Ref: "v1.0.0"}},
			{Name: "local", Source: &config.GitSource{Repo: "github.com/example/sib", Ref: "main"}},
			{Name: "gone", Source: &config.GitSource{Repo: "github.com/example/sib", Ref: "nope"}},
			{Name: "other", Source: &config.GitSource{Repo: "github.com/example/other", Ref: commit}},
		}}),
		resolver: func() (pinResolver, error) {
			return refTableResolver{
				"v1.0.0": {Commit: strings.Repeat("d", 40)},
				"main":   {Commit: strings.Repeat("e", 40), Overridden: true},
			}, nil
		},
	}
	pins, err := check.gitSourcePins(context.Background(), &buildtarget.Source{Repo: "github.com/example/sib"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 3 {
		t.Fatalf("want web (sha ref), docs (resolved), gone (unresolved); the override and the other repo are not pins. got %+v", pins)
	}
	for _, p := range pins {
		switch {
		case strings.Contains(p.declared, `"web"`):
			if p.commit != commit {
				t.Errorf("a commit ref pins itself: %+v", p)
			}
		case strings.Contains(p.declared, `"docs"`):
			if p.commit != strings.Repeat("d", 40) {
				t.Errorf("a tag ref pins what the release's resolver resolves it to: %+v", p)
			}
		case strings.Contains(p.declared, `"gone"`):
			if p.commit != "" || p.unresolved == "" {
				t.Errorf("an unresolvable ref is unresolved: %+v", p)
			}
		default:
			t.Errorf("unexpected pin %+v", p)
		}
	}
}

// The preflight fires for every build sealed under a release version (a cut,
// --release). A deploy that reuses an existing release builds nothing, so it
// has no build to guard. A plain build is not a release and is never refused.
func TestPreflightReleaseSources_EveryReleaseBoundBuild(t *testing.T) {
	root := t.TempDir()
	sib := filepath.Join(root, "sib")
	initGitRepo(t, sib, map[string]string{"go.mod": "module example.com/sib\n"})
	project := filepath.Join(root, "acme")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"),
		[]byte("module m\n\nrequire example.com/sib v0.0.0-20260101000000-0123456789ab\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ents := &KCLEntities{Workloads: []WorkloadEntity{{
		Name:  "sib",
		Build: BuildConfigEntity{Type: "shell", Shell: &ShellBuild{Cmd: "true", Cwd: "../sib"}},
	}}}
	ctx := context.Background()

	if err := preflightReleaseSources(ctx, project, ents, buildOptions{env: "prod", release: "v1"}); err == nil || !strings.Contains(err.Error(), "0123456789ab") {
		t.Errorf("cut: an off-pin sibling must be refused, got %v", err)
	}
	if err := preflightReleaseSources(ctx, project, ents, buildOptions{env: "prod", push: true}); err != nil {
		t.Errorf("a push is not a release and must not be refused: %v", err)
	}
}

// The hosted ledger carries an image's built_from on the wire, and reads it
// back, so a control plane that stores it round-trips it.
func TestHostedWireCarriesBuiltFrom(t *testing.T) {
	built := &release.BuildSource{Repo: "github.com/example/sib", Commit: strings.Repeat("ab", 20), Dirty: true}
	r := release.Release{Version: "v1", Artifacts: map[string]release.Artifact{
		"registry.example.com/sib": {
			Kind: release.KindOCI, Mode: release.ModeShared,
			Digests:   map[string]string{release.SharedVariant: "sha256:" + strings.Repeat("5a", 32)},
			BuiltFrom: built,
		},
	}}
	rows := releaseToWire(r)
	raw, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"builtFrom":{"repo":"github.com/example/sib","commit":"`+built.Commit+`","dirty":true}`) {
		t.Errorf("wire artifact does not carry builtFrom in protojson's camelCase: %s", raw)
	}
	back, err := releaseFromWire(wireRelease{Version: "v1", Artifacts: rows})
	if err != nil {
		t.Fatal(err)
	}
	if got := back.Artifacts["registry.example.com/sib"].BuiltFrom; got == nil || *got != *built {
		t.Errorf("built_from did not round-trip: %+v", got)
	}
}

// The fix command names the checkout relative to where forge runs, with
// forward slashes on every OS, so it pastes into any shell as printed.
func TestGitDirArg(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "acme")
	sib := filepath.Join(root, "sib")
	for _, d := range []string{project, sib} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(project)
	real, err := filepath.EvalSymlinks(sib)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitDirArg(real); got != "../sib" {
		t.Errorf("gitDirArg(%s) = %q, want ../sib", real, got)
	}
}

func TestCwdOutsideProject(t *testing.T) {
	// A real absolute path on this OS: `/w/acme` has no volume on Windows,
	// so it is not absolute there and joins under the project instead.
	project := filepath.Join(t.TempDir(), "acme")
	for cwd, want := range map[string]bool{
		"":                                false,
		".":                               false,
		"tools/gen":                       false,
		"..foo":                           false,
		"../reliant":                      true,
		"..":                              true,
		filepath.Join(project, "x"):       false,
		filepath.Join(project, "..", "y"): true,
	} {
		if got := cwdOutsideProject(project, cwd); got != want {
			t.Errorf("cwdOutsideProject(%q) = %v, want %v", cwd, got, want)
		}
	}
}

func TestAgreedPin(t *testing.T) {
	full := "0123456789ab" + strings.Repeat("f", 28)
	for _, tc := range []struct {
		name   string
		pins   []sourcePin
		target string
		ok     bool
	}{
		{"none", nil, "", true},
		{"prefix and full agree", []sourcePin{{commit: "0123456789ab"}, {commit: full}}, full, true},
		{"full then prefix agree", []sourcePin{{commit: full}, {commit: "0123456789ab"}}, full, true},
		{"unresolved ignored", []sourcePin{{unresolved: "x"}, {commit: full}}, full, true},
		{"disagree", []sourcePin{{commit: full}, {commit: strings.Repeat("a", 40)}}, "", false},
	} {
		target, ok := agreedPin(tc.pins)
		if target != tc.target || ok != tc.ok {
			t.Errorf("%s: agreedPin = (%q, %v), want (%q, %v)", tc.name, target, ok, tc.target, tc.ok)
		}
	}
}

func TestSourceRefusal(t *testing.T) {
	head := strings.Repeat("1", 40)
	use := sourceUse{src: &buildtarget.Source{Dir: "/w/sib", Repo: "github.com/example/sib", Commit: head}, services: []string{"api", "worker"}}

	if block := sourceRefusal(use, []sourcePin{{declared: "go.mod", commit: head[:12]}}, false); block != "" {
		t.Errorf("a clean checkout at its pin passes; got:\n%s", block)
	}
	if block := sourceRefusal(use, nil, false); block != "" {
		t.Errorf("a clean checkout nothing pins is noted, not refused; got:\n%s", block)
	}

	block := sourceRefusal(use, []sourcePin{
		{declared: "go.mod: require example.com/sib v0", commit: strings.Repeat("2", 12)},
		{declared: `deploy/kcl: frontend "web" GitSource ref x`, commit: strings.Repeat("3", 40)},
	}, false)
	if !strings.Contains(block, "different commits") || strings.Contains(block, "checkout --detach") {
		t.Errorf("pins that disagree cannot both be checked out; the fix is to reconcile them:\n%s", block)
	}
	if !strings.Contains(block, "api, worker") {
		t.Errorf("the block names every ShellBuild in the checkout once:\n%s", block)
	}

	block = sourceRefusal(use, []sourcePin{{declared: "go.mod: require example.com/sib v1.9.0", unresolved: "tag v1.9.0 is not in this checkout"}}, false)
	if !strings.Contains(block, "fetch --tags origin") {
		t.Errorf("an unresolved tag's fix is to fetch it:\n%s", block)
	}

	block = sourceRefusal(use, []sourcePin{{declared: "go.mod", commit: strings.Repeat("2", 12)}}, true)
	if !strings.Contains(block, "built from:") {
		t.Errorf("at the cut the block names the RECORDED checkout:\n%s", block)
	}
}

// The project's own checkout is the release's own commit — release
// provenance records it, dirty or not. The guard judges siblings only, and a
// sibling nothing pins is noted rather than refused.
func TestJudgeScope(t *testing.T) {
	check := releaseSourceCheck{projectDir: writeGoMod(t, "module m\n"), version: "v1"}
	notes, err := check.judge(context.Background(), []sourceUse{
		{src: &buildtarget.Source{Dir: "/w/acme", Commit: strings.Repeat("1", 40), Dirty: true, Project: true}, services: []string{"tool"}},
		{src: &buildtarget.Source{Dir: "/w/lib", Commit: strings.Repeat("2", 40), Module: "example.com/lib"}, services: []string{"lib"}},
	})
	if err != nil {
		t.Fatalf("neither the project's own checkout nor an unpinned clean sibling is refused: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "nothing in this project pins its module example.com/lib") {
		t.Errorf("an unpinned sibling is noted: %q", notes)
	}

	_, err = check.judge(context.Background(), []sourceUse{
		{src: &buildtarget.Source{Dir: "/w/lib", Commit: strings.Repeat("2", 40), Dirty: true}, services: []string{"lib"}},
	})
	if err == nil || !strings.Contains(err.Error(), "--release v1") {
		t.Errorf("a dirty sibling is refused even when nothing pins it: %v", err)
	}
}
