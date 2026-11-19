package benefits

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite docs/benefits.md and the README.md benefits block from benefits.yaml")

// repoRoot is the forge checkout, relative to this package's directory (the
// working directory of `go test`).
const repoRoot = "../.."

// The embedded registry is the one every renderer and the command read, so a
// schema violation in it must fail here, with every violation listed.
func TestEmbeddedRegistryValidates(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Benefits) == 0 {
		t.Fatal("benefits.yaml has no benefits")
	}
	// The ten categories, in the order every rendering follows.
	var got []string
	for _, c := range r.Categories {
		got = append(got, c.ID)
	}
	want := "build data test dev guardrails security ship host run agents"
	if strings.Join(got, " ") != want {
		t.Errorf("categories = %q, want %q", strings.Join(got, " "), want)
	}
}

// Every evidence kind is exercised by the embedded registry, so
// internal/cli's TestBenefitsEvidenceExists resolves each kind at least once
// rather than passing vacuously on a kind nobody uses.
func TestEmbeddedRegistryUsesEveryEvidenceKind(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, b := range r.Benefits {
		for _, e := range b.Evidence {
			k, _ := e.Kind()
			seen[k] = true
		}
	}
	for _, k := range []string{KindCommand, KindAnalyzer, KindMarker, KindSkill, KindLibrary, KindPath, KindExternal} {
		if !seen[k] {
			t.Errorf("no entry uses evidence kind %q", k)
		}
	}
}

const validCategories = `
categories:
  - {id: build, title: Build, intro: Builds things.}
  - {id: data, title: Data, intro: Stores things.}
`

