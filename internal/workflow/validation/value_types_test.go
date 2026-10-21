// Copyright (c) 2025 Reliant Labs
package validation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/reliant-labs/reliant/internal/workflow/validation/valuetypestest"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

func typedWorkflow(t *testing.T, inputs, extra string) *reliantv1.Workflow {
	t.Helper()
	entry := "[a]"
	if strings.Contains(extra, "id: b") {
		entry = "[a, b]"
	}
	wf, err := wfyaml.ParseWorkflow([]byte("name: typed\ninputs:\n" + inputs + "entry: " + entry + "\nnodes:\n  - id: a\n    type: approval\n    args: {title: ok}\n" + extra))
	require.NoError(t, err)
	return wf
}

func staticErrors(wf *reliantv1.Workflow) []string {
	res := StaticAnalysisWithOptions(wf, &ValidationOptions{ValueTypes: valuetypestest.GitHub()})
	var out []string
	for _, e := range res.Errors() {
		out = append(out, e.Error())
	}
	return out
}

// Declaration errors, as `reliant workflow validate` words them.
func TestIntegrationInputDeclarationErrors(t *testing.T) {
	tests := []struct {
		name   string
		inputs string
		want   []string // every fragment must appear in one error
	}{
		{
			name:   "a misspelt type suggests the declared one",
			inputs: "  repos: {type: github.repo, multi: true}\n",
			want:   []string{"inputs.repos.type", `github has no input type "repo"`, `did you mean "github.repository"?`, "github declares: label, repository, user"},
		},
		{
			name:   "an integration with no types is named",
			inputs: "  repos: {type: gitlab.repository}\n",
			want:   []string{"inputs.repos.type", `no integration named "gitlab" declares input types`, "integrations with input types: github"},
		},
		{
			name:   "a misspelt integration suggests the near one",
			inputs: "  repos: {type: githb.repository}\n",
			want:   []string{`no integration named "githb"`, `did you mean "github"?`},
		},
		{
			name:   "required together with a default",
			inputs: "  repos: {type: github.repository, multi: true, required: true, default: []}\n",
			want:   []string{"inputs.repos.required", "a required input takes no default", "remove the default, or remove required: true"},
		},
		{
			name:   "required with a default on a built-in type",
			inputs: "  n: {type: integer, required: true, default: 3}\n",
			want:   []string{"inputs.n.required", "a required input takes no default"},
		},
		{
			name:   "min_items on a single value",
			inputs: "  repo: {type: github.repository, min_items: 1}\n",
			want:   []string{"inputs.repo.min_items", "counts the values of a list", "set multi: true"},
		},
		{
			name:   "min greater than max",
			inputs: "  repos: {type: github.repository, multi: true, min_items: 5, max_items: 2}\n",
			want:   []string{"min_items (5) is greater than max_items (2)"},
		},
		{
			name:   "a default that is not a valid value",
			inputs: "  repos: {type: github.repository, multi: true, default: [not-a-repo]}\n",
			want:   []string{"inputs.repos.default", "item 0 is not a valid github.repository"},
		},
		{
			name:   "a default of the wrong shape",
			inputs: "  repos: {type: github.repository, multi: true, default: acme/web}\n",
			want:   []string{"inputs.repos.default", "expects an array of github.repository values"},
		},
		{
			name:   "a scope key the type does not declare",
			inputs: "  repos: {type: github.repository, multi: true}\n  l: {type: github.label, scope: {repo: repos}}\n",
			want:   []string{"inputs.l.scope.repo", `github.label has no scope "repo"`, `did you mean "repository"?`},
		},
		{
			name:   "a scope on a type that takes none",
			inputs: "  repos: {type: github.repository, multi: true}\n  u: {type: github.user, scope: {repository: repos}}\n",
			want:   []string{"inputs.u.scope.repository", "github.user has no scope", "github.user takes no scope"},
		},
		{
			name:   "a scope bound to an undeclared input",
			inputs: "  l: {type: github.label, scope: {repository: repoz}}\n",
			want:   []string{"inputs.l.scope.repository", `input "repoz" is not declared`},
		},
		{
			name:   "a scope bound to an input of the wrong type",
			inputs: "  who: {type: github.user}\n  l: {type: github.label, scope: {repository: who}}\n",
			want:   []string{"inputs.l.scope.repository", `input "who" is a github.user, but scope "repository" of github.label needs a github.repository`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := staticErrors(typedWorkflow(t, tt.inputs, ""))
			require.NotEmpty(t, errs, "expected a validation error")
			joined := strings.Join(errs, "\n")
			for _, frag := range tt.want {
				assert.Contains(t, joined, frag)
			}
		})
	}
}

func TestAValidIntegrationInputDeclarationHasNoFindings(t *testing.T) {
	wf := typedWorkflow(t, `  repos: {type: github.repository, multi: true, required: true, min_items: 1, max_items: 50}
  trusted: {type: github.user, multi: true, default: ["123"]}
  label: {type: github.label, scope: {repository: repos}, default: skip-review}
  one: {type: github.repository, default: acme/web}
`, "")
	assert.Empty(t, staticErrors(wf))
}

