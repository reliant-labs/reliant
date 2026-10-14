// Package celeval evaluates one CEL expression or {{ }} template against a
// supplied context, the way a workflow would at run time, so an author can try
// a filter, an input mapping or a condition without activating anything and
// waiting for an event. The cel_eval tool, `reliant workflow eval` and
// `reliant workflow validate --trigger-payload` are all thin shells over it.
package celeval

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// Kind says how Expression is read.
type Kind string

const (
	// KindAuto reads a value with a {{ outside any string literal as a
	// template, anything else as a raw CEL expression.
	KindAuto Kind = ""
	// KindExpression: raw CEL, as a node condition, a while or a trigger filter.
	KindExpression Kind = "expression"
	// KindTemplate: text with {{ expr }} holes, as every other workflow field.
	KindTemplate Kind = "template"
)

// Scope is which namespaces the expression can see.
type Scope string

const (
	// ScopeRun is a node's scope: inputs, trigger, nodes, workflow, iter.
	ScopeRun Scope = ""
	// ScopeTrigger is a trigger filter's, inputs mapping's and prompt's: only
	// `trigger`. The run does not exist yet, so inputs and nodes do not either.
	ScopeTrigger Scope = "trigger"
)

// Request is one evaluation.
type Request struct {
	// Expression is the CEL or template. Empty with a trigger source set
	// returns the trigger root only (what an author needs to see to write one).
	Expression string
	Kind       Kind
	Scope      Scope

	// Integration and Event generate trigger.payload from the payload that
	// integration's manifest declares for the event.
	Integration string
	Event       string
	// TriggerKind overrides trigger.kind. Default: "integration" when an
	// integration is named, otherwise "webhook" when a payload is given.
	TriggerKind string
	// TriggerPayload is trigger.payload; with Integration/Event it is merged
	// over the generated sample, key by key.
	TriggerPayload map[string]any
	// TriggerSender is trigger.sender (kind, id, display_name, verified).
	TriggerSender map[string]any

	Inputs map[string]any
	// Nodes maps node id to that node's output map.
	Nodes map[string]any
}

// Result is the outcome of an evaluation. Exactly one of Error and the
// Value/Type pair is meaningful.
type Result struct {
	Value any    `json:"value"`
	Type  string `json:"type,omitempty"`
	// Kind is how the expression was read: "expression" or "template".
	Kind  string `json:"kind,omitempty"`
	Error *Error `json:"error,omitempty"`
	// Findings are type problems with the expression against the integration's
	// declared payload that the loose evaluation above did not trip over: a
	// field the payload never has, a list of strings read as objects. A filter
	// that evaluates here can still be wrong on the next event.
	Findings []string `json:"findings,omitempty"`
	// Trigger is the `trigger` root the expression ran against, when one was
	// supplied or generated.
	Trigger map[string]any `json:"trigger,omitempty"`
	// Source says where trigger.payload came from.
	Source string `json:"payload_source,omitempty"`
}

