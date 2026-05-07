package catalog

import (
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

type DiscoverInput struct {
	ProjectPath               string
	DisabledDefinitionPathSet map[string]struct{}
	LoadFullDefinitions       bool
}

type root struct {
	Path  string
	Scope skillscore.Scope
}

type skillDefinition struct {
	Path   string
	Format skillscore.SkillFormat
}

type frontmatter struct {
	Name          string                 `yaml:"name"`
	Description   string                 `yaml:"description"`
	License       string                 `yaml:"license,omitempty"`
	Compatibility string                 `yaml:"compatibility,omitempty"`
	Metadata      map[string]interface{} `yaml:"metadata,omitempty"`
	AllowedTools  string                 `yaml:"allowed-tools,omitempty"`

	// Claude-compatible fields
	ArgumentHint           string `yaml:"argument-hint,omitempty"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation,omitempty"`
	UserInvocable          *bool  `yaml:"user-invocable,omitempty"`
	Paths                  string `yaml:"paths,omitempty"`
}

var allowedSkillFrontmatterFields = map[string]struct{}{
	"name":          {},
	"description":   {},
	"license":       {},
	"compatibility": {},
	"metadata":      {},
	"allowed-tools": {},

	// Claude-compatible fields
	"argument-hint":            {},
	"disable-model-invocation": {},
	"user-invocable":           {},
	"paths":                    {},
}

var builtinSkillPaths = []string{
	"reliant-config/SKILL.md",
	"workflow-builder/SKILL.md",
	"conflict-resolver/SKILL.md",
	"simplification-assessment/SKILL.md",
	"research-methodology/SKILL.md",
	"testing-methodology/SKILL.md",
	"git-operations/SKILL.md",
	"planning-methodology/SKILL.md",
	"documentation-writing/SKILL.md",
	"ux-design/SKILL.md",
	"general-agent/SKILL.md",
	"forge-methodology/SKILL.md",
	// code-review parent + sub-skills
	"code-review/SKILL.md",
	"code-review/code-hygiene-review/SKILL.md",
	"code-review/performance-review/SKILL.md",
	"code-review/security-review/SKILL.md",
	"code-review/architecture-review/SKILL.md",
	"code-review/ux-review-methodology/SKILL.md",
	// debug parent + sub-skills
	"debug/SKILL.md",
	"debug/reproduction-methodology/SKILL.md",
	// refactor parent + sub-skills
	"refactor/SKILL.md",
	"refactor/migration-guidance/SKILL.md",
	"pivot-on-friction/SKILL.md",
	"validation-harness/SKILL.md",
}

// BuiltinSkillsFS is exported for test use.
//
//go:embed builtin/reliant-config/SKILL.md builtin/workflow-builder/SKILL.md builtin/conflict-resolver/SKILL.md builtin/simplification-assessment/SKILL.md builtin/research-methodology/SKILL.md builtin/testing-methodology/SKILL.md builtin/git-operations/SKILL.md builtin/planning-methodology/SKILL.md builtin/documentation-writing/SKILL.md builtin/ux-design/SKILL.md builtin/general-agent/SKILL.md builtin/forge-methodology/SKILL.md builtin/code-review/SKILL.md builtin/code-review/code-hygiene-review/SKILL.md builtin/code-review/performance-review/SKILL.md builtin/code-review/security-review/SKILL.md builtin/code-review/architecture-review/SKILL.md builtin/code-review/ux-review-methodology/SKILL.md builtin/debug/SKILL.md builtin/debug/reproduction-methodology/SKILL.md builtin/refactor/SKILL.md builtin/refactor/migration-guidance/SKILL.md builtin/pivot-on-friction/SKILL.md builtin/validation-harness/SKILL.md
var BuiltinSkillsFS embed.FS

func ParseSkillMarkdown(path string, scope skillscore.Scope, data []byte) (Definition, error) {
	return parseSkillMarkdown(path, scope, data, true)
}

func ParseSkillMarkdownFrontmatter(path string, scope skillscore.Scope, data []byte) (Definition, error) {
	return parseSkillMarkdown(path, scope, data, false)
}

func isExternalProviderScope(scope skillscore.Scope) bool {
	switch scope {
	case skillscore.ScopeClaude, skillscore.ScopeClaudeGlobal,
		skillscore.ScopeCodexProject, skillscore.ScopeCodexAgents, skillscore.ScopeCodexGlobal:
		return true
	default:
		return false
	}
}

func parseSkillMarkdown(path string, scope skillscore.Scope, data []byte, includeBody bool) (Definition, error) {
	content := strings.TrimSpace(string(data))
	if content == "" {
		return Definition{}, fmt.Errorf("empty SKILL.md")
	}

	front, body, err := extractSkillFrontmatterAndBody(content)
	if err != nil {
		return Definition{}, err
	}

	lenient := isExternalProviderScope(scope)

	fm, err := parseFrontmatter([]byte(front), lenient)
	if err != nil {
		return Definition{}, err
	}

	metadata := normalizeSkillMetadata(fm.Metadata)
	if err := ValidateAgentSkillMarkdownFrontmatter(path, fm.Name, fm.Description, fm.Compatibility, metadata); err != nil {
		return Definition{}, err
	}

	skillBody := ""
	if includeBody {
		skillBody = body
	}

	name := skillscore.NormalizeSkillName(fm.Name)
	return Definition{
		Name:                   name,
		NormalizedKey:          name,
		Description:            strings.TrimSpace(fm.Description),
		License:                strings.TrimSpace(fm.License),
		Compatibility:          strings.TrimSpace(fm.Compatibility),
		Metadata:               metadata,
		AllowedTools:           parseAllowedToolsList(fm.AllowedTools),
		Body:                   skillBody,
		Path:                   path,
		Scope:                  scope,
		Format:                 skillscore.SkillFormatClaudeMarkdown,
		SkillDir:               filepath.Dir(path),
		ArgumentHint:           strings.TrimSpace(fm.ArgumentHint),
		DisableModelInvocation: fm.DisableModelInvocation,
		UserInvocable:          fm.UserInvocable,
		Paths:                  strings.TrimSpace(fm.Paths),
	}, nil
}

func extractSkillFrontmatterAndBody(content string) (frontmatter string, body string, err error) {
	if !strings.HasPrefix(content, "---") {
		return "", "", fmt.Errorf("missing YAML frontmatter")
	}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 || strings.TrimSpace(parts[0]) != "" {
		return "", "", fmt.Errorf("invalid YAML frontmatter terminator")
	}

	frontmatter = strings.TrimSpace(parts[1])
	if frontmatter == "" {
		return "", "", fmt.Errorf("invalid YAML frontmatter")
	}

	body = strings.TrimSpace(parts[2])
	return frontmatter, body, nil
}

func parseFrontmatter(frontBlob []byte, lenient bool) (frontmatter, error) {
	if !lenient {
		if err := validateAllowedFrontmatterFields(frontBlob); err != nil {
			return frontmatter{}, err
		}
	}

	dec := yaml.NewDecoder(strings.NewReader(string(frontBlob)))
	dec.KnownFields(!lenient)
	var fm frontmatter
	if err := dec.Decode(&fm); err != nil {
		return frontmatter{}, fmt.Errorf("invalid YAML frontmatter: %w", err)
	}

	return fm, nil
}

func validateAllowedFrontmatterFields(frontBlob []byte) error {
	var raw map[string]interface{}
	if err := yaml.Unmarshal(frontBlob, &raw); err != nil {
		return fmt.Errorf("invalid YAML frontmatter: %w", err)
	}

	extra := make([]string, 0)
	for key := range raw {
		if _, ok := allowedSkillFrontmatterFields[key]; !ok {
			extra = append(extra, key)
		}
	}
	if len(extra) == 0 {
		return nil
	}

	sort.Strings(extra)
	allowed := make([]string, 0, len(allowedSkillFrontmatterFields))
	for key := range allowedSkillFrontmatterFields {
		allowed = append(allowed, key)
	}
	sort.Strings(allowed)

	return fmt.Errorf("unexpected fields in frontmatter: %s (allowed: %s)", strings.Join(extra, ", "), strings.Join(allowed, ", "))
}

func normalizeSkillMetadata(raw map[string]interface{}) map[string]string {
	if len(raw) == 0 {
		return nil
	}

	out := make(map[string]string, len(raw))
	for key, value := range raw {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			out[trimmedKey] = ""
			continue
		}

		switch typed := value.(type) {
		case nil:
			out[trimmedKey] = ""
		case string:
			out[trimmedKey] = typed
		default:
			out[trimmedKey] = fmt.Sprint(typed)
		}
	}
	return out
}

