package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
)

// The grouped default view for `forge skill list`.
//
// The flat table this replaces printed one row per skill, each carrying its
// full frontmatter description — 84 lines and several hundred columns wide for
// the shipped catalog. The measured cost was an agent reading it TWICE (head,
// then tail) to see the whole thing, spending on the INDEX the context it
// wanted for the skills.
//
// The redesign answers the question a reader actually arrives with — "which
// three do I load now, and what exists for later" — with three moves:
//
//  1. START HERE, above everything. The entry skill and the greenfield
//     walkthrough are not one row among sixty-three; they are the rows that
//     make the other sixty-three legible, and alphabetical order buried them
//     between `dev` and `interactor`.
//  2. Two catalog sections, split by AUDIENCE (skillMeta.Emit), because
//     "how does forge do X" and "how do I review code" are answers to
//     different questions and interleaving them makes the reader classify
//     all sixty-three by hand.
//  3. One row per GROUP, not per skill. Grouping is by name — both the path
//     children (`db/seeding`) and the hyphen siblings (`api-rest`), which are
//     children of `api` in everything except their path spelling. A group's
//     row carries a `+N` marker so the reader knows the sub-skills are there
//     to be asked for, without spending a row and a 200-character sentence on
//     each.
//
// Nothing is removed: `--all` still prints the exhaustive table, `--json` is
// byte-compatible for the sub-agents that parse it, and every collapsed
// sub-skill is still loadable by its exact path.

// startHereSkills is the short ordered list a reader loads BEFORE choosing
// from the rest of the catalog: the entry skill (which carries the whole
// greenfield walkthrough) and the map of where code goes. They lead the
// default listing and are excluded from the alphabetical body, so the head
// section is never a duplicate of a row further down.
var startHereSkills = []string{entryPointSkillPath, "architecture"}

// descWidth is the column budget for a description in the grouped view. The
// listing is an index, not the content: a reader picks from a short label and
// gets the full sentence when they load the skill.
const descWidth = 78

// skillGroup is one row of the grouped view: a parent skill plus the
// sub-skills that collapsed into it.
type skillGroup struct {
	parent   skillMeta
	children []skillMeta
}

// skillSection is one labeled block of groups.
type skillSection struct {
	title  string
	blurb  string
	groups []skillGroup
}

