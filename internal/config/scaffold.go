package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/reliant-labs/forge/internal/pathglob"
)

// ScaffoldConfig is the `scaffold:` block: which parts of the repository
// forge must never write.
//
//	scaffold:
//	  exclude:
//	    - frontends/web/**          # the team's own frontend
//	    - .github/workflows/deploy.yml
//
// It is the brownfield twin of `forge project disown`: disown hands ONE file
// to the project after forge wrote it; exclude says, once, for a whole area,
// that forge never writes there at all — not in `forge generate`, a
// `forge scaffold`, `forge project upgrade` or `forge project rescaffold`.
// An excluded path behaves exactly like a disowned or declined one: a file
// already there is the project's, and an absent one stays absent.
//
// Generated files other generated code depends on (gen/, the ORM, the
// composition root's *_gen.go) cannot be excluded on their own: the run fails
// naming the dependent and the feature switch that turns the pair off.
type ScaffoldConfig struct {
	// Exclude lists project-relative globs (internal/pathglob syntax:
	// anchored at the project root, `**` for any depth, a trailing `/` for
	// a whole directory).
	Exclude []string `yaml:"exclude,omitempty"`
}

// validateScaffoldExclude rejects a scaffold.exclude entry that could never
// match a project path, so a typo is a load error rather than an exclusion
// that silently excludes nothing.
func validateScaffoldExclude(cfg *ProjectConfig, root *yaml.Node) []validationIssue {
	var out []validationIssue
	for i, g := range cfg.Scaffold.Exclude {
		if dir, ok := excludeEntersUnguardedDir(g); ok && pathglob.Validate(g) == nil {
			line, col := findNodePos(root, []string{"scaffold", "exclude", fmt.Sprintf("[%d]", i)})
			if line == 0 {
				line, col = findNodePos(root, []string{"scaffold", "exclude"})
			}
			out = append(out, validationIssue{line: line, column: col,
				msg: fmt.Sprintf("scaffold.exclude[%d] %q names a path inside %s/, which scaffold.exclude cannot guard: "+
					"forge never scaffolds there, and the run's guard does not capture it (git's, forge's own state, "+
					"or a dependency or build cache)", i, g, dir),
				fix: "drop the entry; exclude the source tree around it instead (e.g. `frontends/web/**`, which leaves its node_modules alone)."})
			continue
		}
		if err := pathglob.Validate(g); err != nil {
			line, col := findNodePos(root, []string{"scaffold", "exclude", fmt.Sprintf("[%d]", i)})
			if line == 0 {
				line, col = findNodePos(root, []string{"scaffold", "exclude"})
			}
			out = append(out, validationIssue{line: line, column: col,
				msg: fmt.Sprintf("scaffold.exclude[%d] %q is not a usable path glob: %v", i, g, err),
				fix: "write a project-relative glob such as `frontends/web/**`, `.github/workflows/deploy.yml` " +
					"or `docs/` (a trailing / is the whole directory)."})
		}
	}
	return out
}

// ExcludeUnguardedDir reports whether a directory of this name is outside
// scaffold.exclude's guarantee: git's own, forge's own state, or a dependency
// or build cache. The run's guard never descends one (it captures and puts
// back nothing there, either way), so an exclude entry that names a path
// inside one is a load error rather than a promise the guard cannot keep.
func ExcludeUnguardedDir(name string) bool {
	switch name {
	case ".git", ".forge", "node_modules", ".next":
		return true
	}
	return false
}

// excludeEntersUnguardedDir is the unguarded directory a glob names as one
// of its segments (`.next/`, `web/node_modules/**`, `**/.forge/x`), if any.
func excludeEntersUnguardedDir(glob string) (string, bool) {
	for _, seg := range strings.Split(pathglob.Normalize(glob), "/") {
		if ExcludeUnguardedDir(seg) {
			return seg, true
		}
	}
	return "", false
}

// ReadGenerateHookWrites returns the `writes:` every generate hook declares:
// the paths the project's own commands produce during a forge run, which the
// scaffold.exclude guard therefore never treats as forge's writes.
func ReadGenerateHookWrites(projectDir string) []string {
	data, err := os.ReadFile(filepath.Join(projectDir, "forge.yaml"))
	if err != nil {
		return nil
	}
	var doc struct {
		Generate GenerateConfig `yaml:"generate"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []string
	for _, h := range doc.Generate.Hooks {
		for _, w := range h.Writes { // project-relative, whatever the hook's dir
			out = append(out, filepath.ToSlash(filepath.Clean(w)))
		}
	}
	return out
}

// ReadScaffoldExclude returns forge.yaml's scaffold.exclude for the project
// at projectDir, without the rest of the load: it is consulted at every write
// chokepoint, including ones that run before (or without) a full config load.
// A project with no forge.yaml, or none that parses, excludes nothing — the
// full load reports the parse error.
func ReadScaffoldExclude(projectDir string) []string {
	data, err := os.ReadFile(filepath.Join(projectDir, "forge.yaml"))
	if err != nil {
		return nil
	}
	var doc struct {
		Scaffold ScaffoldConfig `yaml:"scaffold"`
	}
	if yaml.Unmarshal(data, &doc) != nil {
		return nil
	}
	var out []string
	for _, g := range doc.Scaffold.Exclude {
		if pathglob.Validate(g) == nil {
			out = append(out, pathglob.Normalize(g))
		}
	}
	return out
}