func parseAllowedToolsList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return nil
	}
	return parts
}

func LoadFullDefinition(definition Definition) (Definition, error) {
	if strings.TrimSpace(definition.Body) != "" {
		return definition, nil
	}

	var (
		blob []byte
		err  error
	)
	if definition.Scope == skillscore.ScopeBuiltin {
		blob, err = ReadBuiltinSkillDefinition(definition.Path)
	} else {
		blob, err = os.ReadFile(definition.Path)
	}
	if err != nil {
		return Definition{}, fmt.Errorf("failed to read skill definition %q: %w", definition.Path, err)
	}

	loaded, err := ParseSkillMarkdown(definition.Path, definition.Scope, blob)
	if err != nil {
		return Definition{}, err
	}
	if strings.TrimSpace(definition.SkillDir) != "" {
		loaded.SkillDir = definition.SkillDir
	}
	return loaded, nil
}

func ValidateSkillCoreFields(name, description, compatibility string, metadata map[string]string) error {
	normalizedName := normalizeSkillNameForValidation(name)
	if normalizedName == "" {
		return fmt.Errorf("missing required field: name")
	}
	if len(normalizedName) > 64 {
		return fmt.Errorf("invalid skill name %q: must be 1-64 characters", normalizedName)
	}
	if normalizedName != strings.ToLower(normalizedName) {
		return fmt.Errorf("invalid skill name %q: must be lowercase", normalizedName)
	}
	if strings.HasPrefix(normalizedName, "-") || strings.HasSuffix(normalizedName, "-") {
		return fmt.Errorf("invalid skill name %q: must not start or end with hyphen", normalizedName)
	}
	if strings.Contains(normalizedName, "--") {
		return fmt.Errorf("invalid skill name %q: must not contain consecutive hyphens", normalizedName)
	}
	for _, r := range normalizedName {
		if r == '-' {
			continue
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return fmt.Errorf("invalid skill name %q: only lowercase letters, digits, and hyphens are allowed", normalizedName)
	}

	trimmedDescription := strings.TrimSpace(description)
	if trimmedDescription == "" {
		return fmt.Errorf("missing required field: description")
	}
	if len(trimmedDescription) > 1024 {
		return fmt.Errorf("invalid description: must be 1-1024 characters")
	}

	trimmedCompatibility := strings.TrimSpace(compatibility)
	if trimmedCompatibility != "" && len(trimmedCompatibility) > 500 {
		return fmt.Errorf("invalid compatibility: must be 1-500 characters when provided")
	}

	for key := range metadata {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("invalid metadata: keys must be non-empty strings")
		}
	}

	return nil
}

