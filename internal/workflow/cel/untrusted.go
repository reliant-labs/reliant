// Copyright (c) 2025 Reliant Labs

package wfcel

import (
	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"

	"github.com/reliant-labs/reliant/internal/untrusted"
)

// celUntrustedFunctions are untrustedText and untrustedJson: how a workflow puts
// text an outside party can write (an error message, an alert label, a webhook
// body) in front of a model.
//
//	untrustedText(s)        one plain line, control and invisible characters
//	                        dropped, backticks and angle brackets neutralized, at
//	                        most 400 runes (for a title or a chat line)
//	untrustedText(s, n)     the same, at most n runes
//	untrustedJson(v)        v as compact JSON: every string treated as above,
//	                        keys sorted, lists and nesting bounded, <, > and &
//	                        escaped so a value cannot close the tag it sits in
//	untrustedJson(v, n)     the same, strings of at most n runes
//
// A template that interpolates such text without them has handed the author of
// the text the prompt. See internal/untrusted.
func celUntrustedFunctions() cel.EnvOption {
	text := func(val ref.Val, max int) ref.Val {
		s, ok := val.Value().(string)
		if !ok {
			return types.NewErr("untrustedText() requires a string, got %s", val.Type().TypeName())
		}
		return types.String(untrusted.Line(s, max))
	}
	jsonOf := func(val ref.Val, max int) ref.Val {
		native, err := celValueToJSON(val)
		if err != nil {
			return types.NewErr("untrustedJson() failed to convert value: %v", err)
		}
		out, err := untrusted.JSON(native, max)
		if err != nil {
			return types.NewErr("untrustedJson() failed to render value: %v", err)
		}
		return types.String(out)
	}
	limit := func(val ref.Val) (int, ref.Val) {
		n, ok := val.Value().(int64)
		if !ok || n < 1 || n > 100_000 {
			return 0, types.NewErr("the length limit must be an int between 1 and 100000")
		}
		return int(n), nil
	}

	return cel.Lib(untrustedLib{funcs: []cel.EnvOption{
		cel.Function("untrustedText",
			cel.Overload("untrusted_text_string", []*cel.Type{cel.StringType}, cel.StringType,
				cel.UnaryBinding(func(v ref.Val) ref.Val { return text(v, 0) })),
			cel.Overload("untrusted_text_string_int", []*cel.Type{cel.StringType, cel.IntType}, cel.StringType,
				cel.BinaryBinding(func(v, n ref.Val) ref.Val {
					max, bad := limit(n)
					if bad != nil {
						return bad
					}
					return text(v, max)
				})),
		),
		cel.Function("untrustedJson",
			cel.Overload("untrusted_json_dyn", []*cel.Type{cel.DynType}, cel.StringType,
				cel.UnaryBinding(func(v ref.Val) ref.Val { return jsonOf(v, 0) })),
			cel.Overload("untrusted_json_dyn_int", []*cel.Type{cel.DynType, cel.IntType}, cel.StringType,
				cel.BinaryBinding(func(v, n ref.Val) ref.Val {
					max, bad := limit(n)
					if bad != nil {
						return bad
					}
					return jsonOf(v, max)
				})),
		),
	}})
}

// untrustedLib bundles the functions as one cel.EnvOption.
type untrustedLib struct{ funcs []cel.EnvOption }

func (l untrustedLib) LibraryName() string { return "reliant.untrusted" }

func (l untrustedLib) CompileOptions() []cel.EnvOption { return l.funcs }

func (untrustedLib) ProgramOptions() []cel.ProgramOption { return nil }
