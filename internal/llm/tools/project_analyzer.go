// Copyright (c) 2025 Reliant Labs
package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/rctx"
)

// Current blockers:
// - Uses filepath.Walk extensively for recursive directory traversal (detectProjectType,
//   findBuildFiles, detectLanguages, findTestPatterns).
// - Uses os.Stat for file existence checks.
// - The daemon interface has GlobFiles() and StatFile() but no recursive Walk primitive,
//   so migration would require GlobFiles("**/*") + StatFile per entry, which is inefficient.
// - This tool is feature-flagged (project_analyzer_disabled) and disabled by default.
// - Not worth investing in migration until the tool is enabled and actively used.

// ProjectAnalyzer provides project analysis capabilities
type ProjectAnalyzer struct {
	rootPath string
}

// NewProjectAnalyzer creates a new project analyzer
func NewProjectAnalyzer(rootPath string) *ProjectAnalyzer {
	if rootPath == "" {
		rootPath, _ = os.Getwd()
	}
	return &ProjectAnalyzer{
		rootPath: rootPath,
	}
}

// ProjectAnalyzerParams defines the parameters for project analysis
type ProjectAnalyzerParams struct {
	Action string `json:"action" jsonschema:"required,enum=detect_type,enum=find_build_files,enum=detect_languages,enum=find_tests,description=Analysis action to perform"`
	Path   string `json:"path,omitempty" jsonschema:"description=Path to analyze (optional defaults to project root)"`
}

// ProjectAnalyzerTool is the tool interface for project analysis
type ProjectAnalyzerTool struct {
	analyzer *ProjectAnalyzer
}

// NewProjectAnalyzerTool creates a new project analyzer tool
func NewProjectAnalyzerTool() Tool {
	tool := &ProjectAnalyzerTool{
		analyzer: NewProjectAnalyzer(""),
	}
	return NewToolWrapper[ProjectAnalyzerParams, interface{}](tool)
}

func (t *ProjectAnalyzerTool) Name() string {
	return "project_analyzer"
}

func (t *ProjectAnalyzerTool) RequiresPermission(params ProjectAnalyzerParams) (bool, error) {
	// project_analyzer tool doesn't require permissions as it's read-only
	return false, nil
}

func (t *ProjectAnalyzerTool) Description() string {
	return "Analyzes project structure, detects languages, build systems, and test frameworks"
}

func (t *ProjectAnalyzerTool) Execute(rctx *rctx.ToolContext, params ProjectAnalyzerParams) (interface{}, error) {
	// Use params.Path if provided, otherwise require project context
	path := params.Path
	if path == "" {
		if rctx.Project == nil || rctx.Project.Path == "" {
			return nil, fmt.Errorf("no project context available and no path provided")
		}
		path = rctx.Project.Path
	}

	switch params.Action {
	case "detect_type":
		return t.detectProjectType(path)
	case "find_build_files":
		return t.findBuildFiles(path)
	case "detect_languages":
		return t.detectLanguages(path)
	case "find_tests":
		return t.findTestPatterns(path)
	default:
		return nil, fmt.Errorf("unknown action: %s", params.Action)
	}
}

// ProjectTypeResult represents the result of project type detection
type ProjectTypeResult struct {
	Type         config.ProjectType `json:"type"`
	Indicators   []string           `json:"indicators"`
	Applications []string           `json:"applications,omitempty"`
}

