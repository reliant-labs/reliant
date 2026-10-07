// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// issuePayload is a trigger.payload schema shaped like GitHub's: label names
// as a list of strings, a nested user object, routing attributes.
func issuePayload(extra map[string]any) map[string]any {
	data := map[string]any{
		"issue": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"number": map[string]any{"type": "integer"},
				"title":  map[string]any{"type": "string"},
				"labels": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Label names."},
				"user": map[string]any{
					"type":       "object",
					"properties": map[string]any{"login": map[string]any{"type": "string"}},
				},
			},
		},
	}
	for k, v := range extra {
		data[k] = v
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"event": map[string]any{"type": "string"},
			"attributes": map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"repository": map[string]any{"type": "string"}},
				"additionalProperties": map[string]any{"type": "string"},
			},
			"data": map[string]any{"type": "object", "properties": data},
		},
	}
}

func mustShape(t *testing.T, schemas ...map[string]any) *Shape {
	t.Helper()
	s, err := NewShape(schemas...)
	require.NoError(t, err)
	return s
}

func reasons(errs []*TypeError) string {
	out := make([]string, len(errs))
	for i, e := range errs {
		out[i] = e.Reason
	}
	return strings.Join(out, "; ")
}

func TestShapeRejectsAFieldOfAString(t *testing.T) {
	s := mustShape(t, issuePayload(nil))
	for expr, want := range map[string][]string{
		"trigger.payload.data.issue.labels.exists(l, l.name == 'wontfix')": {
			"l.name reads field 'name' of l, an element of trigger.payload.data.issue.labels",
			"is list(string) (Label names)", "each element is a string",
		},
		"trigger.payload.data.issue.labels.map(l, l.name).exists(n, n == 'x')": {"l.name", "list(string)"},
		"trigger.payload.data.issue.labels[0].name == 'x'": {
			"trigger.payload.data.issue.labels[0].name reads field 'name' of an element of trigger.payload.data.issue.labels",
		},
		"trigger.payload.data.issue.title.text == 'x'": {
			"trigger.payload.data.issue.title.text reads field 'text' of trigger.payload.data.issue.title, which is string",
		},
		"trigger.payload.data.issue.labels.name == 'x'":      {"of trigger.payload.data.issue.labels, which is list(string)"},
		"trigger.payload.data.issue.user.login.first == 'x'": {"trigger.payload.data.issue.user.login, which is string"},
		"has(trigger.payload.data.issue.title.text)":         {"trigger.payload.data.issue.title.text reads field 'text'"},
		"trigger.payload.data.issue.labels.filter(l, l.size() > 1)[0].name == 'x'": {
			"field 'name': type 'string' does not support field selection",
		},
	} {
		errs := s.CheckFilter(expr)
		require.NotEmpty(t, errs, expr)
		for _, w := range want {
			assert.Contains(t, reasons(errs), w, expr)
		}
	}
}

func TestShapeAcceptsWhatTheRuntimeRuns(t *testing.T) {
	s := mustShape(t, issuePayload(nil))
	for _, expr := range []string{
		"!('wontfix' in trigger.payload.data.issue.labels)",
		"trigger.payload.data.issue.labels.exists(l, l == 'bug' || l.startsWith('p'))",
		"trigger.payload.data.issue.user.login != 'dependabot[bot]'",
		"trigger.payload.data.issue.user != null && trigger.payload.data.issue.title != null",
		// int at intake, double after storage: neither static type holds.
		"trigger.payload.data.issue.number > 10 && trigger.payload.data.issue.number != 42.0",
		"trigger.payload.data.issue.number % 2 == 0",
		// Objects are open unless declared closed.
		"has(trigger.payload.data.issue.milestone) && trigger.payload.data.issue.milestone.title == 'v1'",
		"trigger.payload.attributes['repository'] == 'acme/app' && trigger.payload.attributes.branch == 'main'",
		"trigger.payload.data.?issue.?title.orValue('') != ''",
		"size(trigger.payload.data.issue.labels) > 0",
		"trigger.sender.verified && trigger.sender.id in ['1'] && trigger.kind == 'integration'",
	} {
		assert.Empty(t, reasons(s.CheckFilter(expr)), expr)
	}
}

