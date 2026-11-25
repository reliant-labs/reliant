// Copyright (c) 2026 Reliant Labs

// Package wfobserve evaluates a workflow node's observe: block after the node
// finishes, and records what it produced: the stored row (node_observations),
// the node span's attributes and events, and the metrics
// (dev-docs/design/workflow-observe.md).
//
// Nothing here can fail, retry or delay the node. Each expression is evaluated
// alone, inside a recover, under CEL's cost limit and a budget for the whole
// block; one that fails drops its key, event or metric and is reported as an
// Error. What leaves the tenant's own storage as telemetry passes through one
// function, SafeForExport.
//
//forge:exclude-contract: pure evaluation and value rules over a node's result, plus one concrete metrics Recorder on an injected meter (tests pass their own); no I/O, and no second implementation to substitute
package wfobserve

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
	"unicode/utf8"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/telemetry"
	wfcel "github.com/reliant-labs/reliant/internal/workflow/cel"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// The caps applied to an evaluated block (design section 8.1). A block's own
// counts are shape rules, checked at parse (model.ObserveProblems); these hold
// the effective block, the workflow's defaults merged with the node's, to the
// same counts.
const (
	// EvalBudget is the wall time one block's evaluation may take; what is
	// left when it runs out is skipped.
	EvalBudget = 50 * time.Millisecond
	// MaxEachItems is how many events one each: may emit.
	MaxEachItems = 20
	// MaxStoredStringBytes bounds every string value stored.
	MaxStoredStringBytes = 512
	// MaxRowBytes bounds a stored row's attributes and events, as JSON.
	MaxRowBytes = 8 << 10
	// MaxErrorBytes bounds one error message.
	MaxErrorBytes = 200
	// MaxErrors bounds the errors one row keeps.
	MaxErrors = 32
	// MaxRowsPerRun is how many observations one root run may store; later
	// ones are emitted as telemetry but not stored.
	MaxRowsPerRun = 2000
)

// Scope is what a block's expressions see besides `item`: the node's result
// as the workflow will see it, and the run context the workflow sent.
type Scope struct {
	Output   interface{}
	Inputs   map[string]interface{}
	Workflow *model.WorkflowContext
	Iter     *model.IterContext
	Node     model.ObservedNode
}

// Event is one span event the block produced. Attribute values are as stored:
// redacted and bounded.
type Event struct {
	Name       string                 `json:"name"`
	Attributes map[string]interface{} `json:"attributes,omitempty"`
}

// MetricPoint is one data point the block produced, before the metric
// attribute rules (MetricAttributeValue) and the cardinality caps (Recorder).
type MetricPoint struct {
	Name       string
	Kind       string
	Unit       string
	Value      float64
	Attributes map[string]interface{}
}

// Error is one expression that failed, or the budget that ran out. Message is
// for the author (the stored row), never for telemetry.
type Error struct {
	Kind    string // a telemetry.ObserveError* value
	Message string
}

// Result is what one block produced.
type Result struct {
	Attributes map[string]interface{}
	Events     []Event
	Metrics    []MetricPoint
	Errors     []Error
	// Truncated reports a cap that cut the block: a count, or the budget.
	Truncated bool
}

// HasRecord reports whether the result is worth a stored row: an attribute,
// an event, or an error the author needs to see. A block that only emits
// metrics, and evaluated cleanly, stores nothing.
func (r Result) HasRecord() bool {
	return len(r.Attributes) > 0 || len(r.Events) > 0 || len(r.Errors) > 0
}

// Evaluator evaluates blocks. The zero value is the production one.
type Evaluator struct {
	// Now is the clock the budget is measured on; nil is time.Now.
	Now func() time.Time
	// Budget is the block's evaluation budget; 0 is EvalBudget.
	Budget time.Duration
}

// Evaluate is Evaluator{}.Evaluate.
func Evaluate(block *reliantv1.NodeObserve, s Scope) Result {
	return Evaluator{}.Evaluate(block, s)
}

