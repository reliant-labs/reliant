package workflowref

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNestedWorkflowNames(t *testing.T) {
	nested := []File{{Repo: "control-plane", Path: "merge-steward.yaml", Content: []byte(wf("merge-steward"))}, {Repo: "forge", Path: "merge-steward.yaml", Content: []byte(wf("Merge Steward"))}}
	t.Run("unique bare and qualified", func(t *testing.T) {
		ix := NewIndex(nested[:1])
		a, err := ix.Lookup("merge-steward")
		require.NoError(t, err)
		b, err := ix.Lookup("control-plane/merge-steward")
		require.NoError(t, err)
		require.Same(t, a, b)
		require.Equal(t, "control-plane/.reliant/workflows/merge-steward.yaml", a.Location())
		require.Empty(t, ix.Warnings())
	})
	t.Run("two repos require qualification", func(t *testing.T) {
		ix := NewIndex(nested)
		_, err := ix.Lookup("merge-steward")
		require.ErrorContains(t, err, "project://control-plane/merge-steward")
		require.ErrorContains(t, err, "project://forge/merge-steward")
		require.Len(t, ix.Warnings(), 2)
		for _, e := range ix.Entries() {
			require.Nil(t, e.Problem)
			require.Contains(t, e.Address, "/")
			got, err := ix.Lookup(e.Address)
			require.NoError(t, err)
			require.Same(t, e, got)
		}
		got, err := Resolve("project://control-plane/Merge Steward", Sources{Project: ix, User: func(string) ([]byte, error) { t.Fatal("qualified refs must not consult user drafts"); return nil, nil }})
		require.NoError(t, err)
		require.Equal(t, "control-plane/.reliant/workflows/merge-steward.yaml", got.Path)
	})
	t.Run("root wins", func(t *testing.T) {
		ix := NewIndex(append(nested, File{Path: "merge-steward.yaml", Content: []byte(wf("merge-steward"))}))
		root, err := ix.Lookup("merge-steward")
		require.NoError(t, err)
		require.Empty(t, root.Repo)
		for _, repo := range []string{"forge", "control-plane"} {
			e, err := ix.Lookup(repo + "/merge-steward")
			require.NoError(t, err)
			require.Equal(t, repo, e.Repo)
			require.NotNil(t, e.Warning)
		}
	})
	t.Run("duplicates within repo remain invalid", func(t *testing.T) {
		ix := NewIndex(append(nested[:1], File{Repo: "control-plane", Path: "other.yaml", Content: []byte(wf("merge-steward"))}))
		_, err := ix.Lookup("control-plane/merge-steward")
		require.ErrorContains(t, err, "also declared")
	})
}

func TestMergedLayoutScenariosStayWithTheirRepo(t *testing.T) {
	root := NewLayout(map[string][]byte{"flow.yaml": []byte(wf("flow")), "flow/scenarios/root.yaml": []byte("name: root")})
	nested := NewLayout(map[string][]byte{"flow.yaml": []byte(wf("flow")), "flow/scenarios/nested.yaml": []byte("name: nested")})
	layout := MergeLayouts(root, map[string]*Layout{"control-plane": nested})
	require.Empty(t, layout.ScenarioProblems())
	require.Equal(t, "root", layout.ScenariosFor("flow")[0].Name)
	require.Equal(t, "nested", layout.ScenariosFor("control-plane/flow")[0].Name)
	require.Equal(t, "control-plane", layout.ScenariosFor("control-plane/flow")[0].Repo)
}

func TestDistinctRepoNamesDoNotCollapse(t *testing.T) {
	ix := NewIndex([]File{{Repo: "a_b", Path: "x.yaml", Content: []byte(wf("flow"))}, {Repo: "a-b", Path: "x.yaml", Content: []byte(wf("flow"))}})
	_, err := ix.Lookup("flow")
	require.Error(t, err)
	a, err := ix.Lookup("a_b/flow")
	require.NoError(t, err)
	b, err := ix.Lookup("a-b/flow")
	require.NoError(t, err)
	require.NotSame(t, a, b)
}

func TestQualifiedRefKeyAndPaths(t *testing.T) {
	require.Equal(t, "project://control-plane/merge-steward", RefKey("project://control-plane/Merge Steward"))
	require.NotEqual(t, RefKey("control-plane/merge-steward"), RefKey("control-planemerge-steward"))
	for _, repo := range []string{"", "control-plane", "libs/forge"} {
		p := RelativePath(repo, "x.yaml")
		r, f, ok := SplitRelativePath(p)
		require.True(t, ok)
		require.Equal(t, repo, r)
		require.Equal(t, "x.yaml", f)
	}
}

// A broken file fails closed: the ref it would answer reports the parse
// error. It used to claim no name at all, so the same bare name resolved to
// another directory's copy (the root's pin-bot.yaml beside control-plane's):
// an older version running because the newest one is broken.
func TestBrokenWorkflowFailsClosed(t *testing.T) {
	nested := File{Repo: "control-plane", Path: "release.yaml", Content: []byte(wf("release"))}
	broken := []byte("name: release\nentry: [x\nnodes:\n")
	t.Run("a broken root file blocks the nested copy's bare name", func(t *testing.T) {
		ix := NewIndex([]File{nested, {Path: "release.yaml", Content: broken}})
		_, err := ix.Lookup("release")
		require.ErrorContains(t, err, "release.yaml does not parse, so workflow \"release\" cannot run")
		e, err := ix.Lookup("control-plane/release")
		require.NoError(t, err, "the nested copy is still addressable by its qualified name")
		require.Equal(t, "control-plane", e.Repo)
	})
	t.Run("a broken file shadows a same-named file in its own directory", func(t *testing.T) {
		ix := NewIndex([]File{nested, {Repo: "control-plane", Path: "release-v2.yaml", Content: broken}})
		_, err := ix.Lookup("release")
		require.Error(t, err)
	})
	t.Run("with no readable name it claims its file name", func(t *testing.T) {
		ix := NewIndex([]File{nested, {Path: "release.yaml", Content: []byte(":\n  - [")}})
		_, err := ix.Lookup("release")
		require.ErrorContains(t, err, "does not parse")
	})
	t.Run("a quoted, commented name is recovered", func(t *testing.T) {
		require.Equal(t, "Release Train", recoverDeclaredName([]byte("x: [\nname: \"Release Train\" # the train\n")))
	})
}
