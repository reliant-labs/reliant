// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/invopop/jsonschema"
	rctxpkg "github.com/reliant-labs/reliant/internal/rctx"
)

// A tool has ONE parameter schema. Every parameter in it is either BOUND — a
// human fixed it ahead of time — or OPEN, filled at runtime by the model.
//
// Binding a parameter REMOVES it from the LLM-visible schema, and execution
// merges the bound value back in. A call that sets a bound parameter anyway
// — to anything but its bound value — is refused, never run with either value
// (checkBoundKeys). So the model cannot override a parameter it was never
// shown, and what runs is always what the model asked for.
//
// Hiding happens in call_llm, on the tool instance whose schema the driver
// sends. Merging happens where the call runs: execute_tools applies the
// bindings the turn's capability set recorded (ApplyBindings) before dispatch,
// because the executor — on the worker or on the daemon — builds a fresh,
// unbound tool for every call. A bound tool's own Run applies the same rule,
// for a caller that runs it directly.
//
// A tool must be fully functional with ZERO bindings. Binding is refinement,
// never activation.

// ErrBindingsUnsupported is returned by BindTool when bindings are requested
// for a tool that cannot accept them — an MCP tool, whose schema comes from a
// remote server and has no local struct behind it, or any other hand-rolled
// Tool implementation. It is an error rather than a silent no-op because a
// human who configured a parameter deserves to hear that it will not take
// effect.
var ErrBindingsUnsupported = errors.New("tool does not support parameter bindings")

// ErrUnknownBoundParam is returned when a binding names a parameter the tool's
// schema does not declare. Wrapped, so callers can distinguish a typo in
// configuration from a structurally unbindable tool.
var ErrUnknownBoundParam = errors.New("tool has no such parameter")

// BoundParamSetError is a call that set bound parameters to values other than
// the ones they are bound to. Its text is written for the model, which was
// never shown these parameters, and never repeats a bound value: a binding
// can carry a secret.
type BoundParamSetError struct {
	Tool   string
	Params []string // sorted
}

func (e *BoundParamSetError) Error() string {
	quoted := make([]string, len(e.Params))
	for i, name := range e.Params {
		quoted[i] = "'" + name + "'"
	}
	if len(quoted) == 1 {
		return fmt.Sprintf("Parameter %s of '%s' is fixed for this agent and cannot be set, so the call was not run. Call '%s' again without %s.",
			quoted[0], e.Tool, e.Tool, quoted[0])
	}
	return fmt.Sprintf("Parameters %s of '%s' are fixed for this agent and cannot be set, so the call was not run. Call '%s' again without them.",
		strings.Join(quoted, ", "), e.Tool, e.Tool)
}

// BoundValue is one bound parameter's value. It is deliberately not a bare
// `any`: a binding is not necessarily a constant. `save_to` bound to
// "assets/{{nodes.plan.slug}}.png" is fixed from the model's point of view —
// it cannot set it — while still being resolved per call.
//
// Exactly one of the two forms is in use. Expr wins when non-empty.
type BoundValue struct {
	// Literal is a constant value, JSON-encodable, merged in as-is.
	Literal any `json:"literal,omitempty"`
	// Expr is an expression evaluated at call time by a BindingResolver.
	// Structurally expressible today; evaluation is wired separately.
	Expr string `json:"expr,omitempty"`
}

// LiteralBinding binds a parameter to a constant.
func LiteralBinding(value any) BoundValue { return BoundValue{Literal: value} }

// ExprBinding binds a parameter to an expression resolved at call time.
func ExprBinding(expr string) BoundValue { return BoundValue{Expr: expr} }

// IsExpr reports whether this binding needs a resolver.
func (b BoundValue) IsExpr() bool { return b.Expr != "" }

// Bindings maps a top-level parameter name — the JSON key as it appears in the
// tool's schema — to its bound value.
type Bindings map[string]BoundValue

