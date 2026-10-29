// Package tmpl renders the {{ expr }} templates used by integration manifests.
// Expressions are CEL, the engine's expression language, over the variables
// params, connection (the resolved connection's non-secret params, as
// connection.params.<name>), response (parsed JSON body), raw (body text),
// status, headers and parent (a fan-out request's item). Beyond standard CEL they have optional types,
// base64.decode / base64.encode, <bytes>.text(), cel.bind and
// <string>.lineWindow(start, count, budget).
package tmpl

import (
	"bytes"
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/ext"
	"google.golang.org/protobuf/types/known/structpb"
)

// Options tunes rendering.
type Options struct {
	// EscapePath percent-escapes every interpolated value (for URL path segments).
	EscapePath bool
}

type segment struct {
	literal string
	expr    string
	isExpr  bool
}

var (
	envOnce sync.Once
	env     *cel.Env
	envErr  error

	// custom caches the environments built for other variable sets.
	custom sync.Map // string -> *cel.Env
)

// envOptions are what every expression environment has, beyond its variables.
func envOptions() []cel.EnvOption {
	return []cel.EnvOption{
		// Optional syntax: `{?"body": params.?body}` leaves a key out
		// when the param is absent (a PATCH must not send null), and
		// `headers[?'retry-after']` reads a header that may be missing.
		cel.OptionalTypes(),
		// base64.decode / base64.encode: providers that return file
		// content (GitHub's contents API) send it base64-encoded.
		ext.Encoders(),
		bytesText,
		// cel.bind: name a value once (a decoded file) and use it in
		// several fields of the output.
		ext.Bindings(),
		// <string>.lineWindow(start, count, budget): serve a large file
		// one window of lines at a time (lines.go).
		lineWindowFn,
	}
}

func celEnv() (*cel.Env, error) {
	envOnce.Do(func() {
		opts := []cel.EnvOption{
			cel.Variable("params", cel.DynType),
			cel.Variable("connection", cel.DynType),
			cel.Variable("response", cel.DynType),
			cel.Variable("status", cel.DynType),
			cel.Variable("headers", cel.DynType),
			cel.Variable("raw", cel.StringType),
			// parent is the item a fan-out request is running for; null
			// everywhere else.
			cel.Variable("parent", cel.DynType),
		}
		env, envErr = cel.NewEnv(append(opts, envOptions()...)...)
	})
	return env, envErr
}

// envFor is the environment over exactly the named variables, all dynamic.
func envFor(names []string) (*cel.Env, error) {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	key := strings.Join(sorted, ",")
	if e, ok := custom.Load(key); ok {
		return e.(*cel.Env), nil
	}
	// split, replace, lowerAscii...: a typed value is often taken apart into
	// pieces (`owner/name`). Action request templates do not get these.
	opts := make([]cel.EnvOption, 0, len(sorted)+5)
	opts = append(opts, ext.Strings())
	for _, n := range sorted {
		opts = append(opts, cel.Variable(n, cel.DynType))
	}
	e, err := cel.NewEnv(append(opts, envOptions()...)...)
	if err != nil {
		return nil, err
	}
	custom.Store(key, e)
	return e, nil
}

// bytesText is `<bytes>.text()`: the bytes as a string when they are text,
// else optional.none(). "Text" is valid UTF-8 with no NUL byte — git's own
// test for binary content — so `base64.decode(response.content).text()` turns
// a source file into its text and an image into an absent value, where
// string(<bytes>) would fail the whole expression on the first invalid byte.
//
// The optional is of dyn, not string, so `.text().orValue(null)` type-checks
// the way `response.?x.orValue(null)` does everywhere else in a manifest.
var bytesText = cel.Function("text",
	cel.MemberOverload("bytes_text", []*cel.Type{cel.BytesType}, cel.OptionalType(cel.DynType),
		cel.UnaryBinding(func(v ref.Val) ref.Val {
			b, ok := v.(types.Bytes)
			if !ok {
				return types.MaybeNoSuchOverloadErr(v)
			}
			if !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
				return types.OptionalNone
			}
			return types.OptionalOf(types.String(b))
		})))

// HasExpr reports whether s contains a {{ }} expression.
func HasExpr(s string) bool { return strings.Contains(s, "{{") }

func split(s string) ([]segment, error) {
	var segs []segment
	for len(s) > 0 {
		open := strings.Index(s, "{{")
		if open < 0 {
			segs = append(segs, segment{literal: s})
			break
		}
		if open > 0 {
			segs = append(segs, segment{literal: s[:open]})
		}
		rest := s[open+2:]
		closeIdx := strings.Index(rest, "}}")
		if closeIdx < 0 {
			return nil, fmt.Errorf("unterminated {{ in %q", s)
		}
		expr := strings.TrimSpace(rest[:closeIdx])
		if expr == "" {
			return nil, fmt.Errorf("empty {{ }} expression")
		}
		segs = append(segs, segment{expr: expr, isExpr: true})
		s = rest[closeIdx+2:]
	}
	return segs, nil
}

func compile(expr string) (cel.Program, error) {
	e, err := celEnv()
	if err != nil {
		return nil, err
	}
	ast, iss := e.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("expression %q: %w", expr, iss.Err())
	}
	return e.Program(ast)
}