func TestParseRejectsBadRegistries(t *testing.T) {
	ok := `
  - id: good
    category: build
    title: A good entry
    claim: It is good.
    availability: forge
    evidence:
      - path: go.mod
`
	if _, err := Parse([]byte(validCategories + "benefits:" + ok)); err != nil {
		t.Fatalf("the baseline registry must parse: %v", err)
	}

	cases := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name:    "unknown field",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, headlien: true, evidence: [{path: go.mod}]}\n",
			wantErr: "field headlien not found",
		},
		{
			name:    "unknown evidence key",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{file: go.mod}]}\n",
			wantErr: "field file not found",
		},
		{
			name:    "duplicate id",
			yaml:    validCategories + "benefits:" + ok + ok,
			wantErr: `benefits[1] "good": duplicate benefit id`,
		},
		{
			name:    "id not kebab-case",
			yaml:    validCategories + "benefits:\n  - {id: Not_Kebab, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "Not_Kebab": id must be kebab-case`,
		},
		{
			name:    "id collides with a category anchor",
			yaml:    validCategories + "benefits:\n  - {id: data, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "data": id collides with a docs/benefits.md heading anchor`,
		},
		{
			name:    "unknown category",
			yaml:    validCategories + "benefits:\n  - {id: x, category: shipping, title: T, claim: C, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": category "shipping" is not a categories[].id`,
		},
		{
			name:    "empty claim",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: '  ', availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": claim is empty`,
		},
		{
			name:    "empty title",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, claim: C, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": title is empty`,
		},
		{
			name:    "title too long",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: one two three four five six seven, claim: C, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": title has 7 words; at most 6`,
		},
		{
			name:    "bad status",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, status: shipped, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": status "shipped" is not one of enforced, recorded, in-progress`,
		},
		{
			name:    "missing availability",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": availability "" is not one of forge, hosted, reliant`,
		},
		{
			name:    "in-progress headline",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, status: in-progress, headline: true, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": an in-progress entry may not be a headline`,
		},
		{
			name:    "no evidence",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge}\n",
			wantErr: `benefits[0] "x": no evidence`,
		},
		{
			name:    "evidence item with two keys",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod, skill: db}]}\n",
			wantErr: `benefits[0] "x": evidence[0]: 2 keys set (skill, path); an item has exactly one`,
		},
		{
			name:    "evidence item with no key",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{path: ''}]}\n",
			wantErr: `benefits[0] "x": evidence[0]: no key set`,
		},
		{
			name:    "command spelled with the root",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{command: forge lint}]}\n",
			wantErr: "without the leading `forge`",
		},
		{
			name:    "analyzer that is not a flag",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{analyzer: migration-safety}]}\n",
			wantErr: `analyzer "migration-safety" is not a ` + "`forge lint`" + ` flag`,
		},
		{
			name:    "library outside pkg",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{library: internal/cli}]}\n",
			wantErr: `library "internal/cli" is not a forge/pkg package dir`,
		},
		{
			name:    "path escaping the repo",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{path: ../reliant/go.mod}]}\n",
			wantErr: `path "../reliant/go.mod" must be relative and stay inside the forge repo`,
		},
		{
			name:    "external without a path",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{external: control-plane}]}\n",
			wantErr: `external "control-plane" is not <repo>:<path>`,
		},
		{
			name:    "external naming an unknown repo",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{external: 'website:index.html'}]}\n",
			wantErr: `repo "website" is not one of control-plane, reliant, landing-page`,
		},
		{
			name:    "docs that is not https",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, docs: docs/x.md, evidence: [{path: go.mod}]}\n",
			wantErr: `docs "docs/x.md" is not an https:// URL`,
		},
		{
			name: "rule listed by two families",
			yaml: validCategories + "benefits:\n" +
				"  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}], rules: [{id: r1, lane: --a, bug_class: B}]}\n" +
				"  - {id: y, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}], rules: [{id: r1, lane: --a, bug_class: B}]}\n",
			wantErr: `benefits[1] "y": rules[0] "r1": rule already listed by "x"`,
		},
		{
			name:    "rule lane that is not a flag",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}], rules: [{id: r1, lane: conventions, bug_class: B}]}\n",
			wantErr: `rules[0] "r1": lane "conventions" is not a ` + "`forge lint`" + ` flag`,
		},
		{
			name:    "rule without a bug class",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}], rules: [{id: r1, lane: --a}]}\n",
			wantErr: `rules[0] "r1": bug_class is empty`,
		},
		{
			name:    "table cell with a pipe",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: 'a | b', claim: C, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `title may not contain '|'`,
		},
		{
			name:    "unknown scope",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, scope: anywhere, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": scope "anywhere" is not one of any, forge-project`,
		},
		{
			name:    "any scope without a recipe",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, scope: any, estimate: '~5 min', evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": scope: any needs adopt`,
		},
		{
			name:    "any scope without an estimate",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, scope: any, adopt: Run it., evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": scope: any needs estimate`,
		},
		{
			name:    "estimate that is not an estimate",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, scope: any, adopt: Run it., estimate: quick, evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": estimate "quick" is not`,
		},
		{
			name:    "recipe on a forge-project entry",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, adopt: Run it., evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": adopt and estimate are only for scope: any`,
		},
		{
			name:    "in-progress entry offered to any repo",
			yaml:    validCategories + "benefits:\n  - {id: x, category: build, title: T, claim: C, status: in-progress, availability: forge, scope: any, adopt: Run it., estimate: '~5 min', evidence: [{path: go.mod}]}\n",
			wantErr: `benefits[0] "x": an in-progress entry may not be scope: any`,
		},
		{
			name:    "id taking the any-repo anchor",
			yaml:    validCategories + "benefits:\n  - {id: any-repo, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}]}\n",
			wantErr: `id collides with a docs/benefits.md heading anchor`,
		},
		{
			name:    "duplicate category",
			yaml:    validCategories + "  - {id: data, title: Data, intro: Again.}\nbenefits:" + ok,
			wantErr: `categories[2] "data": duplicate category id`,
		},
		{
			name:    "category without an intro",
			yaml:    validCategories + "  - {id: run, title: Run}\nbenefits:" + ok,
			wantErr: `categories[2] "run": intro is empty`,
		},
		{
			name:    "no categories",
			yaml:    "benefits:" + ok,
			wantErr: "no categories",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.yaml))
			if err == nil {
				t.Fatalf("Parse accepted it; want an error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v\nwant it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// An entry with no status is enforced: the word the page defines for
// "something refuses the action", and the common case.
func TestStatusDefaultsToEnforced(t *testing.T) {
	r, err := Parse([]byte(validCategories +
		"benefits:\n  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Benefits[0].Status != StatusEnforced {
		t.Errorf("status = %q, want %q", r.Benefits[0].Status, StatusEnforced)
	}
}

// An entry with no scope needs a forge project, so adding the field changed
// nothing for the entries written before it; AppliesTo offers a repo with no
// forge.yaml only the scope: any entries, and never an in-progress one.
func TestScopeDefaultAndAppliesTo(t *testing.T) {
	r, err := Parse([]byte(validCategories + "benefits:\n" +
		"  - {id: proj, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}]}\n" +
		"  - {id: anywhere, category: build, title: T, claim: C, availability: forge, scope: any, adopt: Run it., estimate: '~1-2 hours', evidence: [{path: go.mod}]}\n" +
		"  - {id: later, category: build, title: T, claim: C, status: in-progress, availability: forge, evidence: [{path: go.mod}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Benefits[0].Scope; got != ScopeForgeProject {
		t.Errorf("default scope = %q, want %q", got, ScopeForgeProject)
	}
	ids := func(bs []Benefit) string {
		var out []string
		for _, b := range bs {
			out = append(out, b.ID)
		}
		return strings.Join(out, " ")
	}
	if got := ids(r.AppliesTo(false)); got != "anywhere" {
		t.Errorf("AppliesTo(no forge.yaml) = %q, want %q", got, "anywhere")
	}
	if got := ids(r.AppliesTo(true)); got != "proj anywhere" {
		t.Errorf("AppliesTo(forge project) = %q, want %q", got, "proj anywhere")
	}
}

// The embedded registry offers a repo with no forge.yaml something to start
// with: the front door (forge project benefits, the MCP benefits tool) is
// empty otherwise.
func TestEmbeddedRegistryHasAnyRepoBenefits(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if n := len(r.AppliesTo(false)); n < 5 {
		t.Errorf("only %d scope: any benefits; the front door needs real ones to offer", n)
	}
}

// A rule no flag selects runs only in a bare `forge lint`: `lane: ""` is
// valid, and the rule table says "bare `forge lint`" rather than an empty
// code span.
func TestBareLintLane(t *testing.T) {
	r, err := Parse([]byte(validCategories + `benefits:
  - {id: x, category: build, title: T, claim: C, availability: forge, evidence: [{path: go.mod}], rules: [{id: r1, lane: "", bug_class: B}]}
`))
	if err != nil {
		t.Fatalf("an empty lane must parse: %v", err)
	}
	page := string(RenderMarkdown(r))
	if !strings.Contains(page, "| `r1` | B                     | bare `forge lint` |") {
		t.Errorf("the bare-lint rule row is not rendered as bare `forge lint`:\n%s", page)
	}
	if strings.Contains(page, "``") {
		t.Error("the page renders an empty code span")
	}
}

func TestRenderReadmeTable(t *testing.T) {
	r, err := Parse([]byte(validCategories + `benefits:
  - {id: a, category: build, title: Alpha, claim: C, availability: forge, headline: true, evidence: [{path: go.mod}]}
  - {id: b, category: build, title: Beta two, claim: C, availability: forge, headline: true, evidence: [{path: go.mod}], rules: [{id: r1, lane: --x, bug_class: B}]}
  - {id: c, category: data, title: Gamma, claim: C, availability: forge, evidence: [{path: go.mod}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := string(RenderReadmeTable(r))
	// Padded as prettier pads: to rune width ("·" is one column), the
	// delimiter row as wide as its column.
	want := "" +
		"|           | What you get                                                 |\n" +
		"| --------- | ------------------------------------------------------------ |\n" +
		"| **Build** | [Alpha](docs/benefits.md#a) · [Beta two](docs/benefits.md#b) |\n" +
		"\n" +
		"Every benefit, with how to use it and the code that backs it: [docs/benefits.md](docs/benefits.md)\n"
	if got != want {
		t.Errorf("RenderReadmeTable:\n%s\nwant:\n%s", got, want)
	}
	// Data has no headline, so it has no row.
	if strings.Contains(got, "**Data**") {
		t.Error("a category with no headline entries has a row")
	}
	// All-forge headlines need no availability legend.
	if strings.Contains(got, "Reliant hosting") {
		t.Error("an all-forge table carries the availability legend")
	}
}

// TestRenderReadmeTableTagsNonForgeHeadlines pins that a README headline
// open-source forge cannot give you says so, except in the Host row, which is
// hosting by name.
func TestRenderReadmeTableTagsNonForgeHeadlines(t *testing.T) {
	r, err := Parse([]byte(`
categories:
  - {id: data, title: Data, intro: Stores things.}
  - {id: host, title: Host, intro: Hosts things.}
  - {id: agents, title: Agents, intro: Helps agents.}
benefits:
  - {id: a, category: data, title: Backups, claim: C, availability: hosted, headline: true, evidence: [{path: go.mod}]}
  - {id: b, category: host, title: CDN, claim: C, availability: hosted, headline: true, evidence: [{path: go.mod}]}
  - {id: c, category: agents, title: Review, claim: C, availability: reliant, headline: true, evidence: [{path: go.mod}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := string(RenderReadmeTable(r))
	for _, want := range []string{
		"[Backups](docs/benefits.md#a) _(hosted)_",
		"[Review](docs/benefits.md#c) _(Reliant)_",
		"_(hosted)_ needs Reliant hosting, _(Reliant)_ needs the Reliant harness; everything else",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("README table lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "[CDN](docs/benefits.md#b) _(hosted)_") {
		t.Errorf("the Host row is tagged:\n%s", got)
	}
}

func TestSpliceReadme(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	in := "# Title\n\nbefore\n\n" + ReadmeStart + "\n\nstale table\n\n" + ReadmeEnd + "\n\nafter\n"
	out, found := SpliceReadme([]byte(in), r)
	if !found {
		t.Fatal("markers not found")
	}
	wantPrefix := "# Title\n\nbefore\n\n" + ReadmeStart + "\n\n|"
	wantSuffix := ")\n\n" + ReadmeEnd + "\n\nafter\n"
	if s := string(out); !strings.HasPrefix(s, wantPrefix) || !strings.HasSuffix(s, wantSuffix) {
		t.Errorf("splice did not keep the text outside the markers and one blank line inside each:\n%s", s)
	}
	// Splicing is idempotent: the rendered block is a fixed point.
	again, _ := SpliceReadme(out, r)
	if !bytes.Equal(out, again) {
		t.Error("splicing a spliced README changed it")
	}
	if _, found := SpliceReadme([]byte("# no markers\n"), r); found {
		t.Error("found markers in a README that has none")
	}
}

// TestRenderedFilesAreCurrent fails when docs/benefits.md or the README's
// benefits block differs from a fresh render of benefits.yaml. `-update`
// (`task docs:benefits`) rewrites them. Both files are read by this process,
// so Go's test cache sees an edit to either.
func TestRenderedFilesAreCurrent(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	t.Run("README.md", func(t *testing.T) {
		path := filepath.Join(repoRoot, ReadmePath)
		readme, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		want, found := SpliceReadme(readme, r)
		if !found {
			t.Fatalf("%s has no %s … %s block; the README benefits table renders there", ReadmePath, ReadmeStart, ReadmeEnd)
		}
		if bytes.Equal(readme, want) {
			return
		}
		if *update {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Errorf("the benefits block of %s is stale against internal/benefits/benefits.yaml; run `task docs:benefits`", ReadmePath)
	})

	t.Run("docs/benefits.md", func(t *testing.T) {
		path := filepath.Join(repoRoot, DocsPath)
		want := RenderMarkdown(r)
		got, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if bytes.Equal(got, want) {
			return
		}
		if *update {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Errorf("%s is stale against internal/benefits/benefits.yaml; run `task docs:benefits`", DocsPath)
	})
}

// The page anchors every entry at its id, so the README's links and any
// external link to docs/benefits.md#<id> keep working when a title changes.
func TestRenderMarkdownAnchorsEveryEntry(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	page := string(RenderMarkdown(r))
	for _, b := range r.Benefits {
		if !strings.Contains(page, `<a id="`+b.ID+`"></a>`+"\n\n### "+b.Title+"\n") {
			t.Errorf("entry %q has no anchor directly above its heading", b.ID)
		}
		for _, rule := range b.Rules {
			if !strings.Contains(page, "| `"+rule.ID+"` ") {
				t.Errorf("rule %q of entry %q has no table row", rule.ID, b.ID)
			}
		}
	}
	if !strings.HasPrefix(page, "<!-- Generated from internal/benefits/benefits.yaml by `task docs:benefits`") {
		t.Error("the page does not open with its generated-file header")
	}
}