// Evaluate evaluates block over s. It never panics and never returns an
// error: what fails is in Result.Errors.
func (ev Evaluator) Evaluate(block *reliantv1.NodeObserve, s Scope) (res Result) {
	if block == nil {
		return Result{}
	}
	now := ev.Now
	if now == nil {
		now = time.Now
	}
	budget := ev.Budget
	if budget <= 0 {
		budget = EvalBudget
	}
	e := &evaluation{now: now, deadline: now().Add(budget), scope: s}
	defer func() {
		if r := recover(); r != nil {
			e.fail(telemetry.ObserveErrorPanic, "observe", fmt.Sprintf("evaluation panicked: %v", r))
			e.res.Truncated = true
		}
		res = e.res
	}()
	e.attributes(block.GetAttributes())
	e.events(block.GetEvents())
	e.metrics(block.GetMetrics())
	return e.res
}

type evaluation struct {
	now      func() time.Time
	deadline time.Time
	scope    Scope
	res      Result
	spent    bool
}

func (e *evaluation) context(inEach bool, item interface{}) *wfcel.ObserveContext {
	node := e.scope.Node
	return &wfcel.ObserveContext{
		Output:   e.scope.Output,
		Inputs:   e.scope.Inputs,
		Workflow: e.scope.Workflow,
		Iter:     e.scope.Iter,
		Node:     &node,
		InEach:   inEach,
		Item:     item,
	}
}

// outOfBudget reports, once, that the budget ran out; every later check says
// so without measuring again.
func (e *evaluation) outOfBudget() bool {
	if e.spent {
		return true
	}
	if e.now().After(e.deadline) {
		e.spent = true
		e.res.Truncated = true
		e.fail(telemetry.ObserveErrorBudget, "observe", fmt.Sprintf("evaluation took longer than its %s budget; the rest of the block was skipped", EvalBudget))
		return true
	}
	return false
}

func (e *evaluation) fail(kind, path, msg string) {
	if len(e.res.Errors) >= MaxErrors {
		e.res.Truncated = true
		return
	}
	text := cutString(logging.RedactText(path+": "+msg), MaxErrorBytes)
	e.res.Errors = append(e.res.Errors, Error{Kind: kind, Message: text})
}

// template evaluates one template; ok is false when it failed (and the
// failure is recorded) or the budget is spent.
func (e *evaluation) template(path, tmpl string, ctx *wfcel.ObserveContext) (v interface{}, ok bool) {
	if e.outOfBudget() {
		return nil, false
	}
	defer func() {
		if r := recover(); r != nil {
			e.fail(telemetry.ObserveErrorPanic, path, fmt.Sprintf("evaluation panicked: %v", r))
			v, ok = nil, false
		}
	}()
	v, err := wfcel.EvaluateTemplate(tmpl, ctx)
	if err != nil {
		e.fail(telemetry.ObserveErrorEval, path, err.Error())
		return nil, false
	}
	return v, true
}

// condition evaluates a raw-CEL when:. A failure is false.
func (e *evaluation) condition(path, expr string, ctx *wfcel.ObserveContext) (v bool) {
	if e.outOfBudget() {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			e.fail(telemetry.ObserveErrorPanic, path, fmt.Sprintf("evaluation panicked: %v", r))
			v = false
		}
	}()
	b, err := wfcel.EvaluateBool(expr, ctx)
	if err != nil {
		e.fail(telemetry.ObserveErrorEval, path, err.Error())
		return false
	}
	return b
}

func (e *evaluation) attributes(attrs map[string]string) {
	if len(attrs) == 0 {
		return
	}
	ctx := e.context(false, nil)
	out := e.attributeMap("attributes", attrs, model.ObserveMaxAttributes, ctx)
	if len(out) > 0 {
		e.res.Attributes = out
	}
}

// attributeMap evaluates a key -> template map in key order, at most limit
// keys.
func (e *evaluation) attributeMap(path string, attrs map[string]string, limit int, ctx *wfcel.ObserveContext) map[string]interface{} {
	keys := sortedKeys(attrs)
	if len(keys) > limit {
		keys = keys[:limit]
		e.res.Truncated = true
	}
	out := make(map[string]interface{}, len(keys))
	for _, k := range keys {
		if v, ok := e.template(path+"."+k, attrs[k], ctx); ok {
			out[k] = Storable(v)
		}
	}
	return out
}

