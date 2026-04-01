package catalog

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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
}

var allowedSkillFrontmatterFields = map[string]struct{}{
	"name":          {},
	"description":   {},
	"license":       {},
	"compatibility": {},
	"metadata":      {},
	"allowed-tools": {},
}

const builtinSkillCreatorPath = "skill-creator/SKILL.md"

//go:embed builtin/skill-creator/SKILL.md
var builtinSkillsFS embed.FS

func ParseSkillMarkdown(path string, scope skillscore.Scope, data []byte) (Definition, error) {
	return parseSkillMarkdown(path, scope, data, true)
}

func ParseSkillMarkdownFrontmatter(path string, scope skillscore.Scope, data []byte) (Definition, error) {
	return parseSkillMarkdown(path, scope, data, false)
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

	fm, err := parseKnownFrontmatter([]byte(front))
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
		Name:          name,
		NormalizedKey: name,
		Description:   strings.TrimSpace(fm.Description),
		License:       strings.TrimSpace(fm.License),
		Compatibility: strings.TrimSpace(fm.Compatibility),
		Metadata:      metadata,
		AllowedTools:  parseAllowedToolsList(fm.AllowedTools),
		Body:          skillBody,
		Path:          path,
		Scope:         scope,
		Format:        skillscore.SkillFormatClaudeMarkdown,
		SkillDir:      filepath.Dir(path),
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

func parseKnownFrontmatter(frontBlob []byte) (frontmatter, error) {
	if err := validateAllowedFrontmatterFields(frontBlob); err != nil {
		return frontmatter{}, err
	}

	dec := yaml.NewDecoder(strings.NewReader(string(frontBlob)))
	dec.KnownFields(true)
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
	canonical := filepath.ToSlash(filepath.Clean(filepath.Join("builtin", builtinSkillCreatorPath)))
	legacy := filepath.ToSlash(filepath.Clean(builtinSkillCreatorPath))
	if normalized != canonical && normalized != legacy {
		return nil, fs.ErrNotExist
	}
	return builtinSkillsFS.ReadFile("builtin/skill-creator/SKILL.md")
}

func builtinSkills(loadFullDefinitions bool) []Definition {
	blob, err := ReadBuiltinSkillDefinition(filepath.Join("builtin", builtinSkillCreatorPath))
	if err != nil {
		return nil
	}

	var definition Definition
	if loadFullDefinitions {
		definition, err = ParseSkillMarkdown(builtinSkillCreatorPath, skillscore.ScopeBuiltin, blob)
	} else {
		definition, err = ParseSkillMarkdownFrontmatter(builtinSkillCreatorPath, skillscore.ScopeBuiltin, blob)
	}
	if err != nil {
		return nil
	}

	definition.SkillDir = filepath.Dir(filepath.Join("builtin", builtinSkillCreatorPath))
	return []Definition{definition}
}

func Discover(input DiscoverInput) Snapshot {
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
	sort.Slice(result.Definitions, func(i, j int) bool {
		if result.Definitions[i].Scope.Priority() != result.Definitions[j].Scope.Priority() {
			return result.Definitions[i].Scope.Priority() < result.Definitions[j].Scope.Priority()
		}
		if result.Definitions[i].Name != result.Definitions[j].Name {
			return result.Definitions[i].Name < result.Definitions[j].Name
		}
		return result.Definitions[i].Path < result.Definitions[j].Path
	})
	sort.Slice(result.Diagnostics, func(i, j int) bool {
		if result.Diagnostics[i].Scope.Priority() != result.Diagnostics[j].Scope.Priority() {
			return result.Diagnostics[i].Scope.Priority() < result.Diagnostics[j].Scope.Priority()
		}
		return result.Diagnostics[i].Path < result.Diagnostics[j].Path
	})

	return result
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
		{Path: filepath.Join(projectPath, ".reliant.local", "skills"), Scope: skillscore.ScopeProjectLocal},
		{Path: filepath.Join(projectPath, ".reliant", "skills"), Scope: skillscore.ScopeProject},
	}
	if homeDir != "" {
		roots = append(roots, root{Path: filepath.Join(homeDir, ".reliant", "skills"), Scope: skillscore.ScopeGlobal})
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

type CatalogIndex struct {
	mu        sync.RWMutex
	snapshots map[string]indexedSnapshot
}

type indexedSnapshot struct {
	Result    Snapshot
	ExpiresAt time.Time
}

var (
	catalogIndexTTL     = 2 * time.Second
	defaultCatalogIndex = NewCatalogIndex()
)

func NewCatalogIndex() *CatalogIndex {
	return &CatalogIndex{snapshots: map[string]indexedSnapshot{}}
}

func DefaultCatalogIndex() *CatalogIndex {
	return defaultCatalogIndex
}

func discoveryCacheKey(projectPath string, disabledDefinitionPath map[string]struct{}, loadFullDefinitions bool) string {
	homeDir, _ := os.UserHomeDir()
	base := filepath.Clean(projectPath) + "|" + filepath.Clean(homeDir)
	if loadFullDefinitions {
		base += "|full=1"
	}
	if len(disabledDefinitionPath) == 0 {
		return base
	}

	paths := make([]string, 0, len(disabledDefinitionPath))
	for definitionPath := range disabledDefinitionPath {
		canonical := skillscore.CanonicalDefinitionPath(definitionPath)
		if canonical != "" {
			paths = append(paths, canonical)
		}
	}
	if len(paths) == 0 {
		return base
	}

	sort.Strings(paths)
	return base + "|disabled=" + strings.Join(paths, ",")
}

func (c *CatalogIndex) Discover(_ context.Context, input DiscoverInput) Snapshot {
	cacheKey := discoveryCacheKey(input.ProjectPath, input.DisabledDefinitionPathSet, input.LoadFullDefinitions)

	now := time.Now()
	c.mu.RLock()
	cached, ok := c.snapshots[cacheKey]
	c.mu.RUnlock()
	if ok && now.Before(cached.ExpiresAt) {
		return cloneSnapshot(cached.Result)
	}

	result := Discover(input)

	c.mu.Lock()
	c.snapshots[cacheKey] = indexedSnapshot{Result: cloneSnapshot(result), ExpiresAt: now.Add(catalogIndexTTL)}
	c.mu.Unlock()

	return result
}

func (c *CatalogIndex) PreloadProject(ctx context.Context, projectPath string) {
	if strings.TrimSpace(projectPath) == "" {
		return
	}
	select {
	case <-ctx.Done():
		return
	default:
	}
	_ = c.Discover(ctx, DiscoverInput{ProjectPath: projectPath, LoadFullDefinitions: false})
}

func (c *CatalogIndex) PreloadProjects(ctx context.Context, projectPaths []string) {
	seen := make(map[string]struct{}, len(projectPaths))
	for _, projectPath := range projectPaths {
		clean := strings.TrimSpace(projectPath)
		if clean == "" {
			continue
		}
		clean = filepath.Clean(clean)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		c.PreloadProject(ctx, clean)
	}
}

func (c *CatalogIndex) Invalidate(projectPath string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if strings.TrimSpace(projectPath) == "" {
		c.snapshots = map[string]indexedSnapshot{}
		return
	}

	base := filepath.Clean(projectPath) + "|"
	for key := range c.snapshots {
		if strings.HasPrefix(key, base) {
			delete(c.snapshots, key)
		}
	}
}

func (c *CatalogIndex) SnapshotCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.snapshots)
}
