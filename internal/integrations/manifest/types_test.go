package manifest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
)

// typesManifest is a small integration with a list action and the types built
// on it, as the starting point of every test below: each negative case breaks
// one thing in it.
const typesManifest = `
id: demo
version: 1
display_name: Demo
connection:
  base_url: https://api.example.com
actions:
  - id: widgets.list
    placement: server
    mutates: false
    params:
      type: object
      properties:
        owner: { type: string }
        q: { type: string }
        after: { type: string }
        group_id: { type: integer }
    request:
      method: GET
      path: /widgets
      pagination: { style: cursor, cursor_param: after, next_cursor: "response.?next.orValue('')" }
    output:
      select: response.items
      schema:
        type: object
        properties:
          next: { type: string }
          items:
            type: array
            items:
              type: object
              properties:
                id: { type: integer }
                name: { type: string }
                owner: { type: string }
                private: { type: boolean }
                can_edit: { type: boolean }
  - id: widgets.update
    placement: server
    mutates: true
    request: { method: POST, path: /widgets }
  - id: widgets.get
    placement: server
    params:
      type: object
      required: [id]
      properties:
        id: { type: integer }
    request: { method: GET, path: "/widgets/{{ string(int(params.id)) }}" }
    output:
      schema:
        type: object
        properties:
          id: { type: integer }
          name: { type: string }
types:
  - id: group
    value: { type: string, pattern: "^[0-9]+$" }
    options:
      action: widgets.list
      value: string(item.id)
      label: item.name
  - id: widget
    display_name: Widget
    value: { type: string, pattern: "^[a-z]+/[a-z]+$", examples: [acme/app] }
    options:
      action: widgets.list
      params:
        group_id: "{{ has(scope.group) ? dyn(int(scope.group)) : dyn(null) }}"
      search: param:q
      paging: { next_token_path: next, token_param: after }
      value: item.owner + '/' + item.name
      label: item.name
      description: "item.private ? 'private' : 'public'"
      group: item.owner
      annotations:
        can_edit: item.can_edit
      warnings:
        - when: "context.acting_identity == 'app' && !option.annotations.can_edit"
          message: The bot cannot edit this.
      scope:
        group: { type: group }
      resolve:
        action: widgets.get
        params: { id: "{{ int(value) }}" }
        label: item.name
`

func TestTypesParse(t *testing.T) {
	m, err := Parse([]byte(typesManifest), TrustCurated)
	require.NoError(t, err)
	require.Len(t, m.GetTypes(), 2)

	w, ok := Type(m, "widget")
	require.True(t, ok)
	assert.Equal(t, "demo.widget", TypeRef(m, w))
	assert.Equal(t, "string", ValueSchema(w)["type"])
	assert.Equal(t, "widgets.list", w.GetOptions().GetAction())
	assert.Equal(t, map[string]string{"group": "group"}, map[string]string{"group": w.GetOptions().GetScope()["group"].GetType()})
}

func TestTypeRefHelpers(t *testing.T) {
	assert.Equal(t, "demo.widget", QualifyTypeRef("demo", "widget"))
	assert.Equal(t, "other.widget", QualifyTypeRef("demo", "other.widget"))
	assert.Equal(t, "", QualifyTypeRef("demo", ""))
	i, id, ok := SplitTypeRef("github.repository")
	assert.True(t, ok)
	assert.Equal(t, [2]string{"github", "repository"}, [2]string{i, id})
	_, _, ok = SplitTypeRef("repository")
	assert.False(t, ok)
	_, _, ok = SplitTypeRef("Github.repository")
	assert.False(t, ok)
}

// replace edits typesManifest, failing the test if the text is not in it.
func replace(t *testing.T, old, new string) string {
	t.Helper()
	require.Contains(t, typesManifest, old, "the test's edit point moved")
	return strings.Replace(typesManifest, old, new, 1)
}

