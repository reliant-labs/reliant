package cmdutil

import (
	"path/filepath"
	"strings"
)

// The FORGE_SCAFFOLD marker scanners are shared by `forge project audit`
// (internal/cli/audit) and `forge project map` (internal/cli/map.go). They live in
// the leaf package so both reach one implementation without an import cycle.

// CountLineStartScaffoldMarkers counts line-start `// FORGE_SCAFFOLD:` and
// `# FORGE_SCAFFOLD:` markers in data. (The lint analyzer is Go-only, but
// audit/map are allowed to span more file types — hence both comment
// styles.)
func CountLineStartScaffoldMarkers(data []byte) int {
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, "// FORGE_SCAFFOLD:") || strings.HasPrefix(trimmed, "# FORGE_SCAFFOLD:") {
			count++
		}
	}
	return count
}

// IsMarkerScannable identifies file types whose FORGE_SCAFFOLD markers are
// real unfilled placeholders rather than documentation references. Markdown
// and JSON commonly cite the marker syntax in prose / fixtures, so they're
// excluded — those occurrences would otherwise generate noisy "scaffold
// present" warnings on every project that documents how scaffolds work.
func IsMarkerScannable(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".go", ".proto", ".ts", ".tsx", ".js", ".jsx",
		".yaml", ".yml", ".sql", ".k", ".sh", ".tmpl", ".toml":
		return true
	}
	return false
}
