// Copyright (c) 2025 Reliant Labs
package workflowref

import (
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wf(name string) string {
	return "name: " + name + "\nentry: [x]\nnodes:\n  - id: x\n    type: save_message\n    args: {role: assistant, content: hi}\n"
}

func TestParse(t *testing.T) {
	for raw, want := range map[string]Ref{
		"builtin://agent":         {Raw: "builtin://agent", Kind: Builtin, Name: "agent"},
		"project://deploy":        {Raw: "project://deploy", Kind: Project, Name: "deploy"},
		"  project://My Flow  ":   {Raw: "project://My Flow", Kind: Project, Name: "My Flow"},
		"deploy":                  {Raw: "deploy", Kind: Project, Name: "deploy"},
		"project://blog-pipeline": {Raw: "project://blog-pipeline", Kind: Project, Name: "blog-pipeline"},
	} {
		got, err := Parse(raw)
		require.NoError(t, err, raw)
		assert.Equal(t, want, got, raw)
	}
	for _, raw := range []string{"", "  ", "builtin://", "project://", "project://!!!", "workflow://x", "https://x", "{{inputs.flow}}"} {
		_, err := Parse(raw)
		assert.Error(t, err, "%q must not parse", raw)
	}
}

func TestKey_SpellingsOfOneWorkflowAgree(t *testing.T) {
	assert.Equal(t, RefKey("project://Blog Pipeline"), RefKey("blog_pipeline"))
	assert.Equal(t, "project://blog-pipeline", RefKey("blog-pipeline"))
	assert.NotEqual(t, RefKey("builtin://agent"), RefKey("project://agent"),
		"a builtin and a project workflow of the same name are different workflows")
}

func TestProjectSlug(t *testing.T) {
	assert.Equal(t, "my-flow", ProjectSlug("project://My Flow"))
	assert.Equal(t, "my-flow", ProjectSlug("my_flow"))
	assert.Equal(t, "", ProjectSlug("builtin://agent"))
	assert.Equal(t, "", ProjectSlug("{{inputs.flow}}"))
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"blog-content-pipeline":  "blog-content-pipeline",
		" Blog Content_Pipeline": "blog-content-pipeline",
		"a!!b":                   "ab",
		"--a  b--":               "a-b",
		"!!!":                    "",
	} {
		assert.Equal(t, want, Slug(in), in)
	}
}

func TestIndex_AddressesByNameNeverFileName(t *testing.T) {
	ix := NewIndex([]File{{Path: "blog.yaml", Content: []byte(wf("blog-content-pipeline"))}})

	e, err := ix.Lookup("blog-content-pipeline")
	require.NoError(t, err)
	assert.Equal(t, "blog.yaml", e.Path)

	_, err = ix.Lookup("blog")
	require.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), `blog.yaml is named "blog-content-pipeline"`)
	assert.Contains(t, err.Error(), "use project://blog-content-pipeline")
}

func TestIndex_MissingNameIsAnError(t *testing.T) {
	ix := NewIndex([]File{{Path: "deploy.yaml", Content: []byte("entry: [x]\nnodes: []\n")}})
	require.Error(t, ix.EntryAt("deploy.yaml").Problem)
	_, err := ix.Lookup("deploy")
	require.ErrorIs(t, err, ErrNotFound, "a nameless file is not addressed by its file name")
	assert.Contains(t, err.Error(), "deploy.yaml has no name: field")
}

func TestIndex_DuplicateNameIsAnErrorNotAGuess(t *testing.T) {
	ix := NewIndex([]File{
		{Path: "a.yaml", Content: []byte(wf("deploy"))},
		{Path: "b.yaml", Content: []byte(wf("Deploy"))},
	})
	_, err := ix.Lookup("deploy")
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrNotFound), "an ambiguous name is not a miss")
	assert.Contains(t, err.Error(), "also declared by b.yaml")
	for _, e := range ix.Entries() {
		assert.Error(t, e.Problem, "%s claims a shared name", e.Path)
	}
}

func TestIndex_NameThatIsAnotherFilesFileNameIsAnError(t *testing.T) {
	ix := NewIndex([]File{
		{Path: "a.yaml", Content: []byte(wf("b"))},
		{Path: "b.yaml", Content: []byte(wf("c"))},
	})
	_, err := ix.Lookup("b")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `a.yaml is named "b", but b.yaml is a different workflow (it is named "c")`)

	e, err := ix.Lookup("c")
	require.NoError(t, err, "b.yaml stays addressable by its own name")
	assert.Equal(t, "b.yaml", e.Path)
}