// Several events are their union: a field any of them declares may be read,
// and one they type differently is unconstrained.
func TestShapeOfSeveralEventsIsTheirUnion(t *testing.T) {
	labeled := issuePayload(map[string]any{"label": map[string]any{"type": "string"}, "changed": map[string]any{"type": "string"}})
	edited := issuePayload(map[string]any{"changed": map[string]any{"type": "object", "properties": map[string]any{"from": map[string]any{"type": "string"}}}})
	s := mustShape(t, labeled, edited)

	assert.Empty(t, reasons(s.CheckFilter("trigger.payload.data.label == 'bug'")), "declared by one event")
	assert.Empty(t, reasons(s.CheckFilter("trigger.payload.data.changed.from == 'x'")), "typed differently: unconstrained")
	assert.Contains(t, reasons(s.CheckFilter("trigger.payload.data.label.name == 'bug'")), "trigger.payload.data.label, which is string")
	assert.Contains(t, reasons(s.CheckFilter("trigger.payload.data.issue.labels.exists(l, l.name == 'x')")), "list(string)",
		"a field both declare alike keeps its type")
}

func TestShapeClosedObjectsNameTheirFields(t *testing.T) {
	s := mustShape(t, map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{"body": map[string]any{"type": "string"}, "from": map[string]any{"type": "string"}},
	})
	assert.Contains(t, reasons(s.CheckFilter("trigger.payload.bdy == 'x'")), "trigger.payload has no field 'bdy' (its fields: body, from)")
	assert.Empty(t, reasons(s.CheckFilter("trigger.payload.body.contains('STOP')")))
}

// The root is `trigger` as the runtime builds it, whatever the payload.
func TestShapeRootIsClosedAndAnUndeclaredPayloadIsDynamic(t *testing.T) {
	s := mustShape(t)
	assert.Empty(t, reasons(s.CheckFilter("trigger.payload.anything.at.all == 1")))
	assert.Contains(t, reasons(s.CheckFilter("trigger.paylod.x == 1")), "trigger has no field 'paylod'")
	assert.Contains(t, reasons(s.CheckFilter("trigger.sender.login == 'x'")), "trigger.sender has no field 'login'")
}

func TestShapeFilterMustBeABool(t *testing.T) {
	s := mustShape(t, issuePayload(map[string]any{"draft": map[string]any{"type": "boolean"}}))
	assert.Contains(t, reasons(s.CheckFilter("trigger.payload.data.issue.title")), "must evaluate to a bool, not string")
	assert.Empty(t, reasons(s.CheckFilter("trigger.payload.data.draft")), "a nullable bool")
	assert.Empty(t, reasons(s.CheckFilter("trigger.payload.data.issue.number")), "dyn is checked when it runs")
	assert.Empty(t, reasons(s.CheckExpr("trigger.payload.data.issue.title")), "a template expression may be any type")
}

// Syntax and unknown roots are CompileFilter's to report, once.
func TestShapeLeavesCompileErrorsToCompile(t *testing.T) {
	s := mustShape(t, issuePayload(nil))
	assert.Empty(t, s.CheckFilter("trigger.payload.data =="))
	_, err := CompileFilter("trigger.payload.data ==")
	assert.Error(t, err)
}

func TestShapeTypeErrorsUseThePayloadsTypeNames(t *testing.T) {
	s := mustShape(t, issuePayload(nil))
	r := reasons(s.CheckFilter("trigger.payload.data.issue.title > 5"))
	assert.Contains(t, r, "(string, int)")
	assert.NotContains(t, r, "wrapper(")
	r = reasons(s.CheckFilter("trigger.payload.data.issue.user == 'octocat'"))
	assert.Contains(t, r, "object trigger.payload.data.issue.user")
	assert.NotContains(t, r, shapeTypePrefix)
}
