// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/rctx"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

type SkillParams struct {
	Action string `json:"action" jsonschema:"required,description=Action to perform: load (load a skill by path)\\, list (list available skills)\\, search (search skills by keyword),enum=load,enum=list,enum=search"`
	Path   string `json:"path,omitempty" jsonschema:"description=Skill path to load or list children of. Use skill name for top-level (e.g. 'go'\\, 'reliant-config')"`
	Query  string `json:"query,omitempty" jsonschema:"description=Search query for finding skills (used with action=search)"`

	// Windowed reading for action=load. Omit all of these to get the whole
	// skill; every result reports its total size and whether more remains, so
	// one call is enough to know whether a second is needed.
	Section string `json:"section,omitempty" jsonschema:"description=Fetch only this markdown section (heading text\\, e.g. 'Seeding'). Exact match first\\, then unique substring. Every load lists the available sections. Cannot be combined with: regex"`
	Offset  int    `json:"offset,omitempty" jsonschema:"description=Start reading from byte N of the selected view (default: 0). Combine with the same section/regex that produced the view. Can be combined with: limit section regex"`
	Limit   int    `json:"limit,omitempty" jsonschema:"description=Read up to N bytes (default: the delivery budget). Can be combined with: offset section regex"`

	Regex                string `json:"regex,omitempty" jsonschema:"description=Deliver only lines matching this regex\\, numbered\\, instead of the skill body. Use to locate content in a large skill\\, then fetch it with section or offset. Can be combined with: offset limit regex_case_insensitive regex_context_before regex_context_after. Cannot be combined with: section"`
	RegexCaseInsensitive bool   `json:"regex_case_insensitive,omitempty" jsonschema:"description=Perform case-insensitive regex matching (default: false). Requires: regex"`
	RegexContextBefore   int    `json:"regex_context_before,omitempty" jsonschema:"description=Include N lines before each match like grep -B (default: 0). Requires: regex"`
	RegexContextAfter    int    `json:"regex_context_after,omitempty" jsonschema:"description=Include N lines after each match like grep -A (default: 0). Requires: regex"`
}

// skillTool operates entirely on a pre-loaded slice of skills. The skill data
// is synced from the daemon via the config pipeline and injected by the
// ToolsFactory — the tool itself never touches the filesystem.
type skillTool struct {
	skills []config.StoredSkill
}

const skillDescription = `Load skills — specialized knowledge and instructions for specific tasks.
Skills provide detailed guidance on how to perform particular operations.
Use 'list' to see available skills, 'load' to load a skill's instructions,
or 'search' to find skills by keyword.
When you load a skill, its instructions become available in the conversation.
Skills may suggest tools to load — use the load_tool tool if suggested tools are needed.

In multi-repo projects, skills are discovered recursively across all nested
repos. Each skill's source repo is shown in brackets after its description
(e.g. "[source: api]") and is reflected as a prefix on its path
(e.g. "api/deploy" vs "web/deploy"). Use the prefixed path with 'load'.

READING A LARGE SKILL (action=load):
Every load ends with its total size and whether anything remains, so ONE call
tells you if you have the whole skill. Do not page defensively — page only when
a result says bytes remain, and it will name the exact call to continue with.

A skill too large to deliver at once is read with these, mirroring bash_output:
- section: fetch one markdown section by heading. Preferred — every load lists
  the skill's sections, so this is usually one targeted call rather than
  guessing byte ranges.
- offset / limit: page through the current view by byte range.
- regex (+ regex_case_insensitive, regex_context_before, regex_context_after):
  deliver only matching lines, numbered, to locate content before fetching it.

Examples:
  skill(action="load", path="db")                      whole skill + size report
  skill(action="load", path="db", section="Seeding")   one section
  skill(action="load", path="db", offset=23000)        continue where a window ended
  skill(action="load", path="db", regex="foreign key") find the relevant part first`

func NewSkillTool(skills []config.StoredSkill) Tool {
	tool := &skillTool{skills: skills}
	return NewToolWrapper[SkillParams, ToolResponse](tool)
}

func (t *skillTool) Name() string {
	return ToolSkill
}

func (t *skillTool) Description() string {
	return skillDescription
}

func (t *skillTool) RequiresPermission(params SkillParams) (bool, error) {
	return false, nil
}