func TestResolve_Order(t *testing.T) {
	project := NewIndex([]File{{Path: "flow.yaml", Content: []byte(wf("flow"))}})

	t.Run("builtin:// is the embedded builtin only", func(t *testing.T) {
		r, err := Resolve("builtin://agent", Sources{Project: project})
		require.NoError(t, err)
		assert.Equal(t, SourceBuiltin, r.Source)
		assert.Equal(t, "agent", r.Workflow.GetName())

		_, err = Resolve("builtin://flow", Sources{Project: project})
		require.ErrorIs(t, err, ErrNotFound, "builtin:// never falls back to the project")
	})

	t.Run("a bare name is a project ref, not a builtin", func(t *testing.T) {
		_, err := Resolve("agent", Sources{Project: project})
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("the caller's own workflow shadows the project's", func(t *testing.T) {
		user := func(slug string) ([]byte, error) {
			if slug == "flow" {
				return []byte(wf("flow")), nil
			}
			return nil, nil
		}
		r, err := Resolve("project://Flow", Sources{User: user, Project: project})
		require.NoError(t, err)
		assert.Equal(t, SourceUser, r.Source)
	})

	t.Run("a workflow of the caller's that cannot run shadows the project's", func(t *testing.T) {
		notRunnable := errors.New("workflow draft is not complete")
		user := func(string) ([]byte, error) { return nil, notRunnable }
		_, err := Resolve("project://flow", Sources{User: user, Project: project})
		require.ErrorIs(t, err, notRunnable, "never silently run a different definition than the one they named")
	})

	t.Run("the project answers when the caller has no such workflow", func(t *testing.T) {
		none := func(string) ([]byte, error) { return nil, nil }
		r, err := Resolve("project://flow", Sources{User: none, Project: project})
		require.NoError(t, err)
		assert.Equal(t, SourceProject, r.Source)
		assert.Equal(t, "flow.yaml", r.Path)
	})

	t.Run("no project", func(t *testing.T) {
		_, err := Resolve("project://flow", Sources{})
		require.ErrorIs(t, err, ErrNotFound)
	})
}

func TestReadLayout(t *testing.T) {
	fsys := fstest.MapFS{
		"blog.yaml":                                  {Data: []byte(wf("blog-content-pipeline"))},
		"content-next.yaml":                          {Data: []byte(wf("content-next"))},
		"content-next/scenarios/happy.yaml":          {Data: []byte("name: happy\n")},
		"blog-content-pipeline/scenarios/drafts.yml": {Data: []byte("name: drafts\n")},
		"blog-scenarios.md":                          {Data: []byte("# notes")},
		"docs/other.yaml":                            {Data: []byte("not ours")},
	}
	l, err := ReadLayout(fsys)
	require.NoError(t, err)

	var paths []string
	for _, e := range l.Workflows.Entries() {
		paths = append(paths, e.Path)
	}
	assert.Equal(t, []string{"blog.yaml", "content-next.yaml"}, paths, "workflows are the top-level YAML files")
	assert.Equal(t, []ScenarioFile{
		{WorkflowSlug: "blog-content-pipeline", Name: "drafts", Path: "blog-content-pipeline/scenarios/drafts.yml", Content: []byte("name: drafts\n")},
		{WorkflowSlug: "content-next", Name: "happy", Path: "content-next/scenarios/happy.yaml", Content: []byte("name: happy\n")},
	}, l.Scenarios)
	assert.Empty(t, l.ScenarioProblems())
}

func TestReadLayout_MissingDirHoldsNothing(t *testing.T) {
	l, err := ReadLayout(fstest.MapFS{})
	require.NoError(t, err)
	assert.Empty(t, l.Workflows.Entries())
}

func TestReadLayout_RetiredLayoutsNameTheNewLocation(t *testing.T) {
	l := NewLayout(map[string][]byte{
		"blog.yaml":                                   []byte(wf("blog-content-pipeline")),
		"scenarios/blog/drafts.yaml":                  []byte("name: drafts\n"),
		"blog_scenarios.yaml":                         []byte("name: more\n"),
		"blog-content-pipeline/scenarios/deep/x.yaml": []byte("name: x\n"),
	})
	assert.Empty(t, l.Scenarios)
	require.Len(t, l.Workflows.Entries(), 1, "a retired co-located scenario file is not a workflow")

	var msgs []string
	for _, err := range l.ScenarioProblems() {
		msgs = append(msgs, err.Error())
	}
	joined := strings.Join(msgs, "\n")
	assert.Contains(t, joined, "scenarios/blog/drafts.yaml uses the retired scenario layout; move it to blog-content-pipeline/scenarios/drafts.yaml")
	assert.Contains(t, joined, "blog_scenarios.yaml uses the retired scenario layout; move its scenarios to blog-content-pipeline/scenarios/")
	assert.Contains(t, joined, "move it to blog-content-pipeline/scenarios/x.yaml")
}

func TestLayout_ScenariosOfNoWorkflowAreReported(t *testing.T) {
	l := NewLayout(map[string][]byte{
		"blog.yaml":                  []byte(wf("blog-content-pipeline")),
		"blog/scenarios/drafts.yaml": []byte("name: drafts\n"),
	})
	problems := l.ScenarioProblems()
	require.Len(t, problems, 1)
	assert.Contains(t, problems[0].Error(), "blog/scenarios/: these scenarios belong to no workflow")
	assert.Contains(t, problems[0].Error(), `blog.yaml is named "blog-content-pipeline"`)
}