func TestTypesAreValidatedAtLoad(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"duplicate id", replace(t, "  - id: widget\n", "  - id: group\n"), `duplicate type id "group"`},
		{"bad id", replace(t, "  - id: widget\n", "  - id: Widget\n"), "id must match"},
		{"no value schema", replace(t, "    value: { type: string, pattern: \"^[a-z]+/[a-z]+$\", examples: [acme/app] }\n", ""), "value is required"},
		{"value schema without a type", replace(t, "    value: { type: string, pattern: \"^[a-z]+/[a-z]+$\", examples: [acme/app] }", "    value: { pattern: x }"), "JSON Schema with a type"},
		{"value schema that does not compile", replace(t, `pattern: "^[a-z]+/[a-z]+$", examples`, `pattern: "([", examples`), "value"},
		{"example that is not a value", replace(t, "examples: [acme/app]", "examples: [ACME]"), "examples[0]"},

		{"action missing", replace(t, "      action: widgets.list\n      params:", "      action: widgets.nope\n      params:"), `"widgets.nope" is not an action`},
		{"action mutates", replace(t, "      action: widgets.list\n      params:", "      action: widgets.update\n      params:"), "mutates: false"},
		{"action lacks the param that receives the query", replace(t, "      action: widgets.list\n      params:", "      action: widgets.get\n      params:"), `widgets.get has no param "q"`},
		{"no value projection", replace(t, "      value: item.owner + '/' + item.name\n", ""), "value is required"},
		{"projection does not compile", replace(t, "      label: item.name\n      description", "      label: item.name +\n      description"), "label"},
		{"projection reads a field the action does not return", replace(t, "      label: item.name\n      description", "      label: item.title\n      description"), `has no field "title"`},
		{"annotation reads a missing field", replace(t, "can_edit: item.can_edit", "can_edit: item.can_modify"), `has no field "can_modify"`},
		{"warning reads a missing annotation", replace(t, "!option.annotations.can_edit", "!option.annotations.can_delete"), `has no field "can_delete"`},
		{"warning reads a missing context field", replace(t, "context.acting_identity == 'app'", "context.role == 'app'"), `has no field "role"`},
		{"warning without a message", replace(t, "          message: The bot cannot edit this.\n", "          message: \"\"\n"), "needs when and message"},

		{"param the action does not take", replace(t, "group_id: \"{{ has", "colour: \"{{ has"), `has no param "colour"`},
		{"param template reads an undeclared scope key", replace(t, "has(scope.group)", "has(scope.team)"), `has no field "team"`},
		{"required action param nothing supplies", replace(t, "    params:\n      type: object\n      properties:\n        owner: { type: string }", "    params:\n      type: object\n      required: [owner]\n      properties:\n        owner: { type: string }"), `requires param "owner"`},

		{"search param the action lacks", replace(t, "search: param:q", "search: param:term"), `no param "term"`},
		{"search of a bad form", replace(t, "search: param:q", "search: server"), `search "server" must be`},
		{"search param also set by params", replace(t, "group_id: \"{{ has", "q: \"x\"\n        group_id: \"{{ has"), "receives the query"},
		{"paging token param the action lacks", replace(t, "token_param: after", "token_param: cursor"), `no param "cursor"`},
		{"paging path outside the output", replace(t, "next_token_path: next", "next_token_path: after_page"), "is not a field"},
		{"paging with half a contract", replace(t, "paging: { next_token_path: next, token_param: after }", "paging: { token_param: after }"), "paging needs"},

		{"scope of an unknown type", replace(t, "group: { type: group }", "group: { type: team }"), `declares no type "team"`},
		{"scope of a type with a bad ref", replace(t, "group: { type: group }", "group: { type: Group }"), "must match"},
		{"scope key not a name", replace(t, "        group: { type: group }\n      resolve", "        Group-Key: { type: group }\n      resolve"), "scope key"},
		{"scope without a type", replace(t, "group: { type: group }", "group: { required: true }"), "type is required"},

		{"resolve action missing", replace(t, "        action: widgets.get", "        action: widgets.gone"), `"widgets.gone" is not an action`},
		{"resolve label missing", replace(t, "        label: item.name\n", ""), "label is required"},
		{"resolve label reads a missing field", replace(t, "        label: item.name\n", "        label: item.login\n"), `has no field "login"`},
		{"resolve param template that does not compile", replace(t, `id: "{{ int(value) }}"`, `id: "{{ int( }}"`), "params.id"},
		{"resolve requires a param nothing supplies", replace(t, "        params: { id: \"{{ int(value) }}\" }\n", ""), `requires param "id"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte(tc.doc), TrustCurated)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestTypeScopesMayNotFormACycle(t *testing.T) {
	doc := replace(t, "      value: string(item.id)\n      label: item.name\n  - id: widget",
		"      value: string(item.id)\n      label: item.name\n      scope:\n        w: { type: widget }\n  - id: widget")
	_, err := Parse([]byte(doc), TrustCurated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cycle")
}

func TestItemsCanBeANamedField(t *testing.T) {
	doc := replace(t, "      value: string(item.id)\n      label: item.name\n  - id: widget",
		"      items: output.items\n      value: string(item.id)\n      label: item.name\n  - id: widget")
	_, err := Parse([]byte(doc), TrustCurated)
	require.NoError(t, err)

	_, err = Parse([]byte(strings.Replace(doc, "items: output.items", "items: output.rows", 1)), TrustCurated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `has no field "rows"`)

	_, err = Parse([]byte(strings.Replace(doc, "items: output.items", "items: output.next", 1)), TrustCurated)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a list")
}

func TestAttributeTypesMustResolve(t *testing.T) {
	const trigger = `
triggers:
  - id: widget.made
    events: [widget.made]
    attributes:
      - { name: widget, type: %s }
    data: { type: object }
`
	ok, err := Parse([]byte(typesManifest+strings.Replace(trigger, "%s", "widget", 1)), TrustCurated)
	require.NoError(t, err)
	assert.Equal(t, "widget", ok.GetTriggers()[0].GetAttributes()[0].GetType())

	for ref, want := range map[string]string{
		"gadget":      `declares no type "gadget"`,
		"demo.gadget": `declares no type "gadget"`,
		"Widget":      "must match",
	} {
		_, err := Parse([]byte(typesManifest+strings.Replace(trigger, "%s", ref, 1)), TrustCurated)
		require.Error(t, err, ref)
		assert.Contains(t, err.Error(), want, ref)
	}
}

func TestTypeRefsAcrossManifests(t *testing.T) {
	demo, err := Parse([]byte(typesManifest), TrustCurated)
	require.NoError(t, err)
	// A scope may name another integration's type, which only the whole set
	// can check; one manifest alone accepts it.
	otherDoc := strings.Replace(strings.Replace(typesManifest, "id: demo", "id: other", 1), "group: { type: group }", "group: { type: demo.group }", 1)
	other, err := Parse([]byte(otherDoc), TrustCurated)
	require.NoError(t, err)

	require.NoError(t, CheckTypeRefs(nil))
	require.NoError(t, CheckTypeRefs([]*reliantv1.IntegrationManifest{demo, other}))

	err = CheckTypeRefs([]*reliantv1.IntegrationManifest{other})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no integration declares the type "demo.group"`)

	// An attribute of another integration's type, likewise.
	const trigger = `
triggers:
  - id: widget.made
    events: [widget.made]
    attributes:
      - { name: widget, type: demo.widget }
    data: { type: object }
`
	third, err := Parse([]byte(strings.Replace(typesManifest, "id: demo", "id: third", 1)+trigger), TrustCurated)
	require.NoError(t, err)
	require.NoError(t, CheckTypeRefs([]*reliantv1.IntegrationManifest{demo, third}))
	err = CheckTypeRefs([]*reliantv1.IntegrationManifest{third})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `no integration declares the type "demo.widget"`)
}