func normalizeSkillNameForValidation(name string) string {
	return strings.TrimSpace(norm.NFKC.String(name))
}

func ValidateAgentSkillMarkdownFrontmatter(path, name, description, compatibility string, metadata map[string]string) error {
	if err := ValidateSkillCoreFields(name, description, compatibility, metadata); err != nil {
		return err
	}

	trimmedName := skillscore.NormalizeSkillName(name)
	parentDir := skillscore.NormalizeSkillName(filepath.Base(filepath.Dir(path)))
	if parentDir == "" || parentDir == "." || parentDir == string(filepath.Separator) {
		return fmt.Errorf("invalid skill path %q: unable to determine parent directory", path)
	}
	if trimmedName != parentDir {
		return fmt.Errorf("invalid skill name %q: must match parent directory %q", trimmedName, parentDir)
	}

	return nil
}

func ReadBuiltinSkillDefinition(path string) ([]byte, error) {
	normalized := filepath.ToSlash(filepath.Clean(path))
	for _, p := range builtinSkillPaths {
		canonical := filepath.ToSlash(filepath.Clean(filepath.Join("builtin", p)))
		legacy := filepath.ToSlash(filepath.Clean(p))
		if normalized == canonical || normalized == legacy {
			return BuiltinSkillsFS.ReadFile(filepath.ToSlash(filepath.Join("builtin", p)))
		}
	}
	return nil, fs.ErrNotExist
}

