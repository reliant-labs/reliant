package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/templates"
)

// fakeForgeSource lays out a pinned forge's module source the way the module
// cache holds it: <dir>/internal/templates/<SourcePath>. same names templates
// whose bytes match this forge's, differ templates whose bytes do not; a
// SourcePath in neither is absent (new in the range).
func fakeForgeSource(t *testing.T, same, differ []string) string {
	t.Helper()
	dir := t.TempDir()
	write := func(src string, body []byte) {
		p := filepath.Join(dir, filepath.FromSlash(templates.SourceRoot), filepath.FromSlash(src))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, src := range same {
		write(src, embeddedTemplate(t, src))
	}
	for _, src := range differ {
		write(src, append(embeddedTemplate(t, src), "# changed since the pin\n"...))
	}
	return dir
}

func embeddedTemplate(t *testing.T, src string) []byte {
	t.Helper()
	b, err := templates.Source(src)
	if err != nil {
		t.Fatalf("templates.Source(%q): %v", src, err)
	}
	return b
}

// Real template files, so the comparison reads this forge's embedded bytes.
var (
	srcGitignore  = templates.ProjectTemplates().SourcePath(".gitignore")
	srcBuf        = templates.ProjectTemplates().SourcePath("buf.yaml.tmpl")
	srcDockerfile = templates.ProjectTemplates().SourcePath("Dockerfile.tmpl")
)

func rangeOver(dir string) *templateRange {
	return &templateRange{filter: true, dir: dir, memo: map[string]*bool{}}
}

func rangeVerdict(v *bool) string {
	if v == nil {
		return "unknown"
	}
	if *v {
		return "changed"
	}
	return "unchanged"
}

func TestTemplateRangeChanged(t *testing.T) {
	dir := fakeForgeSource(t, []string{srcGitignore}, []string{srcBuf})
	cases := []struct {
		name string
		rng  *templateRange
		src  string
		want string
	}{
		{"same bytes in the pinned forge", rangeOver(dir), srcGitignore, "unchanged"},
		{"different bytes in the pinned forge", rangeOver(dir), srcBuf, "changed"},
		{"absent from the pinned forge: new in the range", rangeOver(dir), srcDockerfile, "changed"},
		{"a row that names no template", rangeOver(dir), "", "unknown"},
		{"a template this forge does not embed", rangeOver(dir), "project/no-such.tmpl", "unknown"},
		{"pinned forge's source unavailable", &templateRange{filter: true, why: "not cached", memo: map[string]*bool{}}, srcBuf, "unknown"},
		{"no range at all", nil, srcBuf, "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rangeVerdict(tc.rng.changed(tc.src)); got != tc.want {
				t.Fatalf("changed(%q) = %s, want %s", tc.src, got, tc.want)
			}
		})
	}
}

