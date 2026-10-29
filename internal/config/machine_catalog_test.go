package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wf(rel, name, content string) StoredWorkflow {
	return StoredWorkflow{Name: name, RelativePath: rel, YAMLContent: content}
}

// The incident: machine default provides control-plane's release workflow, a
// new machine cicd without that checkout pushes a snapshot with none. The
// union must keep default's file; it leaves only when no machine carries it.
func TestUnionWorkflows_AMachineWithoutTheFileDoesNotEraseIt(t *testing.T) {
	t0 := time.Unix(1000, 0)
	release := wf("control-plane/.reliant/workflows/release.yaml", "release", "v1")
	def := MachineCatalog{DaemonID: "default", PushedAt: t0, Workflows: []StoredWorkflow{release}}
	cicd := MachineCatalog{DaemonID: "cicd", PushedAt: t0.Add(time.Hour)}

	union := UnionWorkflows([]MachineCatalog{def, cicd})
	require.Len(t, union, 1)
	assert.Equal(t, release, union[0])

	assert.Empty(t, UnionWorkflows([]MachineCatalog{{DaemonID: "default", PushedAt: t0}, cicd}),
		"a file no machine carries leaves the union")
}

// Two machines carrying the same path: the newest push wins, per file.
func TestUnionWorkflows_NewestPushWinsPerFile(t *testing.T) {
	t0 := time.Unix(1000, 0)
	a := MachineCatalog{DaemonID: "a", PushedAt: t0, Workflows: []StoredWorkflow{
		wf(".reliant/workflows/x.yaml", "x", "old"), wf(".reliant/workflows/only-a.yaml", "only-a", "a"),
	}}
	b := MachineCatalog{DaemonID: "b", PushedAt: t0.Add(time.Minute), Workflows: []StoredWorkflow{
		wf(".reliant/workflows/x.yaml", "x", "new"),
	}}
	union := UnionWorkflows([]MachineCatalog{a, b})
	require.Len(t, union, 2)
	assert.Equal(t, ".reliant/workflows/only-a.yaml", union[0].RelativePath)
	assert.Equal(t, "new", union[1].YAMLContent)
	assert.Equal(t, union, UnionWorkflows([]MachineCatalog{b, a}), "order of catalogs does not matter")
}

func TestUnionPresets_KeyedByPathSoNestedNamesDoNotCollide(t *testing.T) {
	c := MachineCatalog{DaemonID: "a", Presets: []StoredPreset{
		{Name: "implementer", RelativePath: ".reliant/presets/implementer.yaml"},
		{Name: "implementer", RelativePath: "control-plane/.reliant/presets/implementer.yaml"},
	}}
	assert.Len(t, UnionPresets([]MachineCatalog{c}), 2)
}

// A file a machine drops is remembered with its path; carrying it again
// forgets the removal.
func TestNextRemovedCatalogFiles(t *testing.T) {
	now := time.Unix(5000, 0).UTC()
	release := wf("control-plane/.reliant/workflows/release.yaml", "release", "v1")
	prev := MachineCatalog{Workflows: []StoredWorkflow{release}, Presets: []StoredPreset{{Name: "p", RelativePath: ".reliant/presets/p.yaml"}}}

	removed := NextRemovedCatalogFiles(prev, MachineCatalog{}, nil, now)
	require.Len(t, removed, 2)
	assert.Equal(t, RemovedCatalogFile{Kind: CatalogKindWorkflow, Name: "release", RelativePath: release.RelativePath, RemovedAt: now}, removed[0])
	assert.Equal(t, CatalogKindPreset, removed[1].Kind)

	again := NextRemovedCatalogFiles(MachineCatalog{}, prev, removed, now.Add(time.Hour))
	assert.Empty(t, again, "a file carried again is no longer removed")

	assert.Len(t, NextRemovedCatalogFiles(MachineCatalog{}, MachineCatalog{}, removed, now), 2, "history survives unrelated pushes")
}

func TestFindStoredPresetByName_NestedRepos(t *testing.T) {
	root := StoredPreset{Name: "implementer", RelativePath: ".reliant/presets/implementer.yaml", YAMLContent: "root"}
	cp := StoredPreset{Name: "implementer", RelativePath: "control-plane/.reliant/presets/implementer.yaml", YAMLContent: "cp"}
	forge := StoredPreset{Name: "implementer", RelativePath: "forge/.reliant/presets/implementer.yaml", YAMLContent: "forge"}
	reviewer := StoredPreset{Name: "code_reviewer", RelativePath: "control-plane/.reliant/presets/code_reviewer.yaml", YAMLContent: "rev"}
	legacy := StoredPreset{Name: "legacy", YAMLContent: "legacy"}

	all := []StoredPreset{forge, cp, root, reviewer, legacy}
	assert.Equal(t, "root", FindStoredPresetByName(all, "implementer").YAMLContent, "the root shadows nested repos")
	assert.Equal(t, "cp", FindStoredPresetByName(all, "control-plane/implementer").YAMLContent)
	assert.Equal(t, "forge", FindStoredPresetByName(all, "forge/implementer").YAMLContent)
	assert.Equal(t, "rev", FindStoredPresetByName(all, "code_reviewer").YAMLContent, "a nested-only preset resolves by its bare name")
	assert.Equal(t, "cp", FindStoredPresetByName([]StoredPreset{forge, cp}, "implementer").YAMLContent, "without a root copy, repository path order")
	assert.Equal(t, "legacy", FindStoredPresetByName(all, "legacy").YAMLContent, "a preset from a daemon without paths is a root preset")
	assert.Nil(t, FindStoredPresetByName(all, "missing"))
	assert.Equal(t, "control-plane/implementer", cp.QualifiedName())
	assert.Equal(t, "implementer", root.QualifiedName())
}
