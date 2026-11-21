package generator

import (
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/templates"
)

// `forge project upgrade` lists a drift row only when its template changed
// across the upgrade's range, which it learns by comparing the template file
// the row NAMES with the pinned forge's copy. A row naming the wrong file, or
// a file this forge does not embed, would hide real drift or show stale
// drift, so every row a render reaches names an embedded template file. The
// one row with no template file (pre-commit.yml, a Go constant) names none
// and is listed whatever the range.

func assertEmbeddedSource(t *testing.T, path, src string) {
	t.Helper()
	if src == "" {
		t.Errorf("%s names no template file", path)
		return
	}
	if _, err := templates.Source(src); err != nil {
		t.Errorf("%s names template %q, which this forge does not embed: %v", path, src, err)
	}
}

func TestManagedFilesNameTheirTemplateSource(t *testing.T) {
	_, withFrontend := advisoryTestProject(t)
	for _, cfg := range []*config.ProjectConfig{
		{Name: "demo", ModulePath: "github.com/example/demo"},
		withFrontend,
	} {
		files := filterManagedFiles(managedFilesForCfg(cfg), cfg)
		if len(files) == 0 {
			t.Fatal("expected managed files")
		}
		for _, f := range files {
			assertEmbeddedSource(t, f.destPath, f.templateSource())
		}
	}
}

func TestAdvisoryRowsNameTheirTemplateSource(t *testing.T) {
	dir, cfg := advisoryTestProject(t)
	for _, row := range advisoryRows(t, dir, cfg) {
		if filepath.ToSlash(row.Path) == preCommitWorkflowPath {
			continue
		}
		assertEmbeddedSource(t, row.Path, row.Template)
	}
	// The results carry the row's template, so the report can compare it.
	for path, r := range inspect(t, dir, cfg, ForceNone(), true) {
		if filepath.ToSlash(path) != preCommitWorkflowPath && r.Template == "" {
			t.Errorf("result %s lost its template", path)
		}
	}
	for _, f := range CIWorkflowsFor(dir, cfg, CIInputs{}) {
		assertEmbeddedSource(t, f.Dest, f.SourcePath())
	}
}
