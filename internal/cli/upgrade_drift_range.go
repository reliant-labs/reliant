// Which template drift the upgrade's range actually changed.
//
// ── Why ───────────────────────────────────────────────────────────────
//
// An adopted project carries files that differ from forge's templates for
// good: a hand-grown Taskfile, a customized frontend's auth module, a
// template the project never wanted. Every pin bump re-printed all of them
// (reliant, v1.7.1 → v1.8.1: 5 merges, 21 merges and 55 "absent here", the
// same rows as the bump before), and the wall buried the handful of rows
// the bump itself had moved. So by default the report lists only the rows
// whose TEMPLATE changed between the pinned forge and this one, and
// collapses the rest to one line per lane that names --all.
//
// ── How forge knows a template changed ────────────────────────────────
//
// Each row names the embedded template file it renders from
// (generator.UpgradeResult.Template / AdvisoryResult.Template). The pinned
// forge's copy of that file is in its module source in the module cache —
// present whenever the project builds at its pin, which it did until this
// run moved the pin — at <GOMODCACHE>/github.com/reliant-labs/forge@<from>/
// internal/templates/<file>. The two are compared byte for byte.
//
// Why the raw template source, and not what the old template renders to:
// rendering needs the old forge's functions and data shapes, i.e. running
// the old forge. Comparing source costs two file reads and needs nothing
// else. What it can miss is a change outside the template file — a template
// function or a constant it prints (a pinned tool version) — so such a row
// counts as unchanged. That is the cheaper error: the row is still in
// --json (changed_in_range: false), counted in the collapse line, and listed
// by --all. A change the other way (a template edit in a branch this
// project does not render) counts as changed and is shown: noise, not loss.
//
// Fail open: when the pinned forge's source is not in the module cache
// (never downloaded here), or a row names no template file, the row counts
// as changed (JSON null, "unknown") and is shown, and the report says why
// once. A template the pinned forge did not have at that path is new in the
// range, so it is changed.
package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/internal/templates"
)

// forgeModuleSource resolves a forge release's module source directory in
// the module cache, or says why it cannot. A seam: TestMain makes it
// hermetic, since the real one reads the developer's module cache.
var forgeModuleSource = moduleCacheForgeSource

// moduleCacheForgeSource is forge@version's directory in the module cache.
// It never downloads: a source that is not there is "unknown".
func moduleCacheForgeSource(version string) (dir, why string) {
	switch {
	case strings.TrimSpace(version) == "":
		return "", "the project pins no forge_version"
	case !semver.IsValid(version):
		return "", fmt.Sprintf("the pin %s is not a module version", version)
	case semver.Build(version) != "":
		return "", fmt.Sprintf("the pin %s is a local build, which no module cache holds", version)
	}
	cache := strings.TrimSpace(os.Getenv("GOMODCACHE"))
	if cache == "" {
		cmd := exec.Command("go", "env", "GOMODCACHE")
		cmd.Env = goexec.Env()
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Sprintf("the module cache could not be found (go env GOMODCACHE: %v)", err)
		}
		cache = strings.TrimSpace(string(out))
	}
	escPath, err := module.EscapePath(forgeModulePath)
	if err != nil {
		return "", err.Error()
	}
	escVersion, err := module.EscapeVersion(version)
	if err != nil {
		return "", err.Error()
	}
	dir = filepath.Join(cache, escPath+"@"+escVersion)
	if st, err := os.Stat(filepath.Join(dir, filepath.FromSlash(templates.SourceRoot))); err != nil || !st.IsDir() {
		return "", fmt.Sprintf("forge %s's source is not in the module cache (%s)", version, dir)
	}
	return dir, ""
}

// templateRange answers, per template file, whether it changed between the
// forge the project pinned and this one.
type templateRange struct {
	// filter is whether the text hides rows the range did not change: only
	// when there is a range. An upgrade to the pin it already has is a drift
	// report, and hides nothing.
	filter bool
	// dir is the pinned forge's module source; why says why it is "".
	dir, why string
	memo     map[string]*bool
	// noted: the one "listed because…" note has been printed.
	noted bool
}

func newTemplateRange(from, to string) *templateRange {
	t := &templateRange{filter: from != "" && from != to, memo: map[string]*bool{}}
	t.dir, t.why = forgeModuleSource(from)
	return t
}