// writeGroupedSkills renders the default `skill list` view.
func writeGroupedSkills(w io.Writer, skills []skillMeta) error {
	startHere, sections := groupSkills(skills)

	if len(startHere) > 0 {
		_, _ = fmt.Fprintf(w, "START HERE — load these before choosing from the catalog\n")
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, s := range startHere {
			_, _ = fmt.Fprintf(tw, "  %s\t\t%s\n", s.Path, truncateDesc(s.Description))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	for _, sec := range sections {
		if len(sec.groups) == 0 {
			continue
		}
		_, _ = fmt.Fprintf(w, "\n%s — %s\n", sec.title, sec.blurb)
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, g := range sec.groups {
			marker := ""
			if n := len(g.children); n > 0 {
				marker = fmt.Sprintf("+%d", n)
			}
			_, _ = fmt.Fprintf(tw, "  %s\t%s\t%s\n", g.parent.Path, marker, truncateDesc(g.parent.Description))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}

	writeSkillListFooter(w, skills, startHere, sections)
	return nil
}

// writeSkillListFooter names the three things a reader does NEXT, at the
// moment they have just failed to find what they wanted in the collapsed
// view: load one, search by keyword (the right tool when you know the word —
// it scores paths, descriptions AND bodies, so it finds skills whose title
// never mentions your term), or expand to the full table.
func writeSkillListFooter(w io.Writer, all, startHere []skillMeta, sections []skillSection) {
	collapsed := 0
	groups := len(startHere)
	for _, sec := range sections {
		groups += len(sec.groups)
		for _, g := range sec.groups {
			collapsed += len(g.children)
		}
	}
	name := cmdutil.Name()
	_, _ = fmt.Fprintf(w, "\n%d skills in %d groups", len(all), groups)
	if collapsed > 0 {
		_, _ = fmt.Fprintf(w, " (%d sub-skills collapsed into the +N counts)", collapsed)
	}
	_, _ = fmt.Fprintf(w, ".\n")
	_, _ = fmt.Fprintf(w, "  %s skill load <path>     read one (sub-skills load by full path, e.g. db/seeding)\n", name)
	_, _ = fmt.Fprintf(w, "  %s skill search <word>   fastest when you know the keyword — matches bodies too\n", name)
	_, _ = fmt.Fprintf(w, "  %s skill list <group>    one group's sub-skills by full path (e.g. skill list observability)\n", name)
	_, _ = fmt.Fprintf(w, "  %s skill list --all      the exhaustive table, every sub-skill and full description\n", name)
}

// groupSkills splits skills into the start-here head and the labeled catalog
// sections, collapsing sub-skills into their parents.
func groupSkills(skills []skillMeta) ([]skillMeta, []skillSection) {
	byPath := make(map[string]skillMeta, len(skills))
	for _, s := range skills {
		byPath[s.Path] = s
	}

	isStartHere := map[string]bool{}
	for _, p := range startHereSkills {
		isStartHere[p] = true
	}

	// Head, in the declared order — not alphabetical. The order IS the
	// advice: orient, then walk the greenfield sequence, then learn where
	// code goes.
	var head []skillMeta
	for _, p := range startHereSkills {
		if s, ok := byPath[p]; ok {
			head = append(head, s)
		}
	}

	// Assign each remaining skill to its group parent.
	groups := map[string]*skillGroup{}
	var order []string
	claimGroup := func(parent skillMeta) *skillGroup {
		g, ok := groups[parent.Path]
		if !ok {
			g = &skillGroup{parent: parent}
			groups[parent.Path] = g
			order = append(order, parent.Path)
		}
		return g
	}
	// Parents first, so a child never creates a placeholder group that the
	// real parent then has to merge into.
	for _, s := range skills {
		if isStartHere[s.Path] {
			continue
		}
		if groupParent(s, byPath, isStartHere) == "" {
			claimGroup(s)
		}
	}
	for _, s := range skills {
		if isStartHere[s.Path] {
			continue
		}
		parentPath := groupParent(s, byPath, isStartHere)
		if parentPath == "" {
			continue
		}
		if g, ok := groups[parentPath]; ok {
			g.children = append(g.children, s)
			continue
		}
		// Orphan: a nested path whose parent skill does not ship. It is a
		// group of its own rather than a silently dropped row.
		claimGroup(s)
	}

	sections := []skillSection{
		{title: "FORGE", blurb: "framework conventions; load the one for the layer you are touching"},
		{title: "METHODOLOGY", blurb: "how to work; project-agnostic, useful outside forge too"},
		{title: "YOURS", blurb: "project (.forge/skills) and user-global (~/.forge/skills)"},
	}
	sort.Strings(order)
	for _, path := range order {
		g := groups[path]
		sort.Slice(g.children, func(i, j int) bool { return g.children[i].Path < g.children[j].Path })
		sections[sectionIndex(g.parent)].groups = append(sections[sectionIndex(g.parent)].groups, *g)
	}
	return head, sections
}

// sectionIndex places a group in FORGE (0), METHODOLOGY (1), or YOURS (2).
// Scope wins over emit: a skill the user wrote is theirs regardless of what
// its frontmatter declares.
func sectionIndex(s skillMeta) int {
	if s.Scope != SkillScopeForge {
		return 2
	}
	if s.Emit == SkillEmitGeneral {
		return 1
	}
	return 0
}

// groupParent returns the path of the skill s collapses into, or "" when s is
// a group parent itself. Two kinds of child are recognized:
//
//   - PATH children — "db/seeding" under "db". Unambiguous. Nesting deeper
//     than one level collapses ALL the way to the top-level group:
//     "frontend/design/verify" belongs to "frontend", not to a row of its
//     own. Stopping at the immediate parent would leave a third-level path
//     sitting in the same column as "db" and reintroduce exactly the row the
//     grouping exists to remove.
//   - NAME siblings — "api-rest" under "api". These are children in every
//     sense except the path spelling: they were authored as separate
//     top-level skills, and printing three near-identical `api*` rows with
//     three near-identical descriptions is precisely the noise the grouped
//     view exists to remove. Longest matching prefix wins, so
//     "frontend-api-brief" prefers a "frontend-api" parent over "frontend"
//     if one is ever added.
//
// Clustering requires the candidate parent to (a) actually ship, (b) live in
// the same section — a methodology skill must not disappear under a framework
// one — and (c) not be a start-here skill, since those are printed in the head
// and a group hidden under them would be invisible.
func groupParent(s skillMeta, byPath map[string]skillMeta, isStartHere map[string]bool) string {
	eligible := func(candidate string) bool {
		parent, ok := byPath[candidate]
		return ok && !isStartHere[candidate] && sectionIndex(parent) == sectionIndex(s)
	}
	// Nested path: walk from the SHORTEST prefix outward so a grandchild
	// lands on its top-level group rather than on its immediate parent.
	if strings.Contains(s.Path, "/") {
		for idx := strings.Index(s.Path, "/"); idx > 0; {
			if candidate := s.Path[:idx]; eligible(candidate) {
				return candidate
			}
			next := strings.Index(s.Path[idx+1:], "/")
			if next < 0 {
				break
			}
			idx += next + 1
		}
		return ""
	}
	for idx := strings.LastIndex(s.Path, "-"); idx > 0; idx = strings.LastIndex(s.Path[:idx], "-") {
		if candidate := s.Path[:idx]; eligible(candidate) {
			return candidate
		}
	}
	return ""
}

// truncateDesc clips a description to the index's column budget, cutting at a
// word boundary so the label reads as a phrase rather than a severed word.
func truncateDesc(desc string) string {
	desc = strings.Join(strings.Fields(desc), " ")
	if len(desc) <= descWidth {
		return desc
	}
	clipped := desc[:descWidth]
	if idx := strings.LastIndexByte(clipped, ' '); idx > descWidth/2 {
		clipped = clipped[:idx]
	}
	return strings.TrimRight(clipped, " ,;:—-") + "…"
}