func TestModuleCacheForgeSource(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("GOMODCACHE", cache)
	for _, v := range []string{"v1.2.3", "v1.0.0-RC1"} {
		esc := strings.ReplaceAll(v, "RC", "!r!c")
		if err := os.MkdirAll(filepath.Join(cache, "github.com", "reliant-labs", "forge@"+esc, "internal", "templates"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		version string
		wantDir bool
		why     string
	}{
		{"v1.2.3", true, ""},
		{"v1.0.0-RC1", true, ""}, // upper case is escaped the way the module cache spells it
		{"v1.9.9", false, "not in the module cache"},
		{"", false, "pins no forge_version"},
		{"dev", false, "not a module version"},
		{"v1.2.4-0.20260101000000-abcdefabcdef+dirty", false, "local build"},
	}
	for _, tc := range cases {
		t.Run(tc.version, func(t *testing.T) {
			dir, why := moduleCacheForgeSource(tc.version)
			if (dir != "") != tc.wantDir || !strings.Contains(why, tc.why) {
				t.Fatalf("moduleCacheForgeSource(%q) = (%q, %q), want dir=%v and why containing %q", tc.version, dir, why, tc.wantDir, tc.why)
			}
		})
	}
}

func TestDriftViewSplit(t *testing.T) {
	yes, no := changedVerdict(true), changedVerdict(false)
	rows := []driftRow{{Path: "a", Changed: yes}, {Path: "b", Changed: no}, {Path: "c"}}
	cases := []struct {
		name       string
		view       driftView
		wantShown  string
		wantHidden int
		wantNoteB  string
	}{
		{"range filter hides only the unchanged", driftView{rng: rangeOver("x")}, "a,c", 1, ""},
		{"--all hides nothing and marks the unchanged", driftView{showAll: true, rng: rangeOver("x")}, "a,b,c", 0, "template unchanged in this range"},
		{"no range: a drift report hides nothing", driftView{rng: &templateRange{filter: false, memo: map[string]*bool{}}}, "a,b,c", 0, ""},
		{"no templateRange at all", driftView{}, "a,b,c", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			shown, hidden := tc.view.split(rows)
			var paths []string
			noteB := ""
			for _, r := range shown {
				paths = append(paths, r.Path)
				if r.Path == "b" {
					noteB = r.Note
				}
			}
			if got := strings.Join(paths, ","); got != tc.wantShown || hidden != tc.wantHidden || noteB != tc.wantNoteB {
				t.Fatalf("split = (%s, %d, note %q), want (%s, %d, note %q)", got, hidden, noteB, tc.wantShown, tc.wantHidden, tc.wantNoteB)
			}
		})
	}
}

// TestManagedLaneListsOnlyRangeChangedDrift pins the managed lane's text: the
// unchanged row is one collapse line, and the remedies offer commands for the
// listed rows only.
func TestManagedLaneListsOnlyRangeChangedDrift(t *testing.T) {
	dir := fakeForgeSource(t, []string{srcGitignore}, []string{srcBuf})
	modified := func(path, src string, missing, local int) generator.UpgradeResult {
		return generator.UpgradeResult{Path: path, Status: generator.UpgradeUserModified, Missing: missing, Local: local, Template: src}
	}
	unchanged := modified(".gitignore", srcGitignore, 3, 5)
	changed := modified("buf.yaml", srcBuf, 2, 1)
	unknown := modified("x.txt", "", 1, 0)

	cases := []struct {
		name    string
		results []generator.UpgradeResult
		showAll bool
		want    []string
		absent  []string
	}{
		{
			name:    "changed and unknown listed, unchanged collapsed",
			results: []generator.UpgradeResult{unchanged, changed, unknown},
			want: []string{
				"buf.yaml", "x.txt",
				"1 more file differs from templates that did not change in this range — --all lists them",
				"1 listed whether or not this range changed it",
				"Two ways to resolve the 2 file(s) with local edits",
				"--force buf.yaml", "--force x.txt", "disown buf.yaml",
			},
			absent: []string{".gitignore"},
		},
		{
			name:    "--all lists every row and marks the unchanged",
			results: []generator.UpgradeResult{unchanged, changed},
			showAll: true,
			want:    []string{".gitignore", "(template unchanged in this range)", "--force .gitignore", "Two ways to resolve the 2 file(s)"},
			absent:  []string{"did not change in this range —"},
		},
		{
			name:    "every row unchanged: one line, no remedies",
			results: []generator.UpgradeResult{unchanged},
			want:    []string{"1 file differs from templates that did not change in this range — --all lists them"},
			absent:  []string{"Two ways", "--force", ".gitignore"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := driftView{showAll: tc.showAll, rng: rangeOver(dir)}
			var c upgradeCounts
			out := captureStdout(t, func() {
				c = tallyAndPrintUpgradeResults(tc.results, true, view)
				printUserModifiedRemedies(c, tc.showAll)
			})
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(out, a) {
					t.Errorf("output has %q:\n%s", a, out)
				}
			}
			if len(c.modifiedRows) != len(tc.results) {
				t.Errorf("modifiedRows = %d, want every user-modified row (%d) for --json", len(c.modifiedRows), len(tc.results))
			}
		})
	}
}

func TestAdvisoryLaneListsOnlyRangeChangedDrift(t *testing.T) {
	dir := fakeForgeSource(t, []string{srcGitignore}, []string{srcBuf})
	results := []generator.AdvisoryResult{
		{Path: "web/src/lib/a.ts", Status: generator.AdvisoryDiverged, Missing: 4, Local: 2, Template: srcGitignore},
		{Path: "web/src/lib/b.ts", Status: generator.AdvisoryAbsent, Missing: 9, Template: srcGitignore},
		{Path: "web/src/lib/c.ts", Status: generator.AdvisoryDiverged, Missing: 1, Local: 7, Template: srcBuf},
	}
	var rows []driftRow
	var hidden int
	out := captureStdout(t, func() {
		rows, hidden = printAdvisoryReport(results, true, driftView{rng: rangeOver(dir)})
	})
	if len(rows) != 3 || hidden != 2 {
		t.Fatalf("rows=%d hidden=%d, want 3 rows (all, for --json) and 2 hidden", len(rows), hidden)
	}
	for _, w := range []string{"Files you own, whose forge templates have moved on:", "1 merge(s)", "web/src/lib/c.ts",
		"2 more files differ from templates that did not change in this range — --all lists them"} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
	for _, a := range []string{"a.ts", "b.ts", "absent here"} {
		if strings.Contains(out, a) {
			t.Errorf("output has %q:\n%s", a, out)
		}
	}
}