func (t *skillTool) Execute(_ *rctx.ToolContext, params SkillParams) (ToolResponse, error) {
	slog.Debug("[SkillTool] Execute", "action", params.Action, "path", params.Path, "query", params.Query, "availableSkills", len(t.skills))
	switch params.Action {
	case "list":
		return t.listSkills(params.Path)
	case "load":
		if params.Path == "" {
			return NewTextErrorResponse("path is required when action is 'load'"), nil
		}
		return t.loadSkillWindowed(params)
	case "search":
		if params.Query == "" {
			return NewTextErrorResponse("query is required when action is 'search'"), nil
		}
		return t.searchSkills(params.Query)
	default:
		return NewTextErrorResponse(fmt.Sprintf("unknown action: %s (expected: load, list, search)", params.Action)), nil
	}
}

// isInNamespace reports whether skillPath names a skill that sits DIRECTLY
// under the namespace ns, using the same component-aligned addressing
// findSkillByPath uses to resolve a load.
//
// A namespace is not a skill: it is any component-aligned suffix of a skill
// path's parent prefix. "forge/frontend/design" sits under both
// "forge/frontend" and "frontend", because load accepts both
// "forge/frontend/design" and "frontend/design". Deriving the namespace set
// from the paths themselves — rather than from which skills happen to exist —
// is what keeps list and load in agreement: a forge project surfaces a FLAT,
// childless "frontend" skill (the .claude/skills copy) alongside the nested
// "forge/frontend/..." tree, and resolving "frontend" to that flat skill and
// then demanding "frontend/"-prefixed children found nothing.
//
// ns == "" means the root namespace, whose members are the top-level skills.
func isInNamespace(skillPath, ns string) bool {
	idx := strings.LastIndex(skillPath, "/")
	if ns == "" {
		return idx < 0
	}
	if idx < 0 {
		return false
	}
	parent := strings.ToLower(skillPath[:idx])
	return parent == ns || strings.HasSuffix(parent, "/"+ns)
}

// namespaceMembers returns the skills directly under ns. Every returned skill
// is loadable by its printed SkillPath, and by "<ns>/<last-segment>" whenever
// that spelling is unambiguous.
func namespaceMembers(skills []config.StoredSkill, ns string) []config.StoredSkill {
	ns = strings.ToLower(ns)
	var out []config.StoredSkill
	for _, s := range skills {
		if isInNamespace(s.SkillPath, ns) {
			out = append(out, s)
		}
	}
	return out
}

// hasChildren returns true if path is also a namespace with members — i.e. the
// "(has sub-skills)" hint is shown exactly when `list <path>` will succeed.
func hasChildren(skills []config.StoredSkill, path string) bool {
	return len(namespaceMembers(skills, path)) > 0
}

