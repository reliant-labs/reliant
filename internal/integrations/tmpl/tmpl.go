// Package tmpl renders the {{ expr }} templates used by integration manifests.
// Expressions are CEL, the engine's expression language, over the variables
// params, connection (the resolved connection's non-secret params, as
// connection.params.<name>), response (parsed JSON body), raw (body text),
// status and headers.
package tmpl

import (
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
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
)

func celEnv() (*cel.Env, error) {
	envOnce.Do(func() {
		env, envErr = cel.NewEnv(
			cel.Variable("params", cel.DynType),
			cel.Variable("connection", cel.DynType),
			cel.Variable("response", cel.DynType),
			cel.Variable("status", cel.DynType),
			cel.Variable("headers", cel.DynType),
			cel.Variable("raw", cel.StringType),
		)
	})
	return env, envErr
}

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
	full := map[string]any{"params": map[string]any{}, "connection": map[string]any{"params": map[string]any{}}, "response": nil, "status": int64(0), "headers": map[string]any{}, "raw": ""}
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