func builtinSkills(loadFullDefinitions bool) []Definition {
	var defs []Definition
	for _, p := range builtinSkillPaths {
		blob, err := ReadBuiltinSkillDefinition(filepath.Join("builtin", p))
		if err != nil {
			continue
		}

		var definition Definition
		if loadFullDefinitions {
			definition, err = ParseSkillMarkdown(p, skillscore.ScopeBuiltin, blob)
		} else {
			definition, err = ParseSkillMarkdownFrontmatter(p, skillscore.ScopeBuiltin, blob)
		}
		if err != nil {
			continue
		}

		definition.SkillDir = filepath.Dir(filepath.Join("builtin", p))
		// Set SkillPath for builtins so the skill tool can find them by path.
		// For builtins, p is like "code-review/SKILL.md" or "code-review/security-review/SKILL.md",
		// so filepath.Dir(p) gives "code-review" or "code-review/security-review".
		if definition.SkillPath == "" {
			definition.SkillPath = filepath.ToSlash(filepath.Dir(p))
			definition.NormalizedKey = definition.SkillPath
		}
		defs = append(defs, definition)
	}
	return defs
}

// discoverAll discovers all skills (including nested) from all roots.
func discoverAll(input DiscoverInput) Snapshot {
	result := Snapshot{
		ByName:       make(map[string]Definition),
		ShadowedBy:   make(map[string]string),
		ShadowedFrom: make(map[string]string),
	}

	roots := discoveryRoots(input.ProjectPath)
	for _, r := range roots {
		defs := listSkillDefinitions(r.Path)
		for _, def := range defs {
			if len(input.DisabledDefinitionPathSet) > 0 {
				if _, disabled := input.DisabledDefinitionPathSet[skillscore.CanonicalDefinitionPath(def.Path)]; disabled {
					continue
				}
			}
			blob, err := os.ReadFile(def.Path)
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, skillscore.Diagnostic{Path: def.Path, Scope: r.Scope, Message: "failed to read skill definition: " + err.Error()})
				continue
			}

			definition, err := ParseSkillMarkdownFrontmatter(def.Path, r.Scope, blob)
			if err != nil {
				result.Diagnostics = append(result.Diagnostics, skillscore.Diagnostic{Path: def.Path, Scope: r.Scope, Message: err.Error()})
				continue
			}
			if input.LoadFullDefinitions {
				fullDefinition, loadErr := LoadFullDefinition(definition)
				if loadErr != nil {
					result.Diagnostics = append(result.Diagnostics, skillscore.Diagnostic{Path: def.Path, Scope: r.Scope, Message: "failed to load full skill definition: " + loadErr.Error()})
					continue
				}
				definition = fullDefinition
			}

			// Compute the hierarchical skill path relative to the discovery root.
			// e.g. root=/project/.reliant/skills, skillDir=/project/.reliant/skills/go/error-handling
			// => skillPath = "go/error-handling"
			skillPath := computeSkillPath(r.Path, definition.SkillDir)
			if skillPath != "" {
				definition.SkillPath = skillPath
				definition.NormalizedKey = skillPath
			}

			// Detect sub-skills.
			definition.HasChildren = hasChildSkillDirs(definition.SkillDir)

			mergeDefinition(&result, definition)
		}
	}

	for _, definition := range builtinSkills(input.LoadFullDefinitions) {
		mergeDefinition(&result, definition)
	}

	result.Definitions = make([]Definition, 0, len(result.ByName))
	for _, definition := range result.ByName {
		result.Definitions = append(result.Definitions, definition)
	}
	sortDefinitions(result.Definitions)
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		if result.Diagnostics[i].Scope.Priority() != result.Diagnostics[j].Scope.Priority() {
			return result.Diagnostics[i].Scope.Priority() < result.Diagnostics[j].Scope.Priority()
		}
		return result.Diagnostics[i].Path < result.Diagnostics[j].Path
	})

	return result
}