func (e *evaluation) events(events []*reliantv1.ObserveEvent) {
	for i, ev := range events {
		path := fmt.Sprintf("events[%d]", i)
		if !e.roomForEvent() {
			return
		}
		ctx := e.context(false, nil)
		if ev.GetWhen() != "" && !e.condition(path+".when", ev.GetWhen(), ctx) {
			continue
		}
		if ev.GetEach() == "" {
			e.res.Events = append(e.res.Events, Event{
				Name:       ev.GetName(),
				Attributes: nonEmpty(e.attributeMap(path+".attributes", ev.GetAttributes(), model.ObserveMaxEventAttributes, ctx)),
			})
			continue
		}
		v, ok := e.template(path+".each", ev.GetEach(), ctx)
		if !ok {
			continue
		}
		items, isList := v.([]interface{})
		if !isList {
			e.fail(telemetry.ObserveErrorEval, path+".each", fmt.Sprintf("each must evaluate to a list, got %T", v))
			continue
		}
		if len(items) > MaxEachItems {
			items = items[:MaxEachItems]
			e.res.Truncated = true
		}
		for _, item := range items {
			if !e.roomForEvent() {
				return
			}
			e.res.Events = append(e.res.Events, Event{
				Name:       ev.GetName(),
				Attributes: nonEmpty(e.attributeMap(path+".attributes", ev.GetAttributes(), model.ObserveMaxEventAttributes, e.context(true, item))),
			})
		}
	}
}

// roomForEvent reports whether another event fits under the per-node cap,
// marking the result truncated when it does not.
func (e *evaluation) roomForEvent() bool {
	if len(e.res.Events) >= model.ObserveMaxEvents {
		e.res.Truncated = true
		return false
	}
	return true
}

func (e *evaluation) metrics(metrics []*reliantv1.ObserveMetric) {
	if len(metrics) > model.ObserveMaxMetrics {
		metrics = metrics[:model.ObserveMaxMetrics]
		e.res.Truncated = true
	}
	for i, m := range metrics {
		path := fmt.Sprintf("metrics[%d]", i)
		ctx := e.context(false, nil)
		if m.GetWhen() != "" && !e.condition(path+".when", m.GetWhen(), ctx) {
			continue
		}
		value := 1.0
		if m.GetValue() != "" {
			v, ok := e.template(path+".value", m.GetValue(), ctx)
			if !ok {
				continue
			}
			f, isNumber := toFloat(v)
			if !isNumber {
				e.fail(telemetry.ObserveErrorEval, path+".value", fmt.Sprintf("value must be a number, got %T", v))
				continue
			}
			value = f
		} else if m.GetKind() != model.ObserveKindCounter {
			e.fail(telemetry.ObserveErrorEval, path+".value", "a "+m.GetKind()+" needs a value")
			continue
		}
		if m.GetKind() == model.ObserveKindCounter && value < 0 {
			e.fail(telemetry.ObserveErrorEval, path+".value", "a counter cannot be decremented")
			continue
		}
		attrs := e.attributeMap(path+".attributes", m.GetAttributes(), model.ObserveMaxMetricAttributes, ctx)
		e.res.Metrics = append(e.res.Metrics, MetricPoint{
			Name:       m.GetName(),
			Kind:       m.GetKind(),
			Unit:       m.GetUnit(),
			Value:      value,
			Attributes: attrs,
		})
	}
}

// Storable is an evaluated value as it is stored: a bool or a number as it
// is, a string with credential shapes masked (logging.RedactText) and cut to
// MaxStoredStringBytes, and anything else (a list, a map) as that string of
// its JSON.
func Storable(v interface{}) interface{} {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case uint64:
		if t > math.MaxInt64 {
			return float64(t)
		}
		return int64(t)
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return fmt.Sprint(t) // JSON has no NaN or Inf
		}
		return t
	case string:
		return cutString(logging.RedactText(t), MaxStoredStringBytes)
	default:
		b, err := json.Marshal(t)
		text := string(b)
		if err != nil {
			text = fmt.Sprint(t)
		}
		return cutString(logging.RedactText(text), MaxStoredStringBytes)
	}
}

func toFloat(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case int64:
		return float64(t), true
	case int:
		return float64(t), true
	case int32:
		return float64(t), true
	case uint64:
		return float64(t), true
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return 0, false
		}
		return t, true
	default:
		return 0, false
	}
}

// cutString cuts s to limit bytes on a rune boundary and marks the cut.
func cutString(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return fmt.Sprintf("%s… [%d more bytes]", s[:cut], len(s)-cut)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func nonEmpty(m map[string]interface{}) map[string]interface{} {
	if len(m) == 0 {
		return nil
	}
	return m
}
