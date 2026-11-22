package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	forge "github.com/reliant-labs/forge"
)

const crossedChangelog = "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- not shipped yet\n\n" +
	"## [v1.8.0] - 2026-11-19\n\n" +
	"### Breaking changes\n\n- **feat(config):** drop the legacy flag ([#104](https://github.com/reliant-labs/forge/pull/104))\n" +
	"  - Agent: move `old_key` to `new_key` in forge.yaml, then run `forge generate`\n\n" +
	"### Added\n\n- **obs:** say when counting starts ([#2288](https://github.com/reliant-labs/forge/pull/2288))\n" +
	"- **observe:** record failing calls' argument shapes ([#2349](https://github.com/reliant-labs/forge/pull/2349))\n" +
	"  - Generated: middleware_gen.go: each op records redacted argument shapes of a failing or slow call\n" +
	"  - Agent: mark a sensitive argument `forge:no-capture`\n\n" +
	"### Fixed\n\n- a nil map in generate (#102)\n\n" +
	"```\n## [v9.9.9] - not a release, in a fence\n```\n\n" +
	"## [v1.7.0] - 2026-11-17\n\n### Added\n\n- **env:** render takes -D ([#2190](https://github.com/reliant-labs/forge/pull/2190))\n\n" +
	"## [v1.6.0] - 2026-11-13\n\n### Fixed\n\n- **lint:** something old ([#2100](https://github.com/reliant-labs/forge/pull/2100))\n"

func TestParseChangelog(t *testing.T) {
	rels := parseChangelog([]byte(crossedChangelog))
	var versions []string
	for _, r := range rels {
		versions = append(versions, r.Version)
	}
	if got := strings.Join(versions, ","); got != "v1.8.0,v1.7.0,v1.6.0" {
		t.Fatalf("releases = %s, want v1.8.0,v1.7.0,v1.6.0 (no Unreleased, nothing from a fence)", got)
	}
	r := rels[0]
	if r.Date != "2026-11-19" || strings.Join(r.Headings, "|") != "Breaking changes|Added|Fixed" {
		t.Fatalf("v1.8.0 = %+v", r)
	}
	brk := r.Entries["Breaking changes"][0]
	if brk.Scope != "feat(config)" || brk.Text != "drop the legacy flag" || brk.PR != 104 ||
		len(brk.Agent) != 1 || !strings.Contains(brk.Agent[0], "move `old_key`") {
		t.Errorf("breaking entry = %+v", brk)
	}
	obs := r.Entries["Added"][1]
	if obs.PR != 2349 || len(obs.Generated) != 1 || !strings.HasPrefix(obs.Generated[0], "middleware_gen.go: ") || len(obs.Agent) != 1 {
		t.Errorf("observe entry = %+v", obs)
	}
	if fx := r.Entries["Fixed"][0]; fx.PR != 102 || fx.Text != "a nil map in generate" || fx.Scope != "" {
		t.Errorf("plain (#N) entry = %+v", fx)
	}
}

// The changelog forge ships parses: every release section has a version, and
// entries carry their PR numbers.
func TestParseChangelog_ForgesOwn(t *testing.T) {
	rels := parseChangelog(forge.Changelog())
	if len(rels) < 5 {
		t.Fatalf("parsed %d releases from the embedded CHANGELOG.md", len(rels))
	}
	withPR := 0
	for _, r := range rels {
		if semverKey(r.Version) == "" {
			t.Errorf("unparseable release version %q", r.Version)
		}
		for _, es := range r.Entries {
			for _, e := range es {
				if e.PR > 0 {
					withPR++
				}
			}
		}
	}
	if withPR == 0 {
		t.Error("no entry carried a PR number")
	}
}