// TestTemplateDriftRowsJSON pins the additive --json fields: where they sit,
// their spelling, and null for "cannot tell".
func TestTemplateDriftRowsJSON(t *testing.T) {
	rep := newUpgradeReport(upgradeOptions{})
	rep.TemplateDriftRows = []upgradeDriftRow{}
	appendDriftRows(rep, "managed", []driftRow{
		{Path: "Taskfile.yml", Missing: 353, Local: 817, Changed: changedVerdict(false)},
		{Path: "buf.yaml", Missing: 2, Changed: changedVerdict(true)},
	})
	appendDriftRows(rep, "scaffold-once", []driftRow{{Path: ".github/CODEOWNERS", Missing: 5, Absent: true}})
	rep.TemplateDriftHidden = 1
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	for _, w := range []string{
		`{"path":"buf.yaml","lane":"managed","group":"cheap","behind":2,"yours":0,"changed_in_range":true}`,
		`{"path":"Taskfile.yml","lane":"managed","group":"merge","behind":353,"yours":817,"changed_in_range":false}`,
		`{"path":".github/CODEOWNERS","lane":"scaffold-once","group":"absent","behind":5,"yours":0,"changed_in_range":null}`,
		`"template_drift_hidden":1`,
	} {
		if !strings.Contains(doc, w) {
			t.Errorf("--json lacks %s:\n%s", w, doc)
		}
	}
	if strings.Contains(doc, "template_drift_range_note") {
		t.Errorf("an empty range note must be omitted:\n%s", doc)
	}
	order := []string{`"template_drift":`, `"template_drift_rows":`, `"template_drift_hidden":`, `"offers":`}
	for i := 1; i < len(order); i++ {
		if strings.Index(doc, order[i-1]) > strings.Index(doc, order[i]) {
			t.Errorf("%s must follow %s:\n%s", order[i], order[i-1], doc)
		}
	}
	if empty, _ := json.Marshal(func() *upgradeReport {
		r := newUpgradeReport(upgradeOptions{})
		r.TemplateDriftRows = []upgradeDriftRow{}
		return r
	}()); !strings.Contains(string(empty), `"template_drift_rows":[]`) {
		t.Errorf("no rows must be [], not null: %s", empty)
	}
}

func TestUpgradeStepRecordsDuration(t *testing.T) {
	rep := newUpgradeReport(upgradeOptions{})
	r := &upgradeRun{rep: rep, opts: upgradeOptions{skip: map[string]bool{"lint": true}}}
	cases := []struct {
		phase   string
		run     func() error
		wantErr bool
		status  string
		atLeast int64
	}{
		// templates records its own status and detail; step still times it.
		{"templates", func() error { r.setPhase("templates", "done", "x"); time.Sleep(20 * time.Millisecond); return nil }, false, "done", 20},
		{"pin", func() error { return nil }, false, "done", 0},
		{"generate", func() error { return errors.New("boom") }, true, "failed", 0},
		{"lint", func() error { t.Fatal("a --skip phase must not run"); return nil }, false, "skipped", 0},
	}
	for _, tc := range cases {
		t.Run(tc.phase, func(t *testing.T) {
			if err := r.step(tc.phase, tc.run); (err != nil) != tc.wantErr {
				t.Fatalf("step err = %v, wantErr %v", err, tc.wantErr)
			}
			var got *upgradePhase
			for i := range rep.Phases {
				if rep.Phases[i].Name == tc.phase {
					got = &rep.Phases[i]
				}
			}
			switch {
			case got == nil:
				t.Fatalf("phase %q not recorded: %+v", tc.phase, rep.Phases)
			case got.Status != tc.status:
				t.Fatalf("status = %q, want %q", got.Status, tc.status)
			case got.DurationMS < tc.atLeast:
				t.Fatalf("duration_ms = %d, want >= %d", got.DurationMS, tc.atLeast)
			case tc.status == "skipped" && got.DurationMS != 0:
				t.Fatalf("a --skip phase records duration_ms %d, want 0", got.DurationMS)
			}
		})
	}
	raw, _ := json.Marshal(rep.Phases[len(rep.Phases)-1])
	if !strings.Contains(string(raw), `"duration_ms":0`) {
		t.Errorf("duration_ms must always be present: %s", raw)
	}
}

func TestSlowPhasesLine(t *testing.T) {
	cases := []struct {
		name   string
		phases []upgradePhase
		want   string
	}{
		{"none slow", []upgradePhase{{Name: "lint", DurationMS: 29_999}}, ""},
		{"one slow", []upgradePhase{{Name: "templates", DurationMS: 800}, {Name: "generate", DurationMS: 45_000}},
			"Slow phases: generate 45s (phases[].duration_ms in --json)"},
		{"slowest first", []upgradePhase{{Name: "generate", DurationMS: 41_400}, {Name: "lint", DurationMS: 252_000}},
			"Slow phases: lint 4m12s, generate 41s (phases[].duration_ms in --json)"},
		{"no phases", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slowPhasesLine(tc.phases); got != tc.want {
				t.Fatalf("slowPhasesLine = %q, want %q", got, tc.want)
			}
		})
	}
}
