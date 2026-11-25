package cli

// Which generated files an upgrade's `forge generate` changed, grouped by the
// generator that writes them, with what the crossed range says about each.
//
// A pin bump's regenerate is where forge's behaviour changes reach a project
// (generated middleware that records arguments of failing calls, dashboards
// that exclude probe spans), and the agent learned of it from a 131-file
// diff and two broken tests that pinned generated output. Every file forge
// regenerates carries its own content hash (the forge:hash marker,
// internal/checksums), so the before/after comparison is a head-bounded scan
// of the project, not a hash of the tree.

import (
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// upgradeGeneratedChange is one generator's share of the regenerate.
type upgradeGeneratedChange struct {
	// Generator names the files a generator writes, by their file name
	// (middleware_gen.go) or, for one written per entity, its pattern
	// (*_orm_gen.go).
	Generator string `json:"generator"`
	// Files are the project-relative paths generate changed, added or
	// removed. Empty under --check, where generate does not run.
	Files []string `json:"files"`
	// Notes are the Generated-Change notes of the crossed range that name
	// this generator: what changed in its output, and which PR did it.
	Notes []upgradeNote `json:"notes"`
	// Planned marks a note-only entry of --check: the range says this
	// output changes; generate has not run.
	Planned bool `json:"planned,omitempty"`
}

// generatedHint is said whenever generated output changed (or, under
// --check, the range says it will).
const generatedHint = "tests that pin generated output (golden files, hashes or snapshots of generated files) may need updating: compare their expectations with the regenerated files, and update them when the change is forge's"

// snapshotGenerated is every forge-certified generated file of the project
// with its content hash. A variable so tests need no real project.
var snapshotGenerated = func(projectDir string) map[string]string {
	out := map[string]string{}
	for rel, m := range checksums.ScanMarkers(projectDir) {
		out[rel] = m.Body
	}
	return out
}

// diffGenerated lists the paths whose hash differs between before and after,
// or that exist in only one of them, sorted.
func diffGenerated(before, after map[string]string) []string {
	var out []string
	for p, h := range after {
		if before[p] != h {
			out = append(out, p)
		}
	}
	for p := range before {
		if _, ok := after[p]; !ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// generatorKey names the generator of each path. A file name that more than
// one changed path shares (middleware_gen.go, one per handler package) is its
// own key. A file name seen once whose suffix after the first "_" several
// such files share (customer_orm_gen.go, order_orm_gen.go) is one key,
// "*_orm_gen.go": the per-entity outputs of one generator.
func generatorKey(paths []string) map[string]string {
	byBase := map[string]int{}
	for _, p := range paths {
		byBase[path.Base(p)]++
	}
	suffixOf := func(base string) string {
		if i := strings.IndexByte(base, '_'); i > 0 && i < len(base)-1 {
			return base[i+1:]
		}
		return ""
	}
	bySuffix := map[string]int{}
	for _, p := range paths {
		b := path.Base(p)
		if byBase[b] == 1 {
			if s := suffixOf(b); s != "" {
				bySuffix[s]++
			}
		}
	}
	out := map[string]string{}
	for _, p := range paths {
		b := path.Base(p)
		key := b
		if s := suffixOf(b); byBase[b] == 1 && s != "" && bySuffix[s] > 1 {
			key = "*_" + s
		}
		out[p] = key
	}
	return out
}

// generatedNotes are the Generated-Change notes of the crossed range, each
// split into the generator it names and its summary ("middleware_gen.go:
// failing calls record redacted argument shapes").
func generatedNotes(crossed []upgradeCrossed) []upgradeNote {
	var out []upgradeNote
	for _, c := range crossed {
		if c.InPin {
			continue
		}
		for _, es := range [][]upgradeChange{c.Breaking, c.Added, c.Changed, c.Fixed} {
			for _, e := range es {
				for _, g := range e.Generated {
					out = append(out, upgradeNote{Text: g, PR: e.PR, Version: c.Version})
				}
			}
		}
	}
	return out
}

// noteTarget splits a Generated-Change note into the generator pattern it
// names and its summary; pattern is "" when the note names none.
func noteTarget(text string) (pattern, summary string) {
	if p, s, ok := strings.Cut(text, ": "); ok && !strings.ContainsAny(p, " \t") {
		return p, strings.TrimSpace(s)
	}
	return "", text
}

// noteMatches reports whether a note's pattern names the generator key or
// one of its files (a glob over the file name or the whole path).
func noteMatches(pattern, key string, files []string) bool {
	if pattern == "" {
		return false
	}
	if pattern == key {
		return true
	}
	if ok, _ := path.Match(pattern, key); ok {
		return true
	}
	for _, f := range files {
		if ok, _ := path.Match(pattern, path.Base(f)); ok {
			return true
		}
		if ok, _ := path.Match(pattern, f); ok {
			return true
		}
	}
	return false
}

// groupGenerated builds generated_changes: one entry per generator whose
// files changed, with the notes that name it, then one planned entry per
// note no changed generator matched (under --check, every note).
func groupGenerated(changed []string, notes []upgradeNote, check bool) []upgradeGeneratedChange {
	keys := generatorKey(changed)
	byKey := map[string]*upgradeGeneratedChange{}
	var order []string
	for _, p := range changed {
		k := keys[p]
		g := byKey[k]
		if g == nil {
			g = &upgradeGeneratedChange{Generator: k, Files: []string{}, Notes: []upgradeNote{}}
			byKey[k] = g
			order = append(order, k)
		}
		g.Files = append(g.Files, p)
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := byKey[order[i]], byKey[order[j]]
		if len(a.Files) != len(b.Files) {
			return len(a.Files) > len(b.Files)
		}
		return order[i] < order[j]
	})
	var unmatched []upgradeNote
	for _, n := range notes {
		pattern, summary := noteTarget(n.Text)
		hit := false
		for _, k := range order {
			if noteMatches(pattern, k, byKey[k].Files) {
				byKey[k].Notes = append(byKey[k].Notes, upgradeNote{Text: summary, PR: n.PR, Version: n.Version})
				hit = true
			}
		}
		if !hit {
			unmatched = append(unmatched, n)
		}
	}
	out := make([]upgradeGeneratedChange, 0, len(order)+len(unmatched))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	for _, n := range unmatched {
		pattern, summary := noteTarget(n.Text)
		if pattern == "" {
			pattern = "(unnamed)"
		}
		out = append(out, upgradeGeneratedChange{Generator: pattern, Files: []string{},
			Notes: []upgradeNote{{Text: summary, PR: n.PR, Version: n.Version}}, Planned: check})
	}
	return out
}

// generatedShown caps the generators the text report lists.
const generatedShown = 8

// printGenerated is the "generated output" section of the summary.
func printGenerated(w io.Writer, changes []upgradeGeneratedChange, hint string) {
	if len(changes) == 0 {
		return
	}
	fmt.Fprintln(w, "\nGenerated output this upgrade changes:")
	for i, g := range changes {
		if i == generatedShown {
			fmt.Fprintf(w, "  … %d more generators (generated_changes in --json)\n", len(changes)-i)
			break
		}
		line := "  - " + g.Generator
		switch {
		case len(g.Files) > 0:
			line += fmt.Sprintf(": %d %s", len(g.Files), plural(len(g.Files), "file", "files"))
		case g.Planned:
			line += " (when generate runs)"
		}
		fmt.Fprintln(w, line)
		for _, n := range g.Notes {
			fmt.Fprintf(w, "      %s%s\n", n.Text, prRef(n.PR))
		}
	}
	if hint != "" {
		fmt.Fprintf(w, "  Note: %s.\n", hint)
	}
}