// Discover discovers only top-level skills (depth 1). Nested skills are discovered on demand.
func Discover(input DiscoverInput) Snapshot {
	all := discoverAll(input)

	// Filter to top-level skills only (no "/" in SkillPath, or builtin skills).
	result := Snapshot{
		ByName:       make(map[string]Definition, len(all.ByName)),
		ShadowedBy:   all.ShadowedBy,
		ShadowedFrom: all.ShadowedFrom,
		Diagnostics:  all.Diagnostics,
	}
	for key, def := range all.ByName {
		if isTopLevelSkill(def) {
			result.ByName[key] = def
		}
	}
	result.Definitions = make([]Definition, 0, len(result.ByName))
	for _, def := range result.ByName {
		result.Definitions = append(result.Definitions, def)
	}
	sortDefinitions(result.Definitions)

	return result
}

// DiscoverAll discovers all skills including nested ones. Used for search.
func DiscoverAll(input DiscoverInput) Snapshot {
	return discoverAll(input)
}

// DiscoverChildren returns immediate child skills of the given parent skill path.
// For example, DiscoverChildren(input, "go") returns skills like "go/error-handling", "go/defer".
func DiscoverChildren(input DiscoverInput, parentPath string) []Definition {
	all := discoverAll(input)
	prefix := parentPath + "/"
	var children []Definition
	for _, def := range all.Definitions {
		if !strings.HasPrefix(def.SkillPath, prefix) {
			continue
		}
		// Only immediate children: no additional "/" after the prefix.
		remainder := def.SkillPath[len(prefix):]
		if !strings.Contains(remainder, "/") {
			children = append(children, def)
		}
	}
	sortDefinitions(children)
	return children
}

// computeSkillPath computes the hierarchical path of a skill relative to its discovery root.
// e.g. rootPath="/home/.reliant/skills", skillDir="/home/.reliant/skills/go/error-handling"
// => "go/error-handling"
func computeSkillPath(rootPath, skillDir string) string {
	rel, err := filepath.Rel(rootPath, skillDir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return ""
	}
	// Always use forward slashes for skill paths.
	return filepath.ToSlash(rel)
}

// hasChildSkillDirs checks if any immediate subdirectory contains a SKILL.md file.
func hasChildSkillDirs(skillDir string) bool {
	entries, err := os.ReadDir(skillDir)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		childSkill := filepath.Join(skillDir, entry.Name(), "SKILL.md")
		if _, err := os.Stat(childSkill); err == nil {
			return true
		}
	}
	return false
}

// isTopLevelSkill returns true if the skill is at the top level (depth 1).
func isTopLevelSkill(def Definition) bool {
	return !strings.Contains(def.SkillPath, "/")
}

func sortDefinitions(defs []Definition) {
	sort.Slice(defs, func(i, j int) bool {
		if defs[i].Scope.Priority() != defs[j].Scope.Priority() {
			return defs[i].Scope.Priority() < defs[j].Scope.Priority()
		}
		if defs[i].SkillPath != defs[j].SkillPath {
			return defs[i].SkillPath < defs[j].SkillPath
		}
		return defs[i].Path < defs[j].Path
	})
}