func TestCommitTrailersAndChange(t *testing.T) {
	for _, tc := range []struct {
		name               string
		c                  forgeCommit
		heading, scope, tx string
		pr                 int
		agent, generated   []string
		ok                 bool
	}{
		{name: "feat with trailers", c: forgeCommit{Subject: "feat(observe): record argument shapes (#2349)",
			Body: "why\n\nAgent-Instructions: mark sensitive args forge:no-capture,\n  then regenerate\nGenerated-Change: middleware_gen.go: records shapes\n"},
			heading: "Added", scope: "observe", tx: "record argument shapes", pr: 2349,
			agent: []string{"mark sensitive args forge:no-capture, then regenerate"}, generated: []string{"middleware_gen.go: records shapes"}, ok: true},
		{name: "fix", c: forgeCommit{Subject: "fix(lint): x (#1)"}, heading: "Fixed", scope: "lint", tx: "x", pr: 1, ok: true},
		{name: "bang is breaking", c: forgeCommit{Subject: "feat(cfg)!: drop y (#2)"}, heading: "Breaking changes", scope: "cfg", tx: "drop y", pr: 2, ok: true},
		{name: "footer is breaking", c: forgeCommit{Subject: "refactor: z (#3)", Body: "BREAKING CHANGE: z moved"}, heading: "Breaking changes", tx: "z", pr: 3, ok: true},
		{name: "other types change", c: forgeCommit{Subject: "docs(env): words (#4)"}, heading: "Changed", scope: "env", tx: "words", pr: 4, ok: true},
		{name: "untyped", c: forgeCommit{Subject: "Update the readme"}, heading: "Changed", tx: "Update the readme", ok: true},
		{name: "release commit skipped", c: forgeCommit{Subject: "chore: release v1.8.0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, e, ok := tc.c.change()
			if ok != tc.ok {
				t.Fatalf("ok = %v", ok)
			}
			if !ok {
				return
			}
			if h != tc.heading || e.Scope != tc.scope || e.Text != tc.tx || e.PR != tc.pr ||
				fmt.Sprint(e.Agent) != fmt.Sprint(tc.agent) || fmt.Sprint(e.Generated) != fmt.Sprint(tc.generated) {
				t.Errorf("got %q %+v", h, e)
			}
		})
	}
}

// fakeHistory is a linear forge history: commits in order, each with a
// subject; tags name positions.
type fakeHistory struct {
	revs []string // oldest first
	tags map[string]string
	msgs map[string]forgeCommit
}

func (f fakeHistory) idx(rev string) int {
	if t, ok := f.tags[rev]; ok {
		rev = t
	}
	for i, r := range f.revs {
		if strings.HasPrefix(r, rev) {
			return i
		}
	}
	return -1
}
func (f fakeHistory) Resolve(rev string) string {
	if i := f.idx(rev); i >= 0 {
		return f.revs[i]
	}
	return ""
}
func (f fakeHistory) Has(rev string) bool         { return f.idx(rev) >= 0 }
func (f fakeHistory) IsAncestor(a, b string) bool { return f.idx(a) >= 0 && f.idx(a) <= f.idx(b) }
func (f fakeHistory) Log(from, to string) ([]forgeCommit, error) {
	var out []forgeCommit
	for i := f.idx(from) + 1; i <= f.idx(to); i++ {
		out = append(out, f.msgs[f.revs[i]])
	}
	return out, nil
}

func hexRev(n int) string { return fmt.Sprintf("%012x", 0xaaaa00000000+n) }

// fullRevisionHistory models a cache whose graph queries require full IDs.
// Resolve alone accepts abbreviated pseudo-version revisions.
type fullRevisionHistory struct{ fakeHistory }

func (f fullRevisionHistory) Has(rev string) bool {
	return (strings.HasPrefix(rev, "v") || len(rev) == 40) && f.fakeHistory.Has(rev)
}

func (f fullRevisionHistory) IsAncestor(a, b string) bool {
	return f.Has(a) && f.Has(b) && f.fakeHistory.IsAncestor(a, b)
}

func (f fullRevisionHistory) Log(from, to string) ([]forgeCommit, error) {
	if !f.Has(from) || !f.Has(to) {
		return nil, fmt.Errorf("range endpoints must be full commit IDs or tags")
	}
	return f.fakeHistory.Log(from, to)
}

func TestCrossedRangeResolvesPseudoRevisions(t *testing.T) {
	fromRev := hexRev(1) + strings.Repeat("0", 28)
	toRev := hexRev(2) + strings.Repeat("0", 28)
	h := fullRevisionHistory{fakeHistory{
		revs: []string{fromRev, toRev},
		msgs: map[string]forgeCommit{toRev: {Subject: "fix: available offline (#42)"}},
	}}
	from := "v1.8.1-0.20261119000001-" + hexRev(1)
	to := "v1.8.1-0.20261119000002-" + hexRev(2)
	got := crossedRange(from, to, nil, h)
	if len(got) != 1 || got[0].Source != "git" || got[0].Commits == nil || *got[0].Commits != 1 ||
		len(got[0].Fixed) != 1 || got[0].Fixed[0].PR != 42 || got[0].After != hexRev(1) {
		t.Fatalf("resolved range = %+v", got)
	}
	// A missing object still produces the explicit offline fallback.
	h.revs = h.revs[:1]
	got = crossedRange(from, to, nil, h)
	if len(got) != 1 || got[0].Source != "unavailable" || got[0].Commit != hexRev(2) {
		t.Fatalf("unavailable range = %+v", got)
	}
}