// Validate checks that every {{ }} in s compiles.
func Validate(s string) error {
	segs, err := split(s)
	if err != nil {
		return err
	}
	for _, sg := range segs {
		if sg.isExpr {
			if _, err := compile(sg.expr); err != nil {
				return err
			}
		}
	}
	return nil
}

// ValidateExpr checks a bare CEL expression.
func ValidateExpr(expr string) error {
	_, err := compile(expr)
	return err
}

// EvalExpr evaluates a bare CEL expression.
func EvalExpr(expr string, vars map[string]any) (any, error) {
	prg, err := compile(expr)
	if err != nil {
		return nil, err
	}
	return eval(prg, expr, vars)
}

func eval(prg cel.Program, expr string, vars map[string]any) (any, error) {
	full := map[string]any{"params": map[string]any{}, "connection": map[string]any{"params": map[string]any{}}, "response": nil, "status": int64(0), "headers": map[string]any{}, "raw": "", "parent": nil}
	for k, v := range vars {
		full[k] = v
	}
	val, _, err := prg.Eval(full)
	if err != nil {
		return nil, fmt.Errorf("evaluating %q: %w", expr, err)
	}
	native, err := val.ConvertToNative(reflect.TypeOf((*structpb.Value)(nil)))
	if err != nil {
		return nil, fmt.Errorf("evaluating %q: %w", expr, err)
	}
	return native.(*structpb.Value).AsInterface(), nil
}

// Render interpolates s. A string that is exactly one expression keeps the
// expression's native type; anything else is stringified.
func Render(s string, vars map[string]any, opts Options) (any, error) {
	segs, err := split(s)
	if err != nil {
		return nil, err
	}
	if len(segs) == 1 && segs[0].isExpr && !opts.EscapePath {
		prg, err := compile(segs[0].expr)
		if err != nil {
			return nil, err
		}
		return eval(prg, segs[0].expr, vars)
	}
	var b strings.Builder
	for _, sg := range segs {
		if !sg.isExpr {
			b.WriteString(sg.literal)
			continue
		}
		prg, err := compile(sg.expr)
		if err != nil {
			return nil, err
		}
		v, err := eval(prg, sg.expr, vars)
		if err != nil {
			return nil, err
		}
		text := stringify(v)
		if opts.EscapePath {
			text = url.PathEscape(text)
		}
		b.WriteString(text)
	}
	return b.String(), nil
}

// RenderString is Render for callers that need text.
func RenderString(s string, vars map[string]any, opts Options) (string, error) {
	v, err := Render(s, vars, opts)
	if err != nil {
		return "", err
	}
	return stringify(v), nil
}

func stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%t", t)
	default:
		b, err := jsonMarshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// The functions below are for expressions that do not run against an HTTP
// response: a type's option projections read `item`, `scope`, `context` and
// so on, never `response`. The language is the same; only the variables
// differ, and every one is dynamic.

// Exprs returns the {{ }} expressions of a template, in order.
func Exprs(s string) ([]string, error) {
	segs, err := split(s)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, sg := range segs {
		if sg.isExpr {
			out = append(out, sg.expr)
		}
	}
	return out, nil
}

// Compile type-checks expr as an expression over the named variables and
// returns its checked AST, so a caller can inspect what it reads.
func Compile(expr string, names ...string) (*cel.Ast, error) {
	e, err := envFor(names)
	if err != nil {
		return nil, err
	}
	ast, iss := e.Compile(expr)
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("expression %q: %w", expr, iss.Err())
	}
	return ast, nil
}

// EvalWith evaluates a bare expression over vars; the variable names are
// vars' keys.
func EvalWith(expr string, vars map[string]any) (any, error) {
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	ast, err := Compile(expr, names...)
	if err != nil {
		return nil, err
	}
	e, _ := envFor(names)
	prg, err := e.Program(ast)
	if err != nil {
		return nil, err
	}
	return eval(prg, expr, vars)
}

// RenderWith is Render over vars (no response, no path escaping). A string
// that is exactly one expression keeps its native type.
func RenderWith(s string, vars map[string]any) (any, error) {
	segs, err := split(s)
	if err != nil {
		return nil, err
	}
	if len(segs) == 1 && segs[0].isExpr {
		return EvalWith(segs[0].expr, vars)
	}
	var b strings.Builder
	for _, sg := range segs {
		if !sg.isExpr {
			b.WriteString(sg.literal)
			continue
		}
		v, err := EvalWith(sg.expr, vars)
		if err != nil {
			return nil, err
		}
		b.WriteString(stringify(v))
	}
	return b.String(), nil
}

// Prepare compiles expr once, over the named variables, for a caller that
// evaluates it over many inputs (one projection over every item of a list).
// The returned function takes values for the variables.
func Prepare(expr string, names ...string) (func(vars map[string]any) (any, error), error) {
	ast, err := Compile(expr, names...)
	if err != nil {
		return nil, err
	}
	e, err := envFor(names)
	if err != nil {
		return nil, err
	}
	prg, err := e.Program(ast)
	if err != nil {
		return nil, err
	}
	return func(vars map[string]any) (any, error) { return eval(prg, expr, vars) }, nil
}