// canonicalNamespaces returns every full-path namespace that has members,
// sorted. These are the unambiguous spellings a caller can list, so they are
// what an empty listing offers instead of a dead end.
func canonicalNamespaces(skills []config.StoredSkill) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, s := range skills {
		idx := strings.LastIndex(s.SkillPath, "/")
		if idx <= 0 {
			continue
		}
		ns := s.SkillPath[:idx]
		if _, dup := seen[ns]; dup {
			continue
		}
		seen[ns] = struct{}{}
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// canonicalNamespaceOf returns the full-path namespace the given members share,
// falling back to the caller's spelling when they span more than one (a bare
// namespace name can be a component-aligned suffix of several full paths).
func canonicalNamespaceOf(members []config.StoredSkill, fallback string) string {
	canonical := ""
	for _, m := range members {
		idx := strings.LastIndex(m.SkillPath, "/")
		if idx < 0 {
			return fallback
		}
		parent := m.SkillPath[:idx]
		if canonical == "" {
			canonical = parent
			continue
		}
		if canonical != parent {
			return fallback
		}
	}
	if canonical == "" {
		return fallback
	}
	return canonical
}

// noSubSkillsError renders the empty-listing reply. It names the namespaces
// that do exist so a caller who guessed wrong can retry rather than give up.
func noSubSkillsError(skills []config.StoredSkill, filterPath string) ToolResponse {
	var sb strings.Builder
	fmt.Fprintf(&sb, "no sub-skills found under: %s", filterPath)

	namespaces := canonicalNamespaces(skills)
	if len(namespaces) == 0 {
		sb.WriteString("\n\nNo skill namespaces exist — every skill is top-level. Use action='list' with no path to see them.")
		return NewTextErrorResponse(sb.String())
	}

	sb.WriteString("\n\nNamespaces that do have sub-skills:\n- ")
	sb.WriteString(strings.Join(namespaces, "\n- "))
	sb.WriteString("\n\nUse action='list' with one of these, or action='list' with no path for top-level skills.")
	return NewTextErrorResponse(sb.String())
}

// findSkillByPath resolves a caller-supplied path to a skill using the shared
// addressing rule in skillscore.ResolveSkillPathIndex — the same rule the
// workflow validator applies to a charter's `skills:` block, so a name that
// validates is a name this tool can load.
func findSkillByPath(skills []config.StoredSkill, path string) *config.StoredSkill {
	i := skillscore.ResolveSkillPathIndex(storedSkillPaths(skills), path)
	if i < 0 {
		return nil
	}
	return &skills[i]
}

// storedSkillPaths projects the addressable path off each skill, preserving
// index alignment with the input slice.
func storedSkillPaths(skills []config.StoredSkill) []string {
	paths := make([]string, len(skills))
	for i := range skills {
		paths[i] = skills[i].SkillPath
	}
	return paths
}

func (t *skillTool) listSkills(filterPath string) (ToolResponse, error) {
	normalizedFilter := strings.ToLower(strings.TrimSpace(filterPath))

	// The filter names a NAMESPACE, not a skill — see isInNamespace. Members
	// are derived from the skill paths themselves, so a namespace with no skill
	// of its own still lists, and a childless skill that shares a name with a
	// populated namespace no longer swallows the listing.
	matches := namespaceMembers(t.skills, normalizedFilter)

	if len(matches) == 0 {
		if normalizedFilter == "" {
			return NewTextResponse("No skills available.\n\nSkills can be added to:\n- .reliant/skills/ (project)\n- ~/.reliant/skills/ (global)"), nil
		}
		return noSubSkillsError(t.skills, filterPath), nil
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].SkillPath < matches[j].SkillPath
	})

	var sb strings.Builder
	if normalizedFilter == "" {
		sb.WriteString("Available skills:\n\n")
	} else {
		// Echo the canonical namespace the members are actually keyed under, so
		// a caller who reached the group by an unprefixed name learns the
		// prefixed spelling the entries carry.
		fmt.Fprintf(&sb, "Sub-skills of %s:\n\n", canonicalNamespaceOf(matches, normalizedFilter))
	}

	for _, def := range matches {
		desc := def.Description
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		fmt.Fprintf(&sb, "- %s: %s", def.SkillPath, desc)
		if hasChildren(t.skills, def.SkillPath) {
			sb.WriteString(" (has sub-skills)")
		}
		if def.Scope != "" {
			fmt.Fprintf(&sb, " [%s]", def.Scope)
		}
		if def.Source != "" {
			fmt.Fprintf(&sb, " [source: %s]", def.Source)
		}
		sb.WriteString("\n")
	}

	sb.WriteString("\nUse skill tool with action='load' and path='<skill-path>' to load a skill's instructions.")

	return NewTextResponse(sb.String()), nil
}

// loadSkillWindowed delivers a skill for action=load: it renders the same
// content loadSkill always has, then applies the caller's window and appends
// the delivery report.
//
// The two halves are kept apart because they have different audiences.
// loadSkill's output is CONTENT — the same bytes call_llm seeds as a preloaded
// skill, which must stay byte-identical across both delivery paths. The window
// and its footer are per-CALL facts (where you are, what remains, how to ask
// for the rest) that are meaningless in a cached prompt prefix.
func (t *skillTool) loadSkillWindowed(params SkillParams) (ToolResponse, error) {
	resp, err := t.loadSkill(params.Path)
	if err != nil || resp.IsError {
		return resp, err
	}

	if err := validateSkillWindowParams(params); err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	window, err := selectSkillWindow(resp.Content, params)
	if err != nil {
		return NewTextErrorResponse(err.Error()), nil
	}

	// A filter that matched nothing is a dead end unless the reply says what
	// the skill does contain, so it carries the section outline back.
	if params.Regex != "" && window.Matches == 0 {
		var sb strings.Builder
		fmt.Fprintf(&sb, "No lines in skill %s match %q (skill is %d bytes).",
			params.Path, params.Regex, window.SkillBytes)
		if len(window.Sections) > 0 {
			fmt.Fprintf(&sb, "\n\nSections (fetch one with section=\"<name>\"):\n- %s",
				strings.Join(sectionTitles(window.Sections, maxOutlineEntries), "\n- "))
		}
		return NewTextResponse(sb.String()), nil
	}

	text, _ := renderSkillDelivery(params.Path, window)
	return NewTextResponse(text), nil
}