// changed reports whether the template file src (a templates.SourcePath)
// changed across the range: nil when that cannot be told.
func (t *templateRange) changed(src string) *bool {
	if t == nil || src == "" || t.dir == "" {
		return nil
	}
	if v, ok := t.memo[src]; ok {
		return v
	}
	v := t.compare(src)
	t.memo[src] = v
	return v
}

func (t *templateRange) compare(src string) *bool {
	cur, err := templates.Source(src)
	if err != nil {
		return nil
	}
	old, err := os.ReadFile(filepath.Join(t.dir, filepath.FromSlash(templates.SourceRoot), filepath.FromSlash(src)))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// The pinned forge had no template here: it is new in the range.
		return changedVerdict(true)
	case err != nil:
		return nil
	}
	return changedVerdict(!bytes.Equal(old, cur))
}

func changedVerdict(b bool) *bool { return &b }

// driftView is how a drift lane is printed: every row (--all), or the rows
// the range changed.
type driftView struct {
	showAll bool
	// rng is nil for a caller with no range: every row is listed.
	rng *templateRange
}

// filtering reports whether rows the range did not change are hidden.
func (v driftView) filtering() bool { return !v.showAll && v.rng != nil && v.rng.filter }

// split returns the rows the text lists and how many it hides. Under --all
// nothing is hidden, and a row the range did not change says so.
func (v driftView) split(rows []driftRow) (shown []driftRow, hidden int) {
	for _, r := range rows {
		unchanged := r.Changed != nil && !*r.Changed
		switch {
		case v.filtering() && unchanged:
			hidden++
			continue
		case unchanged && v.rng != nil && v.rng.filter && r.Note == "":
			r.Note = "template unchanged in this range"
		}
		shown = append(shown, r)
	}
	return shown, hidden
}

// printCollapse is the one line that stands for the rows split hid.
func (v driftView) printCollapse(hidden, shown int) {
	if hidden == 0 {
		return
	}
	more := ""
	if shown > 0 {
		more = " more"
	}
	fmt.Printf("\n  %d%s %s from templates that did not change in this range — --all lists them\n",
		hidden, more, plural(hidden, "file differs", "files differ"))
}

// noteUnknown says, once per run, why rows were listed although the range
// filter is on: forge could not tell whether their template changed.
func (v driftView) noteUnknown(shown []driftRow) {
	if !v.filtering() || v.rng.noted {
		return
	}
	n := 0
	for _, r := range shown {
		if r.Changed == nil {
			n++
		}
	}
	if n == 0 {
		return
	}
	v.rng.noted = true
	if v.rng.dir == "" {
		fmt.Printf("\n  Listed whether or not this range changed their templates: %s.\n", v.rng.why)
		return
	}
	fmt.Printf("\n  %d listed whether or not this range changed %s: forge cannot name the template file that renders %s.\n",
		n, plural(n, "it", "them"), plural(n, "it", "them"))
}

// upgradeDriftRow is one template-drift row in --json: a file the template
// lane reports and does not write, sized, with whether the range changed
// its template.
type upgradeDriftRow struct {
	Path string `json:"path"`
	// Lane is "managed" (upgrade-managed files) or "scaffold-once" (files
	// you own from birth).
	Lane string `json:"lane"`
	// Group is what adopting the template costs: "cheap" (nothing of
	// yours is lost), "merge" (your lines and the template's), "absent"
	// (the template ships it; you do not have it).
	Group string `json:"group"`
	// Behind is template lines the file lacks; Yours is lines no template
	// line accounts for.
	Behind int `json:"behind"`
	Yours  int `json:"yours"`
	// ChangedInRange is whether the template changed between the pinned
	// forge and this one; null when that cannot be told (and the text then
	// lists the row).
	ChangedInRange *bool `json:"changed_in_range"`
}

// appendDriftRows records one lane's rows in --json, in the text's order.
func appendDriftRows(rep *upgradeReport, lane string, rows []driftRow) {
	cheap, merge, absent := groupDriftRows(rows)
	for _, g := range []struct {
		name string
		rows []driftRow
	}{{"cheap", cheap}, {"merge", merge}, {"absent", absent}} {
		for _, r := range g.rows {
			rep.TemplateDriftRows = append(rep.TemplateDriftRows, upgradeDriftRow{
				Path: filepath.ToSlash(r.Path), Lane: lane, Group: g.name,
				Behind: r.Missing, Yours: r.Local, ChangedInRange: r.Changed,
			})
		}
	}
}