// Names returns the bound parameter names in a stable order, for logging and
// for deterministic error messages.
func (b Bindings) Names() []string {
	names := make([]string, 0, len(b))
	for name := range b {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Merge returns a new Bindings with over layered on top of b. A key present in
// both takes its value from over — "more specific wins", which is what the
// scope-resolution order (tool default → global → workflow → preset) needs.
// Neither receiver nor argument is mutated.
func (b Bindings) Merge(over Bindings) Bindings {
	if len(b) == 0 && len(over) == 0 {
		return nil
	}
	merged := make(Bindings, len(b)+len(over))
	for name, value := range b {
		merged[name] = value
	}
	for name, value := range over {
		merged[name] = value
	}
	return merged
}

// BindingResolver evaluates expression-valued bindings against a live call.
// Declared here, at the consumer, so the expression engine stays out of this
// package. A tool with no resolver and no expression bindings — the common
// case — never touches this.
type BindingResolver interface {
	ResolveBinding(tc *rctxpkg.ToolContext, expr string) (any, error)
}

// DefaultBindingsProvider is an optional interface a tool implementation can
// satisfy to declare the bindings that apply when nothing else binds the
// parameter. Prefer a STRATEGY to a pinned value: generate_image's model
// default is a tag selector, which re-resolves as the model registry changes,
// rather than a concrete model id that goes stale the day it is retired.
type DefaultBindingsProvider interface {
	DefaultBindings() Bindings
}

// BindableTool is the optional interface a Tool implements when its parameters
// can be bound. It is optional — rather than a method on Tool — so that the
// three hand-rolled implementations (ResponseTool, SchemaOnlyTool,
// MCPToolAdapter) need no change and cannot be broken by this. Same shape as
// the existing ReadOnlyTool escape hatch.
type BindableTool interface {
	Tool
	// WithBindings returns a COPY of the tool carrying bindings layered over
	// its declared defaults. The receiver is unchanged, because tool
	// instances are shared across the factory and the registry.
	WithBindings(bindings Bindings) (Tool, error)
	// Bindings returns the bindings currently in effect, defaults included.
	Bindings() Bindings
}

// BindTool applies bindings to a tool if it supports them. This is the seam
// every configuration source goes through.
//
// Zero bindings is always valid and always returns the tool untouched — the
// unconfigured path must never depend on this function succeeding. A non-empty
// binding set against a non-bindable tool returns ErrBindingsUnsupported along
// with the original tool, so a caller that chooses to continue still has a
// working tool.
func BindTool(tool Tool, bindings Bindings) (Tool, error) {
	if tool == nil || len(bindings) == 0 {
		return tool, nil
	}
	bindable, ok := tool.(BindableTool)
	if !ok {
		return tool, fmt.Errorf("%s: %w", tool.Name(), ErrBindingsUnsupported)
	}
	return bindable.WithBindings(bindings)
}

// withoutBoundParams returns the model-facing view of a schema: every bound
// parameter removed from both the property list and the required list.
//
// Dropping it from `required` is not cosmetic. Leaving it there asks the model
// for a parameter it cannot see, which providers with strict schema validation
// reject outright and lenient ones answer by inventing a value.
//
// The schema is safe to mutate in place because both producers of it —
// jsonschema.Reflector.Reflect and ResolveSchemaRefs — build a fresh object on
// every call.
func withoutBoundParams(schema *jsonschema.Schema, bindings Bindings) *jsonschema.Schema {
	if schema == nil || len(bindings) == 0 {
		return schema
	}

	if schema.Properties != nil {
		for name := range bindings {
			schema.Properties.Delete(name)
		}
	}

	if len(schema.Required) > 0 {
		open := make([]string, 0, len(schema.Required))
		for _, name := range schema.Required {
			if _, bound := bindings[name]; bound {
				continue
			}
			open = append(open, name)
		}
		if len(open) == 0 {
			schema.Required = nil
		} else {
			schema.Required = open
		}
	}

	return schema
}

// resolveBindings turns the bound values into a plain JSON-shaped map for one
// call. Literals pass through; expressions go to the resolver.
func resolveBindings(bindings Bindings, resolver BindingResolver, tc *rctxpkg.ToolContext) (map[string]any, error) {
	if len(bindings) == 0 {
		return nil, nil
	}
	resolved := make(map[string]any, len(bindings))
	for _, name := range bindings.Names() {
		bound := bindings[name]
		if !bound.IsExpr() {
			resolved[name] = bound.Literal
			continue
		}
		if resolver == nil {
			return nil, fmt.Errorf("parameter %q is bound to the expression %q but no binding resolver is configured", name, bound.Expr)
		}
		value, err := resolver.ResolveBinding(tc, bound.Expr)
		if err != nil {
			return nil, fmt.Errorf("resolving binding for parameter %q: %w", name, err)
		}
		resolved[name] = value
	}
	return resolved, nil
}

// ApplyBindings is a bound parameter's execution half: the model's call input
// with every bound value merged in, or a *BoundParamSetError when the call
// set a bound parameter to anything else. execute_tools runs it before
// dispatch, so the executor — which builds a fresh, unbound tool — receives a
// complete input on every path, the daemon's included, whatever its version.
//
// There is no expression engine at execution, so a parameter bound to an
// expression fails the call here exactly as it does in a bound tool's Run.
func ApplyBindings(toolName, input string, bindings Bindings) (string, error) {
	if len(bindings) == 0 {
		return input, nil
	}
	resolved, err := resolveBindings(bindings, nil, nil)
	if err != nil {
		return "", err
	}
	stripped, err := checkBoundKeys(toolName, input, resolved)
	if err != nil {
		return "", err
	}
	return applyBindingsToInput(stripped, resolved)
}

// applyBindingsToInput merges the bound values into the input. Bound wins:
// checkBoundKeys has already refused a call whose own value for a bound
// parameter differs, so anything it left in place is the bound value itself.
func applyBindingsToInput(input string, resolved map[string]any) (string, error) {
	if len(resolved) == 0 {
		return input, nil
	}

	fields := map[string]any{}
	if trimmed := trimJSONWhitespace(input); trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
			// Not an object — nothing to merge into, and the decoder
			// downstream will report the real problem with better context.
			return input, nil
		}
	}

	for name, value := range resolved {
		fields[name] = value
	}

	merged, err := json.Marshal(fields)
	if err != nil {
		return input, fmt.Errorf("merging bound parameters: %w", err)
	}
	return string(merged), nil
}

