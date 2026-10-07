// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// Dir is where a project keeps its workflows, relative to the project root.
const Dir = ".reliant/workflows"

const (
	scenariosDir = "scenarios"
	// legacyScenarioSuffix marks the retired co-located scenario file
	// (<file>_scenarios.yaml beside <file>.yaml).
	legacyScenarioSuffix = "_scenarios"
)

// ScenarioDir is where the scenarios of the workflow with this slug live,
// relative to Dir.
func ScenarioDir(slug string) string {
	return path.Join(slug, scenariosDir)
}

// ScenarioPath is the file a scenario called name is kept in, relative to Dir.
func ScenarioPath(slug, name string) string {
	return path.Join(ScenarioDir(slug), name+".yaml")
}

// ScenarioFile is one scenario file a project holds.
type ScenarioFile struct {
	// WorkflowSlug is the directory it sits under: the slug of the workflow
	// it tests.
	WorkflowSlug string
	// Name is the file's stem. It identifies the scenario, and is its name
	// when the file declares none.
	Name string
	// Path is slash-separated and relative to Dir.
	Path    string
	Content []byte
}

// Layout is what a project's workflows directory holds.
type Layout struct {
	Workflows *Index
	Scenarios []ScenarioFile
	// Misplaced are scenario files in a place nothing reads — the retired
	// CLI layouts, or a scenarios directory nested too deep — each an error
	// that names where the file belongs.
	Misplaced []error
}

// IsYAML reports whether a path names a YAML file.
func IsYAML(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".yaml", ".yml":
		return true
	}
	return false
}

// ReadLayout reads a workflows directory (Dir, or wherever --dir points). A
// directory that does not exist holds nothing.
func ReadLayout(fsys fs.FS) (*Layout, error) {
	var paths []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == "." && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if !d.IsDir() && IsYAML(p) {
			paths = append(paths, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	contents := make(map[string][]byte, len(paths))
	for _, p := range paths {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil, err
		}
		contents[p] = data
	}
	return NewLayout(contents), nil
}

// NewLayout lays out files already read, keyed by their slash-separated path
// relative to Dir. ReadLayout is this over a directory.
func NewLayout(files map[string][]byte) *Layout {
	paths := make([]string, 0, len(files))
	for p := range files {
		if IsYAML(p) {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	var workflows []File
	var scenarios []ScenarioFile
	var legacy []string
	var nested []string
	for _, p := range paths {
		parts := strings.Split(p, "/")
		switch {
		case len(parts) == 1 && strings.HasSuffix(stem(p), legacyScenarioSuffix):
			legacy = append(legacy, p)
		case len(parts) == 1:
			workflows = append(workflows, File{Path: p, Content: files[p]})
		case len(parts) == 3 && parts[1] == scenariosDir:
			scenarios = append(scenarios, ScenarioFile{
				WorkflowSlug: parts[0],
				Name:         stem(p),
				Path:         p,
				Content:      files[p],
			})
		case len(parts) > 3 && parts[1] == scenariosDir:
			nested = append(nested, p)
		case len(parts) >= 3 && parts[0] == scenariosDir:
			legacy = append(legacy, p)
		}
	}

	l := &Layout{Workflows: NewIndex(workflows), Scenarios: scenarios}
	for _, p := range legacy {
		l.Misplaced = append(l.Misplaced, l.legacyError(p))
	}
	for _, p := range nested {
		parts := strings.Split(p, "/")
		l.Misplaced = append(l.Misplaced, fmt.Errorf("%s is nested inside %s/; scenario files sit directly in it (move it to %s)",
			p, ScenarioDir(parts[0]), ScenarioPath(parts[0], stem(p))))
	}
	return l
}

// legacyError names where a file in a retired layout belongs. Both retired
// layouts keyed scenarios by the workflow's FILE name; the layout keys them
// by its name:, so the move is not always a rename of the same word.
func (l *Layout) legacyError(p string) error {
	parts := strings.Split(p, "/")
	if len(parts) == 1 {
		fileStem := strings.TrimSuffix(stem(p), legacyScenarioSuffix)
		return fmt.Errorf("%s uses the retired scenario layout; move its scenarios to %s/, one file per scenario",
			p, ScenarioDir(l.slugForFileStem(fileStem)))
	}
	// scenarios/<file stem>/<rest>
	slug := l.slugForFileStem(parts[1])
	return fmt.Errorf("%s uses the retired scenario layout; move it to %s (scenarios live in <slug>/scenarios/, where <slug> is the workflow's name:)",
		p, ScenarioPath(slug, stem(p)))
}

// slugForFileStem is the slug of the workflow in the file with this stem,
// falling back to the stem itself when no such file is named.
func (l *Layout) slugForFileStem(fileStem string) string {
	for _, e := range l.Workflows.Entries() {
		if e.Stem() == fileStem && e.Slug != "" {
			return e.Slug
		}
	}
	return Slug(fileStem)
}

// ScenarioProblems is everything wrong with where the project keeps its
// scenarios: Misplaced files, and scenario directories that belong to no
// workflow (a directory named for a file rather than a name:, a typo, a
// workflow that was renamed).
func (l *Layout) ScenarioProblems() []error {
	problems := append([]error(nil), l.Misplaced...)
	reported := map[string]bool{}
	for _, s := range l.Scenarios {
		if reported[s.WorkflowSlug] {
			continue
		}
		if _, err := l.Workflows.Lookup(s.WorkflowSlug); err != nil {
			reported[s.WorkflowSlug] = true
			problems = append(problems, fmt.Errorf("%s/: these scenarios belong to no workflow: %w", ScenarioDir(s.WorkflowSlug), err))
		}
	}
	return problems
}

// ScenariosFor returns the scenario files of the workflow with this slug.
func (l *Layout) ScenariosFor(slug string) []ScenarioFile {
	var out []ScenarioFile
	for _, s := range l.Scenarios {
		if s.WorkflowSlug == slug {
			out = append(out, s)
		}
	}
	return out
}

func stem(p string) string {
	base := path.Base(p)
	return strings.TrimSuffix(base, path.Ext(base))
}
