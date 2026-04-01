package materialize

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	skillcatalog "github.com/reliant-labs/reliant/internal/skills/catalog"
	skillscore "github.com/reliant-labs/reliant/internal/skills/core"
)

type IntegrationMode string

const (
	IntegrationModeFilesystem IntegrationMode = "filesystem"
	IntegrationModeTool       IntegrationMode = "tool"
)

type RetrievalConfig struct {
	MaxFiles       int
	MaxChunks      int
	ChunkBytes     int
	ChunkOverlap   int
	MaxPromptBytes int
}

type BuildInput struct {
	SupportingLimits skillscore.SupportingFilesLimits
	RetrievalConfig  RetrievalConfig
	QueryText        string
	IntegrationMode  IntegrationMode
}

type BuildResult struct {
	Skill       ActiveSkill
	Diagnostics []skillscore.Diagnostic
}

type DefinitionLoader func(skillcatalog.Definition) (skillcatalog.Definition, error)

func NormalizeIntegrationMode(raw string) IntegrationMode {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch IntegrationMode(normalized) {
	case IntegrationModeTool:
		return IntegrationModeTool
	case IntegrationModeFilesystem:
		fallthrough
	default:
		return IntegrationModeFilesystem
	}
}

func NormalizeRetrievalConfig(cfg RetrievalConfig) RetrievalConfig {
	if cfg.MaxFiles <= 0 {
		cfg.MaxFiles = 8
	}
	if cfg.MaxChunks <= 0 {
		cfg.MaxChunks = 12
	}
	if cfg.ChunkBytes <= 0 {
		cfg.ChunkBytes = 1200
	}
	if cfg.ChunkOverlap < 0 {
		cfg.ChunkOverlap = 0
	}
	if cfg.ChunkOverlap >= cfg.ChunkBytes {
		cfg.ChunkOverlap = cfg.ChunkBytes / 4
	}
	if cfg.MaxPromptBytes <= 0 {
		cfg.MaxPromptBytes = 12000
	}
	return cfg
}

func BuildRetrievalQuery(values ...string) string {
	items := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		key := strings.ToLower(normalized)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		items = append(items, normalized)
	}
	return strings.Join(items, "\n")
}

func BuildActiveSkillContext(active skillcatalog.Definition, input BuildInput, loader DefinitionLoader) BuildResult {
	loadedDefinition := active
	diagnostics := make([]skillscore.Diagnostic, 0, 1)
	if loader != nil {
		definition, err := loader(active)
		if err != nil {
			diagnostics = append(diagnostics, skillscore.Diagnostic{Path: active.Path, Scope: active.Scope, Message: "failed to load full skill definition: " + err.Error()})
		} else {
			loadedDefinition = definition
		}
	}

	mode := input.IntegrationMode
	if mode == "" {
		mode = IntegrationModeFilesystem
	}

	switch mode {
	case IntegrationModeTool:
		return buildToolActiveSkill(loadedDefinition, input, diagnostics)
	case IntegrationModeFilesystem:
		fallthrough
	default:
		return buildFilesystemActiveSkill(loadedDefinition, input, diagnostics)
	}
}

func buildFilesystemActiveSkill(definition skillcatalog.Definition, input BuildInput, diagnostics []skillscore.Diagnostic) BuildResult {
	active, fileDiagnostics := LoadSupportingFiles(definition, input.SupportingLimits)
	diagnostics = append(diagnostics, fileDiagnostics...)

	cfg := NormalizeRetrievalConfig(input.RetrievalConfig)
	query := strings.TrimSpace(input.QueryText)
	if query == "" {
		query = definition.Name + " " + definition.Description
	}

	active.SupportingFiles = SelectSupportingFileContent(active.SupportingFiles, query, cfg)
	active.Trusted = definition.Scope.IsTrustedForAutoActivation()
	return BuildResult{Skill: active, Diagnostics: diagnostics}
}

func buildToolActiveSkill(definition skillcatalog.Definition, input BuildInput, diagnostics []skillscore.Diagnostic) BuildResult {
	active := ActiveSkill{
		Definition: definition,
		Body:       definition.Body,
		Trusted:    definition.Scope.IsTrustedForAutoActivation(),
	}
	active.SupportingFiles = SelectSupportingFileContent(nil, strings.TrimSpace(input.QueryText), NormalizeRetrievalConfig(input.RetrievalConfig))
	return BuildResult{Skill: active, Diagnostics: diagnostics}
}