func (t *ProjectAnalyzerTool) detectProjectType(path string) (*ProjectTypeResult, error) {
	result := &ProjectTypeResult{
		Type:       config.ProjectTypeSingle,
		Indicators: []string{},
	}

	// Check for monorepo indicators
	monorepoIndicators := []string{
		"lerna.json",
		"rush.json",
		"pnpm-workspace.yaml",
		"nx.json",
		"turbo.json",
	}

	for _, indicator := range monorepoIndicators {
		if _, err := os.Stat(filepath.Join(path, indicator)); err == nil {
			result.Type = config.ProjectTypeMonorepo
			result.Indicators = append(result.Indicators, indicator)
		}
	}

	// Check for multiple package.json or go.mod files
	var packageFiles []string
	err := filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip hidden directories and common excluded paths
		if info.IsDir() && (strings.HasPrefix(info.Name(), ".") ||
			info.Name() == "node_modules" ||
			info.Name() == "vendor") {
			return filepath.SkipDir
		}

		relPath, _ := filepath.Rel(path, walkPath)

		if info.Name() == "package.json" || info.Name() == "go.mod" {
			dir := filepath.Dir(relPath)
			if dir != "." {
				packageFiles = append(packageFiles, dir)
			}
		}

		return nil
	})

	if err != nil {
		logging.Error(fmt.Sprintf("Error walking directory: %v", err))
	}

	if len(packageFiles) > 1 {
		result.Type = config.ProjectTypeMonorepo
		result.Applications = packageFiles
		result.Indicators = append(result.Indicators, fmt.Sprintf("%d package/module files found", len(packageFiles)))
	}

	return result, nil
}

// BuildFile represents a build configuration file
type BuildFile struct {
	Path     string `json:"path"`
	Type     string `json:"type"`
	Language string `json:"language"`
}

func (t *ProjectAnalyzerTool) findBuildFiles(path string) ([]BuildFile, error) {
	var buildFiles []BuildFile

	buildFilePatterns := map[string]struct {
		fileType string
		language string
	}{
		"package.json":      {"npm", "javascript"},
		"pom.xml":           {"maven", "java"},
		"build.gradle":      {"gradle", "java"},
		"Cargo.toml":        {"cargo", "rust"},
		"go.mod":            {"go", "go"},
		"requirements.txt":  {"pip", "python"},
		"pyproject.toml":    {"poetry", "python"},
		"Gemfile":           {"bundler", "ruby"},
		"composer.json":     {"composer", "php"},
		"Makefile":          {"make", "generic"},
		"CMakeLists.txt":    {"cmake", "c/c++"},
		"Dockerfile":        {"docker", "generic"},
		".gitlab-ci.yml":    {"gitlab-ci", "generic"},
		".github/workflows": {"github-actions", "generic"},
	}

	err := filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip hidden directories except .github
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && !strings.HasPrefix(info.Name(), ".github") {
			return filepath.SkipDir
		}

		// Skip common excluded directories
		if info.IsDir() && (info.Name() == "node_modules" || info.Name() == "vendor") {
			return filepath.SkipDir
		}

		for pattern, details := range buildFilePatterns {
			if info.Name() == pattern || strings.Contains(walkPath, pattern) {
				relPath, _ := filepath.Rel(path, walkPath)
				buildFiles = append(buildFiles, BuildFile{
					Path:     relPath,
					Type:     details.fileType,
					Language: details.language,
				})
			}
		}

		return nil
	})

	if err != nil {
		logging.Error(fmt.Sprintf("Error finding build files: %v", err))
	}

	return buildFiles, nil
}

// LanguageInfo represents detected language information
type LanguageInfo struct {
	Language   string   `json:"language"`
	Extensions []string `json:"extensions"`
	FileCount  int      `json:"file_count"`
}