// Error is a failed evaluation.
type Error struct {
	// Phase: "syntax", "compile", "eval" or "request" (the request itself was
	// unusable: an unknown integration or event, a bad value).
	Phase string `json:"phase"`
	// Message is the whole error, one line per problem.
	Message string `json:"message"`
	// Line and Column locate a syntax or compile error within the expression
	// (1-based); zero when not applicable.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
	// Hint is what to try.
	Hint string `json:"hint,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Evaluate runs one request. It never panics on author input; every failure is
// in Result.Error.
func Evaluate(req Request) Result {
	res := Result{}

	trigger, source, err := buildTrigger(req)
	if err != nil {
		res.Error = &Error{Phase: "request", Message: err.Error()}
		return res
	}
	res.Trigger, res.Source = trigger, source

	if strings.TrimSpace(req.Expression) == "" {
		if trigger == nil {
			res.Error = &Error{Phase: "request", Message: "nothing to evaluate: give an expression, or an integration and event to see the trigger it delivers"}
		}
		return res
	}

	kind := req.Kind
	if kind == KindAuto {
		kind = KindExpression
		if _, wrapped := wfcel.RawCELTemplateDelimiter(req.Expression); wrapped {
			kind = KindTemplate
		}
	}
	res.Kind = string(kind)

	inputs := normalizeNumbers(req.Inputs)
	nodes := normalizeNumbers(req.Nodes)
	var ctx wfcel.CELEvalContext
	switch req.Scope {
	case ScopeTrigger:
		if len(inputs) > 0 || len(nodes) > 0 {
			res.Error = &Error{Phase: "request", Message: "scope trigger sees only `trigger`; inputs and nodes do not exist when a trigger's filter, inputs mapping or prompt runs",
				Hint: "drop inputs/nodes, or use the default scope to test a node's expression"}
			return res
		}
		ctx = &triggerContext{root: trigger}
	default:
		in := map[string]any{}
		for k, v := range inputs {
			in[k] = v
		}
		if trigger != nil {
			in[wfcel.TriggerInputKey] = trigger
		}
		ctx = &wfcel.NodeResolutionContext{
			Inputs:   in,
			Nodes:    nodes,
			Workflow: &model.WorkflowContext{},
		}
	}

	var value any
	var typ string
	switch kind {
	case KindTemplate:
		value, typ, err = wfcel.EvaluateTemplateTyped(req.Expression, ctx)
	case KindExpression:
		if _, wrapped := wfcel.RawCELTemplateDelimiter(req.Expression); wrapped {
			res.Error = &Error{Phase: "request", Message: "expression is raw CEL but contains {{ }}", Hint: "write it without the braces, or set kind to template"}
			return res
		}
		value, typ, err = wfcel.EvaluateValueTyped(req.Expression, ctx)
	default:
		res.Error = &Error{Phase: "request", Message: fmt.Sprintf("kind must be expression or template, not %q", kind)}
		return res
	}
	// The declared payload explains a failure as well as a pass: a read that
	// errors on the sample ("no such key: name") is usually a read the payload
	// can never satisfy, and the shape says why.
	if req.Integration != "" && req.Event != "" {
		res.Findings = shapeFindings(req, kind)
	}
	if err != nil {
		res.Error = toError(err, req.Expression)
		return res
	}
	res.Value, res.Type = value, typ
	return res
}

// buildTrigger returns the `trigger` root, built by the code that builds it for
// a real run, or nil when the request names no trigger.
func buildTrigger(req Request) (map[string]any, string, error) {
	payload := map[string]any{}
	source := ""
	switch {
	case req.Integration != "" || req.Event != "":
		if req.Integration == "" || req.Event == "" {
			return nil, "", errors.New("integration and event go together: name both to generate a trigger payload")
		}
		schema, err := EventSchema(req.Integration, req.Event)
		if err != nil {
			return nil, "", err
		}
		generated, _ := SampleFromSchema(schema).(map[string]any)
		if generated == nil {
			generated = map[string]any{}
		}
		generated["event"] = req.Event
		payload = generated
		source = fmt.Sprintf("generated from the payload %s declares for %s", req.Integration, req.Event)
		if len(req.TriggerPayload) > 0 {
			payload = mergeInto(payload, normalizeNumbers(req.TriggerPayload))
			source += ", with your trigger_payload merged over it"
		}
	case len(req.TriggerPayload) > 0:
		payload = normalizeNumbers(req.TriggerPayload)
		source = "supplied"
	case len(req.TriggerSender) > 0 || req.TriggerKind != "":
		source = "supplied"
	default:
		return nil, "", nil
	}

	kind := req.TriggerKind
	if kind == "" {
		kind = string(core.TriggerEventKindWebhook)
		if req.Integration != "" {
			kind = string(core.TriggerEventKindIntegration)
		}
	}
	info := &runtime.TriggerInfo{Kind: kind, TriggerID: "sample-trigger", EventID: "sample-event", OccurredAt: "2026-01-01T00:00:00Z", Payload: payload}
	if len(req.TriggerSender) > 0 {
		info.Sender = senderFrom(req.TriggerSender)
	}
	return info.CELValue(), source, nil
}

func senderFrom(m map[string]any) *core.TriggerSender {
	str := func(k string) string { s, _ := m[k].(string); return s }
	verified, _ := m["verified"].(bool)
	return &core.TriggerSender{Kind: core.TriggerSenderKind(str("kind")), ID: str("id"), DisplayName: str("display_name"), Verified: verified}
}

// mergeInto overlays over onto base, recursing into objects; anything else in
// over replaces what base has.
func mergeInto(base, over map[string]any) map[string]any {
	for k, v := range over {
		if ov, ok := v.(map[string]any); ok {
			if bv, ok := base[k].(map[string]any); ok {
				base[k] = mergeInto(bv, ov)
				continue
			}
		}
		base[k] = v
	}
	return base
}

// normalizeNumbers turns whole-number floats (what encoding/json yields for
// every number) into int64, the form a payload number has when a filter runs on
// the event as received.
func normalizeNumbers(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = normalizeValue(v)
	}
	return out
}

func normalizeValue(v any) any {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		f, _ := t.Float64()
		return f
	case map[string]any:
		return normalizeNumbers(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = normalizeValue(t[i])
		}
		return out
	}
	return v
}

func toError(err error, expr string) *Error {
	var evalErr *wfcel.EvalError
	if errors.As(err, &evalErr) {
		out := &Error{Phase: string(evalErr.Phase), Message: err.Error()}
		if len(evalErr.Issues) > 0 {
			out.Line, out.Column = evalErr.Issues[0].Line, evalErr.Issues[0].Column
			var lines []string
			for _, is := range evalErr.Issues {
				lines = append(lines, fmt.Sprintf("line %d, column %d: %s", is.Line, is.Column, is.Message))
			}
			out.Message = string(evalErr.Phase) + " error in " + oneLine(expr) + ": " + strings.Join(lines, "; ")
		}
		switch evalErr.Phase {
		case wfcel.PhaseEval:
			out.Hint = "a field the data lacks is an error, not null: guard it with has(x.f) or x.?f.orValue(default)"
		case wfcel.PhaseCompile:
			out.Hint = "check names against the namespaces in scope (inputs, trigger, nodes, workflow, iter) and function signatures (get_cel_reference)"
		}
		return out
	}
	var synErr *wfcel.TemplateSyntaxError
	if errors.As(err, &synErr) {
		return &Error{Phase: "syntax", Message: err.Error(), Line: synErr.Line, Column: synErr.Column,
			Hint: "every {{ needs a closing }}; braces and quotes inside the expression must balance"}
	}
	return &Error{Phase: "eval", Message: err.Error()}
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// shapeFindings checks the expression against the integration's declared
// payload, the check workflow validation runs on a trigger's filter.
func shapeFindings(req Request, kind Kind) []string {
	schema, err := EventSchema(req.Integration, req.Event)
	if err != nil {
		return nil
	}
	shape, err := triggerspec.NewShape(schema)
	if err != nil {
		return nil
	}
	var exprs []string
	if kind == KindTemplate {
		exprs, _ = wfcel.TemplateExpressions(req.Expression, false)
	} else {
		exprs = []string{req.Expression}
	}
	var out []string
	for _, expr := range exprs {
		for _, te := range shape.CheckExpr(expr) {
			msg := te.Reason
			if te.Suggestion != "" {
				msg += " (" + te.Suggestion + ")"
			}
			out = append(out, msg)
		}
	}
	return out
}

// triggerContext is a CEL context with only the `trigger` namespace.
type triggerContext struct{ root map[string]any }

func (c *triggerContext) Namespaces() []wfcel.CELNamespace {
	return []wfcel.CELNamespace{wfcel.CELTrigger}
}

func (c *triggerContext) Activation() map[string]any {
	return wfcel.EnsureNamespaceDefaults(map[string]any{string(wfcel.CELTrigger): c.root}, c.Namespaces())
}