func TestCrossedRange(t *testing.T) {
	rels := parseChangelog([]byte(crossedChangelog))
	// History: v1.7.0 at 0, #2340 at 1, #2349's commit is... the v1.8.0
	// changelog lists #104 #2288 #2349 #102; commits 1..4 carry them, v1.8.0
	// tags 4; 5..6 are after it.
	h := fakeHistory{tags: map[string]string{"v1.7.0": hexRev(0), "v1.8.0": hexRev(4)}, msgs: map[string]forgeCommit{}}
	subjects := []string{"chore: release v1.7.0", "feat(obs): say when counting starts (#2288)", "feat(config)!: drop the legacy flag (#104)",
		"feat(observe): record failing calls' argument shapes (#2349)", "fix: a nil map in generate (#102)",
		"feat(pgo): profile-guided builds (#2378)", "fix(devidp): v4.20.5 (#2391)\n"}
	for i, s := range subjects {
		h.revs = append(h.revs, hexRev(i))
		body := ""
		if i == 5 {
			body = "Agent-Instructions: run forge generate twice\nGenerated-Change: *_orm_gen.go: Update<E>Where\n"
		}
		h.msgs[hexRev(i)] = forgeCommit{Subject: strings.TrimSpace(s), Body: body}
	}
	pseudo := func(base string, n int) string { return fmt.Sprintf("%s-0.2026111900000%d-%s", base, n, hexRev(n)) }

	for _, tc := range []struct {
		name     string
		from, to string
		hist     forgeHistory
		want     []string // "<version>:<source>:<total>[:inpin]"
		noteHas  string
	}{
		{name: "tag to tag", from: "v1.6.0", to: "v1.8.0", want: []string{"v1.7.0:changelog:1", "v1.8.0:changelog:4"}},
		{name: "nothing crossed", from: "v1.8.0", to: "v1.8.0"},
		{name: "downgrade says nothing", from: "v1.8.0", to: "v1.7.0"},
		{name: "pseudo target, no clone: the tail is unavailable", from: "v1.7.0", to: pseudo("v1.8.1", 6),
			want: []string{"v1.8.0:changelog:4", "v1.8.1-0.20261119000006-aaaa00000006:unavailable:0"}, noteHas: "no local clone"},
		{name: "pseudo target with a clone: the tail from git", from: "v1.7.0", to: pseudo("v1.8.1", 6), hist: h,
			want: []string{"v1.8.0:changelog:4", "v1.8.1-0.20261119000006-aaaa00000006:git:2"}},
		// reliant's bump: the old pin was a pseudo-version naming the very
		// commit v1.8.0 was later tagged on.
		{name: "a pin on the tag's commit already has the release", from: pseudo("v1.7.1", 4), to: pseudo("v1.8.1", 6), hist: h,
			want: []string{"v1.8.0:changelog:0:inpin", "v1.8.1-0.20261119000006-aaaa00000006:git:2"}},
		{name: "a pin part way into a release leaves out what it had", from: pseudo("v1.7.1", 2), to: "v1.8.0", hist: h,
			want: []string{"v1.8.0:changelog:2"}, noteHas: "2 entries were already in your pin"},
		{name: "a pin part way in, no clone: says it cannot tell", from: pseudo("v1.7.1", 2), to: "v1.8.0",
			want: []string{"v1.8.0:changelog:4"}, noteHas: "may already be in it"},
		{name: "two pseudo-versions after one tag: the commits between them", from: pseudo("v1.8.1", 5), to: pseudo("v1.8.1", 6), hist: h,
			want: []string{"v1.8.1-0.20261119000006-aaaa00000006:git:1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := crossedRange(tc.from, tc.to, rels, tc.hist)
			var sum []string
			notes := ""
			for _, c := range got {
				s := fmt.Sprintf("%s:%s:%d", c.Version, c.Source, c.total())
				if c.InPin {
					s += ":inpin"
				}
				sum = append(sum, s)
				notes += c.Note
			}
			if strings.Join(sum, " ") != strings.Join(tc.want, " ") {
				t.Errorf("crossed = %v, want %v", sum, tc.want)
			}
			if tc.noteHas != "" && !strings.Contains(notes, tc.noteHas) {
				t.Errorf("notes %q lack %q", notes, tc.noteHas)
			}
		})
	}

	// The tail carries its trailers through to agent_instructions and the
	// generated notes.
	got := crossedRange("v1.7.0", pseudo("v1.8.1", 6), rels, h)
	tail := got[len(got)-1]
	if !tail.Untagged || tail.After != "v1.8.0" || tail.Commit != hexRev(6) || tail.Commits == nil || *tail.Commits != 2 ||
		!strings.Contains(tail.URL, "/compare/v1.8.0...") {
		t.Errorf("tail = %+v", tail)
	}
	if len(tail.AgentInstructions) != 1 || tail.AgentInstructions[0].PR != 2378 {
		t.Errorf("tail agent instructions = %+v", tail.AgentInstructions)
	}
	var agents []string
	for _, c := range got {
		for _, a := range c.AgentInstructions {
			agents = append(agents, a.Text)
		}
	}
	if len(agents) != 3 {
		t.Errorf("agent instructions over the range = %v, want the two of v1.8.0 and the tail's", agents)
	}
	if n := generatedNotes(got); len(n) != 2 {
		t.Errorf("generated notes = %+v", n)
	}

	var out bytes.Buffer
	printCrossed(&out, "v1.7.0", pseudo("v1.8.1", 6), got)
	for _, want := range []string{
		"Do this, from the changes you crossed:",
		"move `old_key` to `new_key`",
		"(#2378)",
		"v1.8.0 (2026-11-19): 1 breaking, 2 added, 1 fixed",
		"BREAKING feat(config): drop the legacy flag (#104)",
		"2 commits after v1.8.0, up to " + hexRev(6) + " (git): 1 added, 1 fixed",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text lacks %q:\n%s", want, out.String())
		}
	}
	doc, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"version":"v1.8.0"`, `"source":"changelog"`, `"breaking":[{"scope":"feat(config)"`, `"agent_instructions":[{`} {
		if !strings.Contains(string(doc), want) {
			t.Errorf("json lacks %s: %s", want, doc)
		}
	}
}

func TestGroupGenerated(t *testing.T) {
	changed := []string{
		"internal/handlers/a/middleware_gen.go", "internal/handlers/b/middleware_gen.go",
		"internal/store/customer_orm_gen.go", "internal/store/order_orm_gen.go",
		"frontends/web/src/lib/otel_gen.ts",
	}
	notes := []upgradeNote{
		{Text: "middleware_gen.go: each op records argument shapes", PR: 2349, Version: "v1.8.0"},
		{Text: "*_orm_gen.go: Update<E>Where", PR: 2335},
		{Text: "deploy/observability/spec/*_gen.json: probe spans excluded", PR: 2313},
		{Text: "free text with no generator"},
	}
	got := groupGenerated(changed, notes, false)
	var sum []string
	for _, g := range got {
		sum = append(sum, fmt.Sprintf("%s=%d/%d/%v", g.Generator, len(g.Files), len(g.Notes), g.Planned))
	}
	want := "*_orm_gen.go=2/1/false middleware_gen.go=2/1/false otel_gen.ts=1/0/false " +
		"deploy/observability/spec/*_gen.json=0/1/false (unnamed)=0/1/false"
	if strings.Join(sum, " ") != want {
		t.Errorf("groups = %s\nwant     %s", strings.Join(sum, " "), want)
	}
	if got[1].Notes[0].Text != "each op records argument shapes" || got[1].Notes[0].PR != 2349 {
		t.Errorf("middleware note = %+v", got[1].Notes)
	}
	// --check: generate does not run, so every note is planned.
	for _, g := range groupGenerated(nil, notes[:1], true) {
		if !g.Planned || g.Generator != "middleware_gen.go" {
			t.Errorf("check-mode entry = %+v", g)
		}
	}
	if d := diffGenerated(map[string]string{"a": "1", "b": "2", "gone": "3"}, map[string]string{"a": "1", "b": "9", "new": "4"}); strings.Join(d, ",") != "b,gone,new" {
		t.Errorf("diff = %v", d)
	}
	var out bytes.Buffer
	printGenerated(&out, got, generatedHint)
	for _, w := range []string{"middleware_gen.go: 2 files", "each op records argument shapes (#2349)", "tests that pin generated output"} {
		if !strings.Contains(out.String(), w) {
			t.Errorf("text lacks %q:\n%s", w, out.String())
		}
	}
}