func cloneDefinition(in Definition) Definition {
	out := in
	if in.Metadata != nil {
		out.Metadata = make(map[string]string, len(in.Metadata))
		for k, v := range in.Metadata {
			out.Metadata[k] = v
		}
	}
	if in.AllowedTools != nil {
		out.AllowedTools = append([]string(nil), in.AllowedTools...)
	}
	return out
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := Snapshot{
		Definitions:  make([]Definition, 0, len(in.Definitions)),
		ByName:       make(map[string]Definition, len(in.ByName)),
		Diagnostics:  make([]skillscore.Diagnostic, len(in.Diagnostics)),
		ShadowedBy:   make(map[string]string, len(in.ShadowedBy)),
		ShadowedFrom: make(map[string]string, len(in.ShadowedFrom)),
	}
	for _, definition := range in.Definitions {
		out.Definitions = append(out.Definitions, cloneDefinition(definition))
	}
	for key, definition := range in.ByName {
		out.ByName[key] = cloneDefinition(definition)
	}
	copy(out.Diagnostics, in.Diagnostics)
	for k, v := range in.ShadowedBy {
		out.ShadowedBy[k] = v
	}
	for k, v := range in.ShadowedFrom {
		out.ShadowedFrom[k] = v
	}
	return out
}

func shouldReplace(existing Definition, candidate Definition) bool {
	if candidate.Scope.Priority() != existing.Scope.Priority() {
		return candidate.Scope.Priority() < existing.Scope.Priority()
	}
	return candidate.Path < existing.Path
}

func mergeDefinition(result *Snapshot, definition Definition) {
	if existing, ok := result.ByName[definition.NormalizedKey]; ok {
		if shouldReplace(existing, definition) {
			result.ShadowedBy[existing.Path] = definition.Path
			result.ShadowedFrom[definition.Path] = existing.Path
			result.ByName[definition.NormalizedKey] = definition
		} else {
			result.ShadowedBy[definition.Path] = existing.Path
			result.ShadowedFrom[existing.Path] = definition.Path
		}
		return
	}

	result.ByName[definition.NormalizedKey] = definition
}

func discoveryRoots(projectPath string) []root {
	homeDir, _ := os.UserHomeDir()
	roots := []root{
		// Reliant paths (highest priority)
		{Path: filepath.Join(projectPath, ".reliant.local", "skills"), Scope: skillscore.ScopeProjectLocal},
		{Path: filepath.Join(projectPath, ".reliant", "skills"), Scope: skillscore.ScopeProject},
		// Claude paths
		{Path: filepath.Join(projectPath, ".claude", "skills"), Scope: skillscore.ScopeClaude},
		// Codex paths
		{Path: filepath.Join(projectPath, ".codex", "skills"), Scope: skillscore.ScopeCodexProject},
		{Path: filepath.Join(projectPath, ".agents", "skills"), Scope: skillscore.ScopeCodexAgents},
	}

	if homeDir != "" {
		roots = append(roots,
			root{Path: filepath.Join(homeDir, ".reliant", "skills"), Scope: skillscore.ScopeGlobal},
			root{Path: filepath.Join(homeDir, ".claude", "skills"), Scope: skillscore.ScopeClaudeGlobal},
			root{Path: filepath.Join(homeDir, ".codex", "skills"), Scope: skillscore.ScopeCodexGlobal},
		)
	}
	for _, r := range roots {
		slog.Debug("[Skills] discoveryRoot", "path", r.Path, "scope", r.Scope, "projectPath", projectPath)
	}
	return roots
}

func listSkillDefinitions(rootPath string) []skillDefinition {
	info, err := os.Stat(rootPath)
	if err != nil || !info.IsDir() {
		return nil
	}
	var defs []skillDefinition
	_ = filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		base := strings.ToLower(filepath.Base(path))
		if base == "skill.md" {
			defs = append(defs, skillDefinition{Path: path, Format: skillscore.SkillFormatClaudeMarkdown})
		}
		return nil
	})
	sort.Slice(defs, func(i, j int) bool {
		if defs[i].Path == defs[j].Path {
			return defs[i].Format < defs[j].Format
		}
		return defs[i].Path < defs[j].Path
	})
	return defs
}