// With no resolver set, the embedded catalog is the source: the types GitHub's
// manifest declares resolve, with the shipped schemas and scopes, and one it
// does not declare is unknown, with the declared ones to choose from.
func TestWithoutAResolverTheCatalogDeclaresTheTypes(t *testing.T) {
	wf := typedWorkflow(t, `  repos: {type: github.repository, multi: true, required: true, min_items: 1}
  who: {type: github.user, multi: true, default: ["583231"]}
  skip: {type: github.label, scope: {repository: repos}, default: skip-review}
`, "")
	assert.Empty(t, staticErrors(wf), "the shipped catalog declares these")

	bad := typedWorkflow(t, "  repos: {type: github.repo, multi: true}\n", "")
	res := StaticAnalysisWithOptions(bad, nil)
	require.True(t, res.HasErrors())
	assert.Contains(t, res.Errors()[0].Error(), `github has no input type "repo"`)
	assert.Contains(t, res.Errors()[0].Error(), "github.repository", "the nearest declared type is suggested")

	// The shipped value schema is what is enforced: a login is not a user id.
	wf = typedWorkflow(t, "  who: {type: github.user, default: octocat}\n", "")
	res = StaticAnalysisWithOptions(wf, nil)
	require.True(t, res.HasErrors())
	assert.Contains(t, res.Errors()[0].Error(), "github.user")

	// A scope the type does not declare is refused against the shipped scopes.
	wf = typedWorkflow(t, "  repos: {type: github.repository, multi: true}\n  r: {type: github.repository, scope: {team: repos}}\n", "")
	res = StaticAnalysisWithOptions(wf, nil)
	require.True(t, res.HasErrors())
	assert.Contains(t, res.Errors()[0].Error(), `github.repository has no scope "team"`)
}

// Bound values: a run's inputs and an activation's params.
func TestIntegrationInputValues(t *testing.T) {
	wf := typedWorkflow(t, `  repos: {type: github.repository, multi: true, required: true, min_items: 1, max_items: 2}
  one: {type: github.repository, default: acme/web}
  label: {type: github.label, default: ""}
`, "")
	gh := valuetypestest.GitHub()

	tests := []struct {
		name   string
		values map[string]any
		want   []string // empty: valid
	}{
		{"valid", map[string]any{"repos": []any{"acme/web", "acme/api"}}, nil},
		{"required and missing", map[string]any{}, []string{"required input 'repos' is not provided"}},
		{"required and null", map[string]any{"repos": nil}, []string{"required input 'repos' is not provided"}},
		{"too few", map[string]any{"repos": []any{}}, []string{"input 'repos' requires at least 1 item(s)"}},
		{"too many", map[string]any{"repos": []any{"a/1", "a/2", "a/3"}}, []string{"input 'repos' allows at most 2 item(s)"}},
		{"not a list", map[string]any{"repos": "acme/web"}, []string{"input 'repos' expects an array of github.repository values, got string"}},
		{"an item that is not a repository", map[string]any{"repos": []any{"acme/web", "nope"}}, []string{"item 1 is not a valid github.repository"}},
		{"an item of the wrong JSON type", map[string]any{"repos": []any{float64(7)}}, []string{"item 0 is not a valid github.repository"}},
		{"a single value of the wrong shape", map[string]any{"repos": []any{"a/1"}, "one": []any{"acme/web"}}, []string{"input 'one' is not a valid github.repository"}},
		{"a single value that fails the pattern", map[string]any{"repos": []any{"a/1"}, "one": "no slash"}, []string{"input 'one' is not a valid github.repository"}},
		{"label bounds come from the type", map[string]any{"repos": []any{"a/1"}, "label": strings.Repeat("x", 51)}, []string{"input 'label' is not a valid github.label"}},
		{"unknown input", map[string]any{"repos": []any{"a/1"}, "repo": "a/1"}, []string{"unknown input(s): repo", "did you mean 'repos'?"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ValidateInputsWith(wf, tt.values, gh)
			var got []string
			for _, e := range res.Errors() {
				got = append(got, e.Error())
			}
			if len(tt.want) == 0 {
				assert.Empty(t, got)
				return
			}
			for _, frag := range tt.want {
				assert.Contains(t, strings.Join(got, "\n"), frag)
			}
		})
	}

	t.Run("params leave unsupplied inputs alone but check supplied ones", func(t *testing.T) {
		assert.Empty(t, ValidateParamsWith(wf.GetInputs(), nil, gh))
		assert.Empty(t, ValidateParamsWith(wf.GetInputs(), map[string]any{"repos": []any{"acme/web"}}, gh))
		problems := ValidateParamsWith(wf.GetInputs(), map[string]any{"repos": []any{"nope"}}, gh)
		require.Len(t, problems, 1)
		assert.Contains(t, problems[0], "item 0 is not a valid github.repository")
	})
}

