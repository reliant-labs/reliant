package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/changesdecl"
	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/internal/kclplugin"
	"github.com/reliant-labs/forge/pkg/release"
)

// docs/proposals/changes-from-sources.md: a frontend built from a pinned
// source carries that source's changes.yaml into the env's shape.

const sourceChangesYAML = `version: 1
changes:
  pending-provider-copy:
    description: The banner's copy
    type: bool
    default: current
    variants:
      current: false
      outcome-first: true
`

// fakeChangeSources resolves a pin to a directory per repo@ref, records every
// call, and serves a changes.yaml body (or none) per directory.
type fakeChangeSources struct {
	t       *testing.T
	root    string
	commits map[string]string // repo@ref → commit the ref resolves to
	files   map[string]string // repo@ref-or-commit → changes.yaml ("" = none)
	calls   []string
	err     error
	over    map[string]bool // repo → overridden
}

func (f *fakeChangeSources) Resolve(_ context.Context, src gitsource.Source) (gitsource.Resolution, error) {
	key := src.Repo + "@" + src.Ref
	f.calls = append(f.calls, key)
	if f.err != nil {
		return gitsource.Resolution{}, f.err
	}
	commit := f.commits[key]
	if commit == "" {
		commit = src.Ref
	}
	dir := filepath.Join(f.root, strings.NewReplacer("/", "_", "@", "_").Replace(src.Repo+"@"+commit))
	if f.over[src.Repo] {
		dir = filepath.Join(f.root, "override")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if body := f.files[src.Repo+"@"+commit]; body != "" {
		if err := os.WriteFile(filepath.Join(dir, changesdecl.FileName), []byte(body), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	if f.over[src.Repo] {
		return gitsource.Resolution{Dir: dir, Overridden: true}, nil
	}
	return gitsource.Resolution{Dir: dir, Commit: commit}, nil
}

func useChangeSources(t *testing.T, f *fakeChangeSources) {
	t.Helper()
	prev := newChangeSourceResolver
	newChangeSourceResolver = func(string, io.Writer) (changeSourceResolver, error) { return f, nil }
	t.Cleanup(func() { newChangeSourceResolver = prev })
}

func sourcedFrontend(name, repo, ref string) FrontendEntity {
	return FrontendEntity{Name: name, Type: "vite", Source: &config.GitSource{Repo: repo, Ref: ref, Subdir: "frontends/web"}}
}

func writeRootChanges(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, changesdecl.FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const reliantRepo = "github.com/reliant-labs/reliant"

var pinSHA = strings.Repeat("a", 40)

func TestEnvShapeChanges_CarriesAPinnedSourcesDeclaration(t *testing.T) {
	src := &fakeChangeSources{t: t, root: t.TempDir(), files: map[string]string{reliantRepo + "@" + pinSHA: sourceChangesYAML}}
	useChangeSources(t, src)
	project := t.TempDir()

	got, origins, err := envShapeChanges(context.Background(), &strings.Builder{}, project,
		&KCLEntities{Frontends: []FrontendEntity{sourcedFrontend("reliant-web", reliantRepo, pinSHA)}})
	if err != nil {
		t.Fatal(err)
	}
	c, ok := got["pending-provider-copy"]
	if !ok || len(got) != 1 || c.Type != release.ChangeTypeBool || c.Default != "current" || string(c.Variants["outcome-first"]) != "true" {
		t.Fatalf("changes = %+v, want the source's pending-provider-copy", got)
	}
	if want := map[string]string{"pending-provider-copy": reliantRepo + "@" + pinSHA[:12]}; !reflect.DeepEqual(origins, want) {
		t.Errorf("origins = %v, want %v", origins, want)
	}
	// The repository root is read, not the frontend's subdir.
	if want := []string{reliantRepo + "@" + pinSHA}; !reflect.DeepEqual(src.calls, want) {
		t.Errorf("resolved %v, want %v (no subdir, one resolution)", src.calls, want)
	}
}

// The byte-identical guarantee: without a sourced frontend (or with one whose
// source declares nothing, or that opted out) the shape's changes are exactly
// the root's, and its encoding and digest do not move.
func TestEnvShapeChanges_ByteIdenticalWithoutASourcedDeclaration(t *testing.T) {
	for _, rootBody := range []string{"", shapeChangesYAML} {
		project := t.TempDir()
		if rootBody != "" {
			writeRootChanges(t, project, rootBody)
		}
		before, err := changesdecl.Load(project)
		if err != nil {
			t.Fatal(err)
		}
		want := release.Shape{Kind: release.EnvPersistent, Changes: before.Shape()}
		wantJSON, err := json.Marshal(want)
		if err != nil {
			t.Fatal(err)
		}
		wantDigest := release.SpecDigest(wantJSON)

		optOut := false
		silent := &fakeChangeSources{t: t, root: t.TempDir()} // no source declares anything
		cases := map[string]struct {
			frontends []FrontendEntity
			resolves  bool
		}{
			"no frontends":     {nil, false},
			"in-repo frontend": {[]FrontendEntity{{Name: "web", Path: "frontends/web"}}, false},
			"opted-out source": {[]FrontendEntity{func() FrontendEntity {
				f := sourcedFrontend("rw", reliantRepo, pinSHA)
				f.ChangesFromSource = &optOut
				return f
			}()}, false},
			"source with no file": {[]FrontendEntity{sourcedFrontend("rw", reliantRepo, pinSHA)}, true},
		}
		for name, tc := range cases {
			silent.calls = nil
			useChangeSources(t, silent)
			got, origins, err := envShapeChanges(context.Background(), &strings.Builder{}, project, &KCLEntities{Frontends: tc.frontends})
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if origins != nil {
				t.Errorf("%s: origins = %v, want none", name, origins)
			}
			shape := release.Shape{Kind: release.EnvPersistent, Changes: got}
			gotJSON, err := json.Marshal(shape)
			if err != nil {
				t.Fatal(err)
			}
			gotDigest := release.SpecDigest(gotJSON)
			if string(gotJSON) != string(wantJSON) || gotDigest != wantDigest {
				t.Errorf("%s (root declared: %v): shape moved\n got %s %s\nwant %s %s", name, rootBody != "", gotJSON, gotDigest, wantJSON, wantDigest)
			}
			if resolved := len(silent.calls) > 0; resolved != tc.resolves {
				t.Errorf("%s: resolved sources = %v (%v), want %v", name, resolved, silent.calls, tc.resolves)
			}
		}
	}
}

// The commit the release pins wins over what the declared ref resolves to
// now: a deploy of a recorded release reads the bytes that release ships.
func TestEnvShapeChanges_ReadsAtTheReleasesPinnedCommit(t *testing.T) {
	pinned, moved := strings.Repeat("b", 40), strings.Repeat("c", 40)
	src := &fakeChangeSources{t: t, root: t.TempDir(),
		commits: map[string]string{reliantRepo + "@main": moved},
		files:   map[string]string{reliantRepo + "@" + pinned: sourceChangesYAML},
	}
	useChangeSources(t, src)
	ctx := withChangeSourcePins(context.Background(), map[string]release.Source{"reliant-web": {Repo: reliantRepo, Ref: "main", Commit: pinned}})

	got, _, err := envShapeChanges(ctx, &strings.Builder{}, t.TempDir(), &KCLEntities{Frontends: []FrontendEntity{sourcedFrontend("reliant-web", reliantRepo, "main")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got["pending-provider-copy"]; !ok {
		t.Fatalf("changes = %v, want the pinned commit's declaration (the moved ref declares none)", got)
	}
	if want := []string{reliantRepo + "@" + pinned}; !reflect.DeepEqual(src.calls, want) {
		t.Errorf("resolved %v, want %v (never resolve the moving declared ref)", src.calls, want)
	}

	// A legacy pin with no repository still resolves its exact commit.
	src.calls = nil
	ctx = withChangeSourcePins(context.Background(), map[string]release.Source{"reliant-web": {Commit: moved}})
	if _, _, err := envShapeChanges(ctx, &strings.Builder{}, t.TempDir(), &KCLEntities{Frontends: []FrontendEntity{sourcedFrontend("reliant-web", reliantRepo, "main")}}); err != nil {
		t.Fatal(err)
	}
	if len(src.calls) != 1 {
		t.Errorf("resolved %v, want one resolution", src.calls)
	}
}

func TestEnvShapeChanges_RejectsMismatchedPinRepository(t *testing.T) {
	for _, overridden := range []bool{false, true} {
		src := &fakeChangeSources{t: t, root: t.TempDir(), over: map[string]bool{reliantRepo: overridden}}
		useChangeSources(t, src)
		otherRepo := "github.com/acme/other"
		ctx := withChangeSourcePins(context.Background(), map[string]release.Source{
			"reliant-web": {Repo: otherRepo, Commit: pinSHA},
		})
		_, _, err := envShapeChanges(ctx, &strings.Builder{}, t.TempDir(),
			&KCLEntities{Frontends: []FrontendEntity{sourcedFrontend("reliant-web", reliantRepo, "main")}})
		for _, want := range []string{`frontend "reliant-web"`, otherRepo, reliantRepo, "align"} {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("override=%v: error = %v, want %q", overridden, err, want)
			}
		}
		if len(src.calls) != 0 {
			t.Fatalf("resolved %v despite mismatched repository", src.calls)
		}
	}
}

// A local override reads the working copy, as the build does, and says so.
func TestEnvShapeChanges_LocalOverride(t *testing.T) {
	src := &fakeChangeSources{t: t, root: t.TempDir(), over: map[string]bool{reliantRepo: true}}
	if err := os.MkdirAll(filepath.Join(src.root, "override"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeRootChanges(t, filepath.Join(src.root, "override"), sourceChangesYAML)
	useChangeSources(t, src)
	ctx := withChangeSourcePins(context.Background(), map[string]release.Source{"reliant-web": {Repo: reliantRepo, Commit: pinSHA}})

	_, origins, err := envShapeChanges(ctx, &strings.Builder{}, t.TempDir(), &KCLEntities{Frontends: []FrontendEntity{sourcedFrontend("reliant-web", reliantRepo, pinSHA)}})
	if err != nil {
		t.Fatal(err)
	}
	if origins["pending-provider-copy"] != reliantRepo+" (local override)" {
		t.Errorf("origins = %v, want the override named", origins)
	}
	if len(src.calls) != 1 {
		t.Errorf("resolved %v: an override is not re-resolved at the pin", src.calls)
	}
}

// Each file is validated on its own, and its errors name where it came from.
func TestEnvShapeChanges_InvalidSourceNamesItsOrigin(t *testing.T) {
	src := &fakeChangeSources{t: t, root: t.TempDir(), files: map[string]string{reliantRepo + "@" + pinSHA: "version: 1\nchanges:\n  k:\n    type: int\n"}}
	useChangeSources(t, src)
	_, _, err := envShapeChanges(context.Background(), &strings.Builder{}, t.TempDir(), &KCLEntities{Frontends: []FrontendEntity{sourcedFrontend("reliant-web", reliantRepo, pinSHA)}})
	if err == nil {
		t.Fatal("an invalid source changes.yaml was accepted")
	}
	for _, want := range []string{`frontend "reliant-web"`, reliantRepo + "@" + pinSHA[:12] + ":changes.yaml:4:"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}

	src.err = errors.New("offline")
	if _, _, err := envShapeChanges(context.Background(), &strings.Builder{}, t.TempDir(), &KCLEntities{Frontends: []FrontendEntity{sourcedFrontend("reliant-web", reliantRepo, pinSHA)}}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Errorf("an unresolvable source = %v, want the resolver's error", err)
	}
}

// Two frontends on one pin read one file, once; an identical declaration in
// the root and a source is kept once (the description aside).
func TestEnvShapeChanges_IdenticalDeclarationsAreKeptOnce(t *testing.T) {
	src := &fakeChangeSources{t: t, root: t.TempDir(), files: map[string]string{reliantRepo + "@" + pinSHA: sourceChangesYAML}}
	useChangeSources(t, src)
	project := t.TempDir()
	writeRootChanges(t, project, strings.Replace(sourceChangesYAML, "The banner's copy", "A mirror kept in step", 1))
	var warn strings.Builder

	got, origins, err := envShapeChanges(context.Background(), &warn, project, &KCLEntities{Frontends: []FrontendEntity{
		sourcedFrontend("reliant-web", reliantRepo, pinSHA), sourcedFrontend("forge-demo", reliantRepo, pinSHA),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got["pending-provider-copy"].Description != "A mirror kept in step" {
		t.Errorf("changes = %+v, want the root's one declaration", got)
	}
	if origins != nil {
		t.Errorf("origins = %v: the kept declaration is the root's", origins)
	}
	if warn.Len() != 0 {
		t.Errorf("an identical declaration warned: %s", warn.String())
	}
}

func shapeChange(def string, variants map[string]string) release.ShapeChange {
	vs := map[string]json.RawMessage{}
	for k, v := range variants {
		vs[k] = json.RawMessage(v)
	}
	return release.ShapeChange{Type: release.ChangeTypeBool, Default: def, Variants: vs}
}

// A key two declarations define differently is the warn-first rule
// changes-source-conflict: while it warns the first declaration ships and the
// conflict is printed; when it gates the shape is refused.
func TestMergeShapeChanges_ConflictWarnsThenRefuses(t *testing.T) {
	root := changesDeclaration{Origin: changesdecl.FileName, Changes: map[string]release.ShapeChange{
		"k": shapeChange("off", map[string]string{"off": "false", "on": "true"}),
	}}
	source := changesDeclaration{Origin: reliantRepo + "@" + pinSHA[:12], Changes: map[string]release.ShapeChange{
		"k":     shapeChange("off", map[string]string{"off": "false", "on": "1"}),
		"other": shapeChange("a", map[string]string{"a": "true"}),
	}}

	var warn strings.Builder
	warning := lint.RuleGrade{Rule: lint.RuleChangesSourceConflict, IntroducedIn: "v1.12.0", GatesFrom: "v1.13.0"}
	got, origins, err := mergeShapeChanges(&warn, warning, []changesDeclaration{root, source})
	if err != nil {
		t.Fatalf("a warning conflict refused: %v", err)
	}
	if string(got["k"].Variants["on"]) != "true" || len(got) != 2 || origins["k"] != changesdecl.FileName || origins["other"] != source.Origin {
		t.Errorf("merged = %+v (origins %v), want the root's k and the source's other", got, origins)
	}
	for _, want := range []string{"[changes-source-conflict]", `change "k" is declared by changes.yaml and by ` + source.Origin, "variant on is true vs 1", "gates from v1.13.0", "lint.rules: {changes-source-conflict: error}"} {
		if !strings.Contains(warn.String(), want) {
			t.Errorf("warning does not say %q:\n%s", want, warn.String())
		}
	}

	gating := lint.RuleGrade{Rule: lint.RuleChangesSourceConflict, Gates: true}
	if _, _, err := mergeShapeChanges(&strings.Builder{}, gating, []changesDeclaration{root, source}); err == nil ||
		!strings.Contains(err.Error(), `change "k" is declared by changes.yaml and by `+source.Origin) {
		t.Errorf("a gating conflict = %v, want it refused naming both origins", err)
	}
}

// The rule warns for the release that ships it and gates from the next.
func TestChangesSourceConflict_RollsOutWarnFirst(t *testing.T) {
	g := lint.GradeRule(nil, lint.RuleChangesSourceConflict)
	if g.IntroducedIn == "" || g.GatesFrom == "" {
		t.Fatalf("%s has no warn-first rollout row: %+v", lint.RuleChangesSourceConflict, g)
	}
	at := func(pin string) lint.RuleGrade {
		return lint.GradeRule(&config.ProjectConfig{ForgeVersion: pin}, lint.RuleChangesSourceConflict)
	}
	if at(g.IntroducedIn).Gates {
		t.Errorf("gates at %s, the release that ships it", g.IntroducedIn)
	}
	if !at(g.GatesFrom).Gates {
		t.Errorf("does not gate at %s", g.GatesFrom)
	}
}

// The merged set is held to release.MaxShapeChanges, naming who declares how many.
func TestMergeShapeChanges_CapsTheMergedTotal(t *testing.T) {
	half := func(prefix string) map[string]release.ShapeChange {
		out := map[string]release.ShapeChange{}
		for i := 0; i < release.MaxShapeChanges/2+1; i++ {
			out[prefix+strings.Repeat("x", i%3)+string(rune('a'+i%26))+strings.Repeat("y", i/26)] = shapeChange("a", map[string]string{"a": "true"})
		}
		return out
	}
	root, src := half("r"), half("s")
	_, _, err := mergeShapeChanges(&strings.Builder{}, lint.RuleGrade{Gates: true}, []changesDeclaration{
		{Origin: changesdecl.FileName, Changes: root}, {Origin: "src@1", Changes: src},
	})
	if err == nil || !strings.Contains(err.Error(), "at most 200") || !strings.Contains(err.Error(), "src@1 declares") {
		t.Errorf("an over-cap merge = %v, want it refused with the counts", err)
	}
}

// End to end through the KCL render: an env with a frontend built from a
// pinned source (here overridden to a local working copy, so no network)
// declares the changes that source's changes.yaml declares; with
// changes_from_source = False it declares none.
func TestRenderEnvShape_CarriesAPinnedSourcesChanges(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates KCL; runs in task test")
	}
	kclplugin.Register()
	for _, optOut := range []bool{false, true} {
		dir := writeRenderStdoutProject(t)
		t.Chdir(dir)
		work := t.TempDir()
		writeRootChanges(t, work, sourceChangesYAML)
		if err := os.MkdirAll(filepath.Join(work, "web"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".forge", gitsource.OverridesFileName), []byte("sources:\n  github.com/acme/shop: "+work+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		mainK := filepath.Join(dir, "deploy", "kcl", "prod", "main.k")
		raw, err := os.ReadFile(mainK)
		if err != nil {
			t.Fatal(err)
		}
		opt := ""
		if optOut {
			opt = "\n        changes_from_source = False"
		}
		frontend := `    frontends = [forge.Frontend {
        name = "store", type = "vite", port = 5173
        source = forge.GitSource {repo = "github.com/acme/shop", ref = "v1.0.0", subdir = "web"}
        runtime = forge.OnHost {}` + opt + `
    }]
}`
		patched := strings.Replace(string(raw), "    ]]\n}", "    ]]\n"+frontend, 1)
		if patched == string(raw) {
			t.Fatal("fixture main.k changed shape; cannot add the frontend")
		}
		if err := os.WriteFile(mainK, []byte(patched), 0o644); err != nil {
			t.Fatal(err)
		}

		doc, err := renderEnvShape(context.Background(), io.Discard, dir, "prod")
		if err != nil {
			t.Fatalf("render (opt out %v): %v", optOut, err)
		}
		_, carried := doc.Shape.Changes["pending-provider-copy"]
		if carried == optOut {
			t.Errorf("opt out %v: shape changes = %v", optOut, doc.Shape.Changes)
		}
		if optOut {
			continue
		}
		var summary strings.Builder
		writeShapeSummary(&summary, doc)
		if !strings.Contains(summary.String(), "pending-provider-copy [bool]: 2 variant(s), default current (from github.com/acme/shop (local override))") {
			t.Errorf("the summary does not name the change's source:\n%s", summary.String())
		}
	}
}