func BuildActiveSkillNotice(active ActiveSkill, diagnostics []skillscore.Diagnostic) skillscore.Notice {
	loaded := len(active.SupportingFiles)
	truncated := 0
	for _, file := range active.SupportingFiles {
		if file.Truncated {
			truncated++
		}
	}

	skipped := 0
	for _, diagnostic := range diagnostics {
		msg := strings.ToLower(strings.TrimSpace(diagnostic.Message))
		if strings.Contains(msg, "supporting files limit reached") || strings.HasPrefix(msg, "skipping ") {
			skipped++
		}
	}

	message := fmt.Sprintf("skill() %s", active.Definition.Name)
	if loaded > 0 || truncated > 0 || skipped > 0 {
		message = fmt.Sprintf("%s • supporting files: %d loaded", message, loaded)
		if truncated > 0 {
			message = fmt.Sprintf("%s, %d truncated", message, truncated)
		}
		if skipped > 0 {
			message = fmt.Sprintf("%s, %d skipped", message, skipped)
		}
	}

	return skillscore.Notice{Level: skillscore.NoticeLevelInfo, Message: message}
}

func LoadSupportingFiles(definition skillcatalog.Definition, rawLimits skillscore.SupportingFilesLimits) (ActiveSkill, []skillscore.Diagnostic) {
	limits := skillscore.NormalizeSupportingFilesLimits(rawLimits)
	files, diagnostics := collectSupportingFiles(definition, limits, nil)
	return ActiveSkill{
		Definition:      definition,
		Body:            definition.Body,
		SupportingFiles: files,
		Trusted:         definition.Scope.IsTrustedForAutoActivation(),
	}, diagnostics
}

func collectSupportingFiles(definition skillcatalog.Definition, limits skillscore.SupportingFilesLimits, diagnostics []skillscore.Diagnostic) ([]skillscore.SupportingFile, []skillscore.Diagnostic) {
	if definition.SkillDir == "" {
		return nil, diagnostics
	}

	files := make([]skillscore.SupportingFile, 0)
	_ = filepath.WalkDir(definition.SkillDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		rel, relErr := filepath.Rel(definition.SkillDir, path)
		if relErr != nil || strings.HasPrefix(rel, "..") {
			diagnostics = append(diagnostics, skillscore.Diagnostic{Path: path, Scope: definition.Scope, Message: "skipping unsupported supporting file path"})
			return nil
		}
		rel = filepath.ToSlash(rel)

		base := strings.ToLower(filepath.Base(rel))
		if base == "skill.md" {
			return nil
		}
		if ShouldExcludeSupportingFileName(base) || ShouldExcludeSupportingFilePath(rel) {
			return nil
		}

		if len(files) >= limits.MaxFiles {
			diagnostics = append(diagnostics, skillscore.Diagnostic{Path: definition.Path, Scope: definition.Scope, Message: "supporting files limit reached; additional files skipped"})
			return fs.SkipDir
		}

		content, readErr := os.ReadFile(path)
		if readErr != nil {
			diagnostics = append(diagnostics, skillscore.Diagnostic{Path: path, Scope: definition.Scope, Message: "failed to read supporting file: " + readErr.Error()})
			return nil
		}
		if !utf8.Valid(content) {
			diagnostics = append(diagnostics, skillscore.Diagnostic{Path: path, Scope: definition.Scope, Message: "skipping non-utf8 supporting file"})
			return nil
		}

		truncated := false
		if len(content) > limits.MaxBytes {
			content = content[:limits.MaxBytes]
			truncated = true
			diagnostics = append(diagnostics, skillscore.Diagnostic{Path: path, Scope: definition.Scope, Message: "supporting file truncated to configured size limit"})
		}

		files = append(files, skillscore.SupportingFile{
			RelativePath: rel,
			Content:      strings.TrimSpace(string(content)),
			Truncated:    truncated,
		})
		return nil
	})

	sort.Slice(files, func(i, j int) bool {
		return files[i].RelativePath < files[j].RelativePath
	})
	return files, diagnostics
}

var excludedSupportingFileNamePrefixes = []string{
	"license",
	"licence",
	"notice",
	"copying",
	"copyright",
}

var excludedSupportingFileRelativePathPrefixes = []string{
	"agents/",
}

var excludedSupportingAssetExtensions = map[string]struct{}{
	".png":  {},
	".jpg":  {},
	".jpeg": {},
	".gif":  {},
	".webp": {},
	".ico":  {},
	".bmp":  {},
	".tif":  {},
	".tiff": {},
	".avif": {},
	".svg":  {},
}

func ShouldExcludeSupportingFileName(base string) bool {
	base = strings.ToLower(strings.TrimSpace(base))
	if base == "" {
		return false
	}
	for _, prefix := range excludedSupportingFileNamePrefixes {
		if base == prefix ||
			strings.HasPrefix(base, prefix+".") ||
			strings.HasPrefix(base, prefix+"-") ||
			strings.HasPrefix(base, prefix+"_") {
			return true
		}
	}
	return false
}

func ShouldExcludeSupportingFilePath(rel string) bool {
	normalized := strings.ToLower(strings.TrimSpace(filepath.ToSlash(rel)))
	if normalized == "" {
		return false
	}

	for _, prefix := range excludedSupportingFileRelativePathPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return true
		}
	}

	if strings.HasPrefix(normalized, "assets/") {
		ext := strings.ToLower(filepath.Ext(normalized))
		if _, ok := excludedSupportingAssetExtensions[ext]; ok {
			return true
		}
	}

	return false
}