// A required input is required even though the workflow gives it a default-shaped
// sibling: required is explicit, not "no default".
func TestRequiredFlagMakesAnInputRequiredAtLaunch(t *testing.T) {
	wf := typedWorkflow(t, "  must: {type: string, required: true}\n  may: {type: string, default: x}\n", "")
	res := ValidateInputs(wf, map[string]any{})
	require.True(t, res.HasErrors())
	assert.Contains(t, res.Errors()[0].Error(), "required input 'must' is not provided")
	assert.False(t, ValidateInputs(wf, map[string]any{"must": "v"}).HasErrors())
}

// CEL typing: `inputs.repos` is list<string> when multi, string otherwise.
func TestIntegrationInputCELTypes(t *testing.T) {
	inputs := `  repos: {type: github.repository, multi: true, default: []}
  one: {type: github.repository, default: acme/web}
  n: {type: integer, default: 1}
`
	node := func(expr string) string {
		return "  - id: b\n    type: save_message\n    args:\n      role: assistant\n      content: \"{{ " + expr + " }}\"\n"
	}
	tests := []struct {
		name    string
		expr    string
		wantErr string
	}{
		{"membership in a multi input", "'acme/web' in inputs.repos", ""},
		{"a list macro over the strings", "inputs.repos.exists(r, r == inputs.one)", ""},
		{"size of the list", "size(inputs.repos)", ""},
		{"a string method on an item", "inputs.repos.map(r, r.startsWith('acme/'))", ""},
		{"a string method on the single value", "inputs.one.startsWith('acme/')", ""},
		{"the single value is not a list", "'x' in inputs.one", "no matching overload"},
		{"the list is not a string", "inputs.repos.startsWith('a')", "no matching overload"},
		{"an item compared with a number", "inputs.repos.exists(r, r == 1)", "no matching overload"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wf := typedWorkflow(t, inputs, node(tt.expr))
			errs := strings.Join(staticErrors(wf), "\n")
			if tt.wantErr == "" {
				assert.Empty(t, errs)
				return
			}
			assert.Contains(t, errs, tt.wantErr)
		})
	}
}

// Passing an integration input to a sub-workflow input checks the CEL type.
func TestIntegrationInputCELTypeNames(t *testing.T) {
	multi := &reliantv1.Input{Type: "github.repository", Config: &reliantv1.Input_IntegrationInput{IntegrationInput: &reliantv1.IntegrationInputConfig{Multi: true}}}
	single := &reliantv1.Input{Type: "github.repository", Config: &reliantv1.Input_IntegrationInput{IntegrationInput: &reliantv1.IntegrationInputConfig{}}}
	gh := valuetypestest.GitHub()

	assert.Equal(t, "list(string)", protoInputToCELType(multi, gh).String())
	assert.Equal(t, "string", protoInputToCELType(single, gh).String())
	assert.Equal(t, "list<github.repository>", protoInputTypeName(multi))
	assert.Equal(t, "github.repository", protoInputTypeName(single))

	// An unresolved type still types its values as strings: the unknown type is
	// reported once, on the declaration, not again at every use.
	assert.Equal(t, "list(string)", protoInputToCELType(multi, nil).String())
}

// triggerspec: activation.params is typed from the same resolver.
func TestActivationParamsSchemaForIntegrationInputs(t *testing.T) {
	wf := typedWorkflow(t, `  repos: {type: github.repository, multi: true, required: true}
  one: {type: github.repository, default: acme/web}
`, "")
	schema := triggerspec.ParamsSchema(wf.GetInputs(), valuetypestest.GitHub())
	props := schema["properties"].(map[string]any)

	repos := props["repos"].(map[string]any)
	assert.Equal(t, "array", repos["type"])
	items := repos["items"].(map[string]any)
	assert.Equal(t, "string", items["type"])
	assert.Contains(t, items["pattern"], "A-Za-z0-9")

	assert.Equal(t, "string", props["one"].(map[string]any)["type"])

	// Unresolvable: still typed as strings.
	un := triggerspec.ParamsSchema(wf.GetInputs(), nil)["properties"].(map[string]any)["repos"].(map[string]any)
	assert.Equal(t, "array", un["type"])
	assert.Equal(t, map[string]any{"type": "string"}, un["items"])
}

// A trigger filter over a typed multi input type-checks as list<string>.
func TestTriggerFilterOverATypedParamIsTypeChecked(t *testing.T) {
	run := func(filter string) []string {
		body := githubTrigger("issues.opened", "    filter: \""+filter+"\"\n")
		wf := typedWorkflow(t, "  repos: {type: github.repository, multi: true, required: true}\n", "triggers:\n"+body)
		return staticErrors(wf)
	}
	assert.Empty(t, run("trigger.payload.attributes.repository in trigger.activation.params.repos"))
	assert.Empty(t, run("trigger.activation.params.repos.exists(r, r.startsWith('acme/'))"))
	errs := strings.Join(run("trigger.activation.params.repos[0].foo == 'x'"), "\n")
	assert.Contains(t, errs, "foo")
}