// loadSkill renders a skill's delivered CONTENT: its body plus the sub-skill,
// related-skill and suggested-tools pointers. It applies no window and no size
// cap — see loadSkillWindowed for the delivery layer, and LoadSkillForInjection
// for the preload path that depends on these exact bytes.
func (t *skillTool) loadSkill(path string) (ToolResponse, error) {
	normalizedPath := strings.ToLower(strings.TrimSpace(path))

	def := findSkillByPath(t.skills, normalizedPath)
	if def == nil {
		// Try partial match across all skills.
		var candidates []string
		for _, s := range t.skills {
			if strings.Contains(strings.ToLower(s.SkillPath), normalizedPath) {
				candidates = append(candidates, s.SkillPath)
			}
		}
		if len(candidates) > 0 {
			sort.Strings(candidates)
			return NewTextErrorResponse(fmt.Sprintf("skill not found: %s\n\nDid you mean one of these?\n- %s",
				path, strings.Join(candidates, "\n- "))), nil
		}
		return NewTextErrorResponse(fmt.Sprintf("skill not found: %s\n\nUse action='list' to see available skills.", path)), nil
	}

	var sb strings.Builder
	sb.WriteString(def.Body)

	// Show sub-skills if this skill's path is also a populated namespace.
	if children := namespaceMembers(t.skills, def.SkillPath); len(children) > 0 {
		sort.Slice(children, func(i, j int) bool {
			return children[i].SkillPath < children[j].SkillPath
		})
		sb.WriteString("\n\n---\nSub-skills available (use skill tool with action=list or action=load):\n")
		for _, child := range children {
			fmt.Fprintf(&sb, "- %s: %s", child.SkillPath, truncateDescription(child.Description, 80))
			if hasChildren(t.skills, child.SkillPath) {
				sb.WriteString(" (has sub-skills)")
			}
			sb.WriteString("\n")
		}
	}

	// Find sibling skills (same parent path).
	siblings := findSiblingSkills(t.skills, *def)
	if len(siblings) > 0 {
		sb.WriteString("\n---\nRelated skills available (use skill tool to load):\n")
		for _, s := range siblings {
			fmt.Fprintf(&sb, "- %s: %s\n", s.SkillPath, truncateDescription(s.Description, 80))
		}
	}

	// Append allowed tools suggestion.
	if len(def.AllowedTools) > 0 {
		sb.WriteString("\n---\nThis skill suggests loading these tools: ")
		sb.WriteString(strings.Join(def.AllowedTools, ", "))
		sb.WriteString("\n")
	}

	return NewTextResponse(sb.String()), nil
}

// LoadSkillForInjection resolves a skill path against the given skills using the
// SAME tolerant resolution as the runtime skill tool (findSkillByPath) and
// returns the resolved skill's canonical name plus the exact body text the tool
// would produce for action=load — including any sub-skill / related-skill /
// allowed-tools annotations. Returns ("", "", false) when the path does not
// resolve or the resolved skill has an empty body.
//
// call_llm uses this to seed a preloaded skill whose body is byte-identical to
// the agent loading the skill by hand, so the preloaded body and a hand-loaded
// body can never diverge.
func LoadSkillForInjection(skills []config.StoredSkill, path string) (name string, body string, ok bool) {
	def := findSkillByPath(skills, strings.ToLower(strings.TrimSpace(path)))
	if def == nil || strings.TrimSpace(def.Body) == "" {
		return "", "", false
	}
	t := &skillTool{skills: skills}
	resp, err := t.loadSkill(path)
	if err != nil || resp.IsError || strings.TrimSpace(resp.Content) == "" {
		return "", "", false
	}
	return def.Name, resp.Content, true
}