func (t *ProjectAnalyzerTool) detectLanguages(path string) ([]LanguageInfo, error) {
	languageMap := make(map[string]*LanguageInfo)

	extensionToLanguage := map[string]string{
		".go":    "go",
		".js":    "javascript",
		".ts":    "typescript",
		".jsx":   "javascript",
		".tsx":   "typescript",
		".py":    "python",
		".java":  "java",
		".rs":    "rust",
		".c":     "c",
		".cpp":   "c++",
		".cs":    "c#",
		".rb":    "ruby",
		".php":   "php",
		".swift": "swift",
		".kt":    "kotlin",
		".scala": "scala",
		".r":     "r",
		".lua":   "lua",
		".sh":    "shell",
		".bash":  "bash",
	}

	err := filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		// Skip directories
		if info.IsDir() {
			// Skip hidden directories and common excluded paths
			if strings.HasPrefix(info.Name(), ".") ||
				info.Name() == "node_modules" ||
				info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		ext := strings.ToLower(filepath.Ext(info.Name()))
		if lang, ok := extensionToLanguage[ext]; ok {
			if _, exists := languageMap[lang]; !exists {
				languageMap[lang] = &LanguageInfo{
					Language:   lang,
					Extensions: []string{},
				}
			}

			// Add extension if not already present
			found := false
			for _, e := range languageMap[lang].Extensions {
				if e == ext {
					found = true
					break
				}
			}
			if !found {
				languageMap[lang].Extensions = append(languageMap[lang].Extensions, ext)
			}

			languageMap[lang].FileCount++
		}

		return nil
	})

	if err != nil {
		logging.Error(fmt.Sprintf("Error detecting languages: %v", err))
	}

	// Convert map to slice
	var languages []LanguageInfo
	for _, info := range languageMap {
		languages = append(languages, *info)
	}

	return languages, nil
}

// TestPattern represents a detected test pattern
type TestPattern struct {
	Path      string `json:"path"`
	Type      string `json:"type"`
	Framework string `json:"framework,omitempty"`
}

func (t *ProjectAnalyzerTool) findTestPatterns(path string) ([]TestPattern, error) {
	var patterns []TestPattern

	// Common test directory patterns
	testDirs := []string{
		"test", "tests", "spec", "specs", "__tests__", "test_",
	}

	// Test file patterns by language
	testFilePatterns := map[string]struct {
		suffix    string
		framework string
	}{
		"_test.go":   {"", "go test"},
		".test.js":   {"", "jest/mocha"},
		".spec.js":   {"", "jest/mocha"},
		".test.ts":   {"", "jest/mocha"},
		".spec.ts":   {"", "jest/mocha"},
		"_test.py":   {"", "pytest"},
		"test_*.py":  {"", "pytest"},
		"Test*.java": {"", "junit"},
		"*Test.java": {"", "junit"},
		"_test.rb":   {"", "rspec"},
		"_spec.rb":   {"", "rspec"},
	}

	err := filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		relPath, _ := filepath.Rel(path, walkPath)

		// Check for test directories
		if info.IsDir() {
			for _, testDir := range testDirs {
				if strings.Contains(strings.ToLower(info.Name()), testDir) {
					patterns = append(patterns, TestPattern{
						Path: relPath,
						Type: "directory",
					})
					break
				}
			}

			// Skip hidden directories and common excluded paths
			if strings.HasPrefix(info.Name(), ".") ||
				info.Name() == "node_modules" ||
				info.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		// Check for test files
		fileName := info.Name()
		for pattern, details := range testFilePatterns {
			matched := false

			if strings.Contains(pattern, "*") {
				// Handle wildcard patterns
				prefix := strings.TrimSuffix(pattern, "*"+filepath.Ext(pattern))
				suffix := strings.TrimPrefix(pattern, "*")

				if prefix != pattern && strings.HasPrefix(fileName, prefix) {
					matched = true
				} else if suffix != pattern && strings.HasSuffix(fileName, suffix) {
					matched = true
				}
			} else if strings.HasSuffix(fileName, pattern) {
				matched = true
			}

			if matched {
				patterns = append(patterns, TestPattern{
					Path:      relPath,
					Type:      "file",
					Framework: details.framework,
				})
				break
			}
		}

		return nil
	})

	if err != nil {
		logging.Error(fmt.Sprintf("Error finding test patterns: %v", err))
	}

	// Check for test configuration files
	testConfigs := map[string]string{
		"jest.config.js": "jest",
		"karma.conf.js":  "karma",
		"mocha.opts":     "mocha",
		"pytest.ini":     "pytest",
		"phpunit.xml":    "phpunit",
		".rspec":         "rspec",
	}

	for config, framework := range testConfigs {
		if _, err := os.Stat(filepath.Join(path, config)); err == nil {
			patterns = append(patterns, TestPattern{
				Path:      config,
				Type:      "config",
				Framework: framework,
			})
		}
	}

	return patterns, nil
}