// checkBoundKeys is the one rule for a bound parameter the model set anyway.
// Set to its bound value, the key is redundant and is dropped. Set to anything
// else, the call is refused with a *BoundParamSetError naming every such
// parameter — it is not run with the model's value, and not silently run with
// the bound one either. The model was never shown the parameter, so a value
// for it is a hallucination or an injected instruction, and the rest of the
// call was composed around it: running it against the bound value would run
// a call nobody wrote, then report it to the model as the call it made.
//
// Dropping the redundant key matters because the model-facing schema sets
// additionalProperties:false, so a bound tool's own validation would reject
// the key even when its value is right.
//
// Input that is not a JSON object is returned as-is: there is nothing to
// check, and the decoder downstream reports the real problem.
func checkBoundKeys(toolName, input string, resolved map[string]any) (string, error) {
	if len(resolved) == 0 {
		return input, nil
	}
	trimmed := trimJSONWhitespace(input)
	if trimmed == "" {
		return input, nil
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		return input, nil
	}
	var conflicting []string
	removed := false
	for name, bound := range resolved {
		supplied, present := fields[name]
		if !present {
			continue
		}
		if !sameJSONValue(supplied, bound) {
			conflicting = append(conflicting, name)
			continue
		}
		delete(fields, name)
		removed = true
	}
	if len(conflicting) > 0 {
		sort.Strings(conflicting)
		return "", &BoundParamSetError{Tool: toolName, Params: conflicting}
	}
	if !removed {
		return input, nil
	}
	stripped, err := json.Marshal(fields)
	if err != nil {
		return input, nil
	}
	return string(stripped), nil
}

// sameJSONValue compares a value decoded from the model's JSON with a bound
// value, which may be any Go value a binding was written with (an int, a
// struct). Both are taken through JSON so 3 and 3.0 are one value.
func sameJSONValue(supplied, bound any) bool {
	normalize := func(v any) (any, bool) {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		var out any
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, false
		}
		return out, true
	}
	a, okA := normalize(supplied)
	b, okB := normalize(bound)
	return okA && okB && reflect.DeepEqual(a, b)
}

func trimJSONWhitespace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t' || s[start] == '\n' || s[start] == '\r') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\n' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
