package gitsource

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// OverridesDirName / OverridesFileName locate the machine-local source
// override file, `.forge/source-overrides.yaml`. It sits under `.forge/`
// with forge's other machine-local state, and it is never committed —
// which is the property that makes it safe. An override that could travel
// with a change would silently un-pin CI, defeating the entire point of a
// pinned source.
const (
	OverridesDirName  = ".forge"
	OverridesFileName = "source-overrides.yaml"
)

// overridesDoc is the file's shape:
//
//	# .forge/source-overrides.yaml — machine-local, never committed.
//	sources:
//	  github.com/reliant-labs/reliant: ../reliant
//
// A map keyed by repo, valued by a directory (relative to the project
// root, or absolute). Keyed by repo rather than by component name so one
// entry covers every component sourced from that repository — a project
// consuming two directories of one repo overrides it once.
type overridesDoc struct {
	Sources map[string]string `yaml:"sources"`
}

// LoadOverrides reads <projectDir>/.forge/source-overrides.yaml. A
// missing file yields an empty map and no error: having no overrides is
// the normal state, and the pinned path is what should work by default.
//
// A malformed file IS an error. The failure mode it prevents is the worst
// one available here — a typo'd override silently ignored, so the build
// quietly uses the pin while the developer believes they are testing
// their working copy.
func LoadOverrides(projectDir string) (map[string]string, error) {
	path := filepath.Join(projectDir, OverridesDirName, OverridesFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var doc overridesDoc
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make(map[string]string, len(doc.Sources))
	for repo, dir := range doc.Sources {
		if dir == "" {
			return nil, fmt.Errorf("%s: source override for %q has an empty directory", path, repo)
		}
		out[normalizeRepo(repo)] = dir
	}
	return out, nil
}

// WriteOverrides writes the override file, creating `.forge/` as needed.
// Provided so a future `forge source override` subcommand — and the
// tests — have one implementation of the file's shape rather than two.
func WriteOverrides(projectDir string, sources map[string]string) error {
	dir := filepath.Join(projectDir, OverridesDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := yaml.Marshal(overridesDoc{Sources: sources})
	if err != nil {
		return fmt.Errorf("encode source overrides: %w", err)
	}
	header := "# Machine-local source overrides. NOT committed — an override that\n" +
		"# traveled with a change would un-pin CI. Maps a repo declared as a\n" +
		"# forge.GitSource to a local working copy to build instead.\n"
	path := filepath.Join(dir, OverridesFileName)
	if err := atomicfile.WriteFile(path, append([]byte(header), data...), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