func SelectSupportingFileContent(files []skillscore.SupportingFile, query string, cfg RetrievalConfig) []skillscore.SupportingFile {
	cfg = NormalizeRetrievalConfig(cfg)
	if len(files) == 0 {
		return nil
	}

	limited := files
	if len(limited) > cfg.MaxFiles {
		limited = append([]skillscore.SupportingFile(nil), limited[:cfg.MaxFiles]...)
	}

	queryTerms := retrievalTerms(query)
	chunks := make([]retrievalChunk, 0)
	order := 0
	for _, f := range limited {
		segments := chunkContent(strings.TrimSpace(f.Content), cfg.ChunkBytes, cfg.ChunkOverlap)
		for _, seg := range segments {
			segTrim := strings.TrimSpace(seg)
			if segTrim == "" {
				continue
			}
			score := scoreChunk(segTrim, f.RelativePath, queryTerms)
			chunks = append(chunks, retrievalChunk{
				path:      f.RelativePath,
				content:   segTrim,
				truncated: f.Truncated,
				score:     score,
				order:     order,
			})
			order++
		}
	}

	if len(chunks) == 0 {
		return nil
	}

	sort.SliceStable(chunks, func(i, j int) bool {
		if chunks[i].score != chunks[j].score {
			return chunks[i].score > chunks[j].score
		}
		if chunks[i].path != chunks[j].path {
			return chunks[i].path < chunks[j].path
		}
		return chunks[i].order < chunks[j].order
	})

	if len(chunks) > cfg.MaxChunks {
		chunks = chunks[:cfg.MaxChunks]
	}

	byPath := make(map[string][]retrievalChunk)
	pathOrder := make([]string, 0)
	seenPath := make(map[string]struct{})
	usedBytes := 0
	for _, ch := range chunks {
		if usedBytes >= cfg.MaxPromptBytes {
			break
		}
		remaining := cfg.MaxPromptBytes - usedBytes
		content := ch.content
		if len(content) > remaining {
			content = strings.TrimSpace(content[:remaining])
			if content == "" {
				continue
			}
			ch.truncated = true
		}
		if _, ok := seenPath[ch.path]; !ok {
			seenPath[ch.path] = struct{}{}
			pathOrder = append(pathOrder, ch.path)
		}
		ch.content = content
		byPath[ch.path] = append(byPath[ch.path], ch)
		usedBytes += len(content)
	}

	result := make([]skillscore.SupportingFile, 0, len(pathOrder))
	for _, path := range pathOrder {
		chunksForPath := byPath[path]
		if len(chunksForPath) == 0 {
			continue
		}
		sort.SliceStable(chunksForPath, func(i, j int) bool {
			return chunksForPath[i].order < chunksForPath[j].order
		})

		parts := make([]string, 0, len(chunksForPath))
		truncated := false
		for _, ch := range chunksForPath {
			parts = append(parts, ch.content)
			if ch.truncated {
				truncated = true
			}
		}
		result = append(result, skillscore.SupportingFile{
			RelativePath: path,
			Content:      strings.Join(parts, "\n\n"),
			Truncated:    truncated,
		})
	}

	return result
}

type retrievalChunk struct {
	path      string
	content   string
	truncated bool
	score     int
	order     int
}

func chunkContent(content string, chunkBytes, overlap int) []string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}
	if len(trimmed) <= chunkBytes {
		return []string{trimmed}
	}

	step := chunkBytes - overlap
	if step <= 0 {
		step = chunkBytes
	}

	chunks := make([]string, 0)
	for start := 0; start < len(trimmed); start += step {
		end := start + chunkBytes
		if end > len(trimmed) {
			end = len(trimmed)
		}
		segment := strings.TrimSpace(trimmed[start:end])
		if segment != "" {
			chunks = append(chunks, segment)
		}
		if end >= len(trimmed) {
			break
		}
	}
	return chunks
}

var autoSkillTokenPattern = regexp.MustCompile(`[a-z0-9]+`)

func retrievalTerms(query string) []string {
	terms := autoSkillTokenPattern.FindAllString(strings.ToLower(query), -1)
	seen := make(map[string]struct{}, len(terms))
	out := make([]string, 0, len(terms))
	for _, term := range terms {
		if len(term) < 2 {
			continue
		}
		if _, ok := seen[term]; ok {
			continue
		}
		seen[term] = struct{}{}
		out = append(out, term)
	}
	return out
}

func scoreChunk(content string, path string, terms []string) int {
	lower := strings.ToLower(content)
	pathLower := strings.ToLower(path)
	score := 0
	for _, term := range terms {
		if strings.Contains(pathLower, term) {
			score += 4
		}
		if strings.Contains(lower, term) {
			score += 2
		}
	}
	if score == 0 {
		score = 1
	}
	return score
}