func (t *skillTool) searchSkills(query string) (ToolResponse, error) {
	if len(t.skills) == 0 {
		return NewTextResponse("No skills available to search."), nil
	}

	queryLower := strings.ToLower(query)
	queryWords := strings.Fields(queryLower)

	type scoredDef struct {
		def   config.StoredSkill
		score int
	}
	var results []scoredDef

	for _, def := range t.skills {
		score := 0
		pathLower := strings.ToLower(def.SkillPath)
		nameLower := strings.ToLower(def.Name)
		descLower := strings.ToLower(def.Description)

		for _, word := range queryWords {
			if strings.Contains(pathLower, word) {
				score += 3 // Path match (includes name) is worth more
			} else if strings.Contains(nameLower, word) {
				score += 3
			}
			if strings.Contains(descLower, word) {
				score += 1
			}
		}

		if def.Body != "" {
			bodyLower := strings.ToLower(def.Body)
			for _, word := range queryWords {
				if strings.Contains(bodyLower, word) {
					score += 1
				}
			}
		}

		if score > 0 {
			results = append(results, scoredDef{def: def, score: score})
		}
	}

	if len(results) == 0 {
		return NewTextResponse(fmt.Sprintf("No skills found matching query: %s\n\nUse action='list' to see all available skills.", query)), nil
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].def.SkillPath < results[j].def.SkillPath
	})

	var sb strings.Builder
	fmt.Fprintf(&sb, "Skills matching \"%s\":\n\n", query)

	for _, r := range results {
		desc := r.def.Description
		if len(desc) > 120 {
			desc = desc[:117] + "..."
		}
		fmt.Fprintf(&sb, "- %s: %s\n", r.def.SkillPath, desc)
	}

	sb.WriteString("\nUse skill tool with action='load' and path='<skill-path>' to load a skill's instructions.")

	return NewTextResponse(sb.String()), nil
}

// findSiblingSkills returns skills that share the same parent skill path (excluding the skill itself).
func findSiblingSkills(skills []config.StoredSkill, skill config.StoredSkill) []config.StoredSkill {
	if skill.SkillPath == "" {
		return nil
	}
	// Determine the parent path. e.g. "go/error-handling" -> "go", "go" -> ""
	parentPath := ""
	if idx := strings.LastIndex(skill.SkillPath, "/"); idx >= 0 {
		parentPath = skill.SkillPath[:idx]
	}
	if parentPath == "" {
		// Top-level skill — no meaningful siblings to show.
		return nil
	}

	prefix := parentPath + "/"
	var siblings []config.StoredSkill
	for _, def := range skills {
		if def.SkillPath == skill.SkillPath {
			continue
		}
		if !strings.HasPrefix(def.SkillPath, prefix) {
			continue
		}
		// Only immediate siblings (no further nesting).
		remainder := def.SkillPath[len(prefix):]
		if !strings.Contains(remainder, "/") {
			siblings = append(siblings, def)
		}
	}

	sort.Slice(siblings, func(i, j int) bool {
		return siblings[i].SkillPath < siblings[j].SkillPath
	})

	return siblings
}

func truncateDescription(desc string, maxLen int) string {
	if len(desc) <= maxLen {
		return desc
	}
	return desc[:maxLen-3] + "..."
}

// SkillsAnnouncement returns a brief summary of top-level skills for system
// prompt injection. Operates purely on the provided slice — no filesystem.
// Returns empty string when no skills are eligible.
func SkillsAnnouncement(skills []config.StoredSkill) string {
	tops := namespaceMembers(skills, "")
	if len(tops) == 0 {
		return ""
	}

	sort.Slice(tops, func(i, j int) bool {
		return tops[i].SkillPath < tops[j].SkillPath
	})

	var sb strings.Builder
	sb.WriteString("\n\n<system-reminder>\nAvailable skills (use the skill tool to load):\n")
	for _, def := range tops {
		desc := def.Description
		if len(desc) > 80 {
			desc = desc[:77] + "..."
		}
		fmt.Fprintf(&sb, "- %s: %s", def.SkillPath, desc)
		if hasChildren(skills, def.SkillPath) {
			sb.WriteString(" (has sub-skills)")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("</system-reminder>")

	return sb.String()
}
