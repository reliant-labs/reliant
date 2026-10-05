package manifest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withTriggers is validHTTP plus a triggers block.
func withTriggers(block string) string { return validHTTP + "triggers:\n" + block }

const validTrigger = `  - id: thing.created
    display_name: Thing created
    summary: A thing was created.
    description: Fires when a thing is created in the connected account.
    keywords: [new]
    events: [thing.created]
    attributes:
      - { name: owner, description: The thing's owner., example: acme }
    data:
      type: object
      properties:
        thing: { type: object, properties: { id: { type: string } } }
`

func TestTriggersLoad(t *testing.T) {
	m, err := Parse([]byte(withTriggers(validTrigger)), TrustCurated)
	require.NoError(t, err)
	require.Len(t, m.GetTriggers(), 1)
	tr := m.GetTriggers()[0]
	assert.Equal(t, "thing.created", tr.GetId())
	assert.Equal(t, []string{"thing.created"}, tr.GetEvents())
	require.Len(t, tr.GetAttributes(), 1)
	assert.Equal(t, "owner", tr.GetAttributes()[0].GetName())
	assert.Equal(t, "acme", tr.GetAttributes()[0].GetExample())
}

// Actions and triggers share one ref namespace (`demo/thing.get@1` is what
// search returns and get_integration_schema takes), so an id may name one or
// the other, never both.
func TestTriggerMayNotShareAnActionID(t *testing.T) {
	doc := withTriggers(strings.Replace(validTrigger, "id: thing.created", "id: thing.get", 1))
	_, err := Parse([]byte(doc), TrustCurated)
	assert.ErrorContains(t, err, `trigger id "thing.get" is also an action id`)
}

// A manifest with only triggers (a provider that only delivers events) is
// valid; it used to need an action.
func TestTriggerOnlyManifestLoads(t *testing.T) {
	doc := `
id: pager
version: 1
display_name: Pager
triggers:
  - id: alert.fired
    display_name: Alert fired
    events: [alert.fired]
    data: { type: object }
`
	_, err := Parse([]byte(doc), TrustCurated)
	assert.NoError(t, err)

	empty := "\nid: nothing\nversion: 1\ndisplay_name: Nothing\n"
	_, err = Parse([]byte(empty), TrustCurated)
	assert.ErrorContains(t, err, "at least one action or trigger")
}

func TestInvalidTriggersRejected(t *testing.T) {
	cases := map[string]string{
		"bad id":            strings.Replace(validTrigger, "id: thing.created", "id: Thing!", 1),
		"no events":         strings.Replace(validTrigger, "events: [thing.created]", "events: []", 1),
		"blank event":       strings.Replace(validTrigger, "events: [thing.created]", `events: [""]`, 1),
		"spaced event":      strings.Replace(validTrigger, "events: [thing.created]", `events: ["thing created"]`, 1),
		"wildcard event":    strings.Replace(validTrigger, "events: [thing.created]", `events: ["thing.*"]`, 1),
		"dup event":         strings.Replace(validTrigger, "events: [thing.created]", `events: [a.b, a.b]`, 1),
		"bad attribute":     strings.Replace(validTrigger, "name: owner", "name: Owner-Name", 1),
		"no data":           validTrigger[:strings.Index(validTrigger, "    data:")],
		"data not object":   strings.Replace(validTrigger, "      type: object\n      properties:", "      type: string\n      properties:", 1),
		"bad keyword":       strings.Replace(validTrigger, "keywords: [new]", `keywords: ["New Thing"]`, 1),
		"multiline summary": strings.Replace(validTrigger, "summary: A thing was created.", `summary: "a\nb"`, 1),
		"unknown field":     validTrigger + "    poll: true\n",
	}
	for name, block := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(withTriggers(block)), TrustCurated)
			assert.Error(t, err)
		})
	}

	dupID := withTriggers(validTrigger + validTrigger)
	_, err := Parse([]byte(dupID), TrustCurated)
	assert.ErrorContains(t, err, `duplicate trigger id "thing.created"`)

	dupAttr := withTriggers(strings.Replace(validTrigger,
		"      - { name: owner, description: The thing's owner., example: acme }\n",
		"      - { name: owner }\n      - { name: owner }\n", 1))
	_, err = Parse([]byte(dupAttr), TrustCurated)
	assert.ErrorContains(t, err, `duplicate attribute "owner"`)
}

// The payload schema is the envelope every integration event is recorded in
// (webhook.toInbound / triggers.pollEvent), with the declared data schema
// under `data` and the declared attributes under `attributes`. A filter
// written against it reads trigger.payload.<field>.
func TestTriggerPayloadSchema(t *testing.T) {
	m, err := Parse([]byte(withTriggers(validTrigger)), TrustCurated)
	require.NoError(t, err)
	schema := TriggerPayloadSchema(m, m.GetTriggers()[0])

	assert.Equal(t, "object", schema["type"])
	props, ok := schema["properties"].(map[string]any)
	require.True(t, ok)
	for _, key := range []string{"integration", "event", "account", "delivery_id", "attributes", "data"} {
		assert.Contains(t, props, key)
	}
	assert.Equal(t, map[string]any{"type": "string", "const": "demo"}, props["integration"])
	assert.Equal(t, map[string]any{"type": "string", "enum": []any{"thing.created"}}, props["event"])

	attrs := props["attributes"].(map[string]any)
	assert.Equal(t, []any{"owner"}, attrs["required"])
	owner := attrs["properties"].(map[string]any)["owner"].(map[string]any)
	assert.Equal(t, "string", owner["type"])
	assert.Equal(t, "The thing's owner.", owner["description"])
	assert.Equal(t, []any{"acme"}, owner["examples"])

	data := props["data"].(map[string]any)
	assert.Contains(t, data["properties"], "thing")

	// It is a valid schema: the form generator and filter checker read it.
	require.NoError(t, validateSchema("payload", schema))

	// The declaration is not aliased: mutating the result leaves the
	// manifest's schema untouched.
	data["mutated"] = true
	again := TriggerPayloadSchema(m, m.GetTriggers()[0])
	assert.NotContains(t, again["properties"].(map[string]any)["data"], "mutated")
}

func TestTriggerByID(t *testing.T) {
	m, err := Parse([]byte(withTriggers(validTrigger)), TrustCurated)
	require.NoError(t, err)
	tr, ok := Trigger(m, "thing.created")
	require.True(t, ok)
	assert.Equal(t, "Thing created", tr.GetDisplayName())
	_, ok = Trigger(m, "thing.get")
	assert.False(t, ok, "an action id is not a trigger")
}
