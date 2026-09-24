// forge:exclude-contract
//
// Temporal workflow/activity code. The exported functions are registered with
// the Temporal SDK by name and invoked by the runtime, not through a Go
// interface a caller could substitute. Determinism constraints, not an
// interface, define this boundary.
package wfcel

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/env"
	"github.com/google/cel-go/common/overloads"
	"github.com/google/cel-go/common/types"
	"github.com/google/cel-go/common/types/ref"
	"github.com/google/cel-go/common/types/traits"
	"github.com/google/cel-go/ext"
	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// =============================================================================
// CEL ENVIRONMENT CONFIGURATION
// =============================================================================

// CELEnvConfig specifies which namespaces and features to include in the CEL environment.
type CELEnvConfig struct {
	// Namespaces to include in the environment
	Namespaces []CELNamespace

	// IncludeStdLib includes the CEL standard library (true by default)
	IncludeStdLib bool

	// IncludeCustomFunctions includes parseJson(), toJson(), coalesce(), etc.
	IncludeCustomFunctions bool
}

// =============================================================================
// PRESET CONFIGURATIONS
// =============================================================================

// DefaultCELEnvConfig returns the standard config for workflow template evaluation.
// Includes: inputs, workflow, nodes, iter
func DefaultCELEnvConfig() CELEnvConfig {
	return CELEnvConfig{
		Namespaces: []CELNamespace{
			CELInputs,
			CELWorkflow,
			CELNodes,
			CELIter,
		},
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	}
}

// SaveMessageCELEnvConfig returns config for save_message expression evaluation.
// Includes: output, inputs, workflow, iter. `nodes` is excluded on purpose — see
// PostActivityContext.
func SaveMessageCELEnvConfig() CELEnvConfig {
	return CELEnvConfig{
		Namespaces: []CELNamespace{
			CELOutput,
			CELInputs,
			CELWorkflow,
			CELIter,
		},
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	}
}

// LoopWhileCELEnvConfig returns config for loop while expression evaluation.
// Includes: outputs, iter, inputs (for iteration limits via inputs.max_turns etc.),
// and nodes (for while conditions that reference parent node outputs).
//
// Custom functions are included: the runtime evaluates `while` through
// evaluateRaw, which always has them, so the preset must describe the same
// function set or it would type-check a different language than runs.
func LoopWhileCELEnvConfig() CELEnvConfig {
	return CELEnvConfig{
		Namespaces: []CELNamespace{
			CELOutputs,
			CELIter,
			CELInputs,
			CELNodes,
		},
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	}
}

// TemplateResolutionCELEnvConfig returns config for initial template resolution.
// Includes inputs and workflow namespaces.
func TemplateResolutionCELEnvConfig() CELEnvConfig {
	return CELEnvConfig{
		Namespaces: []CELNamespace{
			CELInputs,
			CELWorkflow,
		},
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	}
}

// EdgeConditionCELEnvConfig returns config for edge condition evaluation.
// Includes all namespaces that might be referenced in conditions.
func EdgeConditionCELEnvConfig() CELEnvConfig {
	return CELEnvConfig{
		Namespaces: []CELNamespace{
			CELInputs,
			CELWorkflow,
			CELNodes,
			CELIter,
			CELOutputs,
		},
		IncludeStdLib:          true,
		IncludeCustomFunctions: true,
	}
}

// =============================================================================
// CEL ENVIRONMENT FACTORY
// =============================================================================

// NewEnv creates a CEL environment with the specified configuration.
// Uses native Go types for compile-time field validation.
func NewEnv(config CELEnvConfig) (*cel.Env, error) {
	var opts []cel.EnvOption

	if config.IncludeStdLib {
		opts = append(opts, StdLib())
	}

	// Enable OptionalTypes for ?. syntax (optional field access on potentially-skipped nodes)
	opts = append(opts, cel.OptionalTypes())

	// Enable cross-type numeric comparisons (e.g., int > double)
	opts = append(opts, cel.CrossTypeNumericComparisons(true))

	// Register context types with CEL for native field validation.
	// Uses json tags for field names via ext.ParseStructTag("json").
	opts = append(opts, ext.NativeTypes(
		ext.ParseStructTag("json"),
		reflect.TypeOf(&model.WorkflowContext{}),
		reflect.TypeOf(&model.IterContext{}),
	))

	// Add namespace variable declarations
	for _, ns := range config.Namespaces {
		opts = append(opts, getNamespaceDecl(ns))
	}

	// Enable extended string functions (trimPrefix, trimSuffix, replace, split, join, format, etc.)
	opts = append(opts, ext.Strings())

	// Add custom functions
	if config.IncludeCustomFunctions {
		opts = append(opts, CustomFunctions()...)
	}

	// NewCustomEnv, not NewEnv: cel.NewEnv always starts from cel-go's full
	// stdlib, which would reintroduce the singleton size() that StdLib replaces.
	return cel.NewCustomEnv(opts...)
}

// EnsureNamespaceDefaults ensures all required namespaces have at least defaults.
// Typed namespaces (workflow, iter) get empty struct instances; dynamic namespaces get empty maps.
// This prevents "no such key" errors when evaluating CEL expressions.
func EnsureNamespaceDefaults(ctx map[string]interface{}, namespaces []CELNamespace) map[string]interface{} {
	result := make(map[string]interface{})
	for k, v := range ctx {
		result[k] = v
	}
	for _, ns := range namespaces {
		if _, ok := result[string(ns)]; !ok {
			result[string(ns)] = namespaceDefault(ns)
		}
	}
	return result
}

// namespaceDefault returns the appropriate default value for a namespace.
// Typed namespaces get empty struct pointers; dynamic namespaces get empty maps.
func namespaceDefault(ns CELNamespace) interface{} {
	switch ns {
	case CELWorkflow:
		return &model.WorkflowContext{}
	case CELIter:
		return map[string]interface{}{"iteration": 0, "index": 0}
	default:
		return make(map[string]interface{})
	}
}

// getNamespaceDecl returns the appropriate CEL declaration for a namespace.
// Typed namespaces (workflow, iter) use ObjectType for compile-time field validation.
// Dynamic namespaces (inputs, nodes, output, outputs) use DynType.
func getNamespaceDecl(ns CELNamespace) cel.EnvOption {
	switch ns {
	case CELWorkflow:
		return cel.Variable(string(ns), cel.ObjectType("model.WorkflowContext"))
	case CELIter:
		return cel.Variable(string(ns), cel.DynType)
	default:
		// Dynamic namespaces (inputs, nodes, output, outputs)
		return cel.Variable(string(ns), cel.DynType)
	}
}

// =============================================================================
// STANDARD LIBRARY
// =============================================================================

// StdLib is the CEL standard library as every workflow evaluation site sees it:
// cel-go's stdlib with ONE deliberate change — size() of null is 0.
//
// A workflow reads a lot of values that are legitimately null: an unset
// response_data, an optional response-tool field the model left out, a
// coalesce() that fell through. `size(x) > 0` over those failed the step with
// "no such overload: size", so every author had to write
// `x != null && size(x) > 0`. Treating null as empty removes that class of
// failure without making anything else lenient: `null + 'x'`, `null > 1` and
// friends still fail, and size() of an int is still a type error.
//
// Only null is forgiven. An ABSENT key (`size(inputs.nope)`, `size(m.missing)`)
// still fails with "no such key" — the error is raised by the field access,
// before size() is ever called. Guard those with has() or `?.`.
//
// It is tied to the stdlib rather than to CustomFunctions so that every
// environment that has size() at all gets the same semantics, including the
// loop-while environment, which carries no custom functions.
//
// cel-go binds size() as a single "singleton" implementation, and a singleton
// cannot be combined with added overloads, so the function is excluded from the
// stdlib and redeclared whole with per-overload bindings. Because cel.NewEnv
// always pre-loads the full stdlib, an environment using this must be built
// with cel.NewCustomEnv.
func StdLib() cel.EnvOption {
	return cel.Lib(stdLibrary{})
}

type stdLibrary struct{}

func (stdLibrary) CompileOptions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.StdLib(cel.StdLibSubset(&env.LibrarySubset{
			ExcludeFunctions: []*env.Function{env.NewFunction(overloads.Size)},
		})),
		nullSafeSizeFunction(),
	}
}

func (stdLibrary) ProgramOptions() []cel.ProgramOption { return nil }

// nullSafeSizeFunction declares size() with the stdlib's overloads plus a null
// overload (global and receiver form) that returns 0.
func nullSafeSizeFunction() cel.EnvOption {
	sizeOf := cel.UnaryBinding(func(val ref.Val) ref.Val {
		sizer, ok := val.(traits.Sizer)
		if !ok {
			return types.NoSuchOverloadErr()
		}
		return sizer.Size()
	})
	sizeOfNull := cel.UnaryBinding(func(ref.Val) ref.Val { return types.IntZero })
	listOfA := cel.ListType(cel.TypeParamType("A"))
	mapOfAB := cel.MapType(cel.TypeParamType("A"), cel.TypeParamType("B"))

	return cel.Function(overloads.Size,
		cel.Overload(overloads.SizeString, []*cel.Type{cel.StringType}, cel.IntType, sizeOf),
		cel.MemberOverload(overloads.SizeStringInst, []*cel.Type{cel.StringType}, cel.IntType, sizeOf),
		cel.Overload(overloads.SizeBytes, []*cel.Type{cel.BytesType}, cel.IntType, sizeOf),
		cel.MemberOverload(overloads.SizeBytesInst, []*cel.Type{cel.BytesType}, cel.IntType, sizeOf),
		cel.Overload(overloads.SizeList, []*cel.Type{listOfA}, cel.IntType, sizeOf),
		cel.MemberOverload(overloads.SizeListInst, []*cel.Type{listOfA}, cel.IntType, sizeOf),
		cel.Overload(overloads.SizeMap, []*cel.Type{mapOfAB}, cel.IntType, sizeOf),
		cel.MemberOverload(overloads.SizeMapInst, []*cel.Type{mapOfAB}, cel.IntType, sizeOf),
		cel.Overload("size_null", []*cel.Type{cel.NullType}, cel.IntType, sizeOfNull),
		cel.MemberOverload("null_size", []*cel.Type{cel.NullType}, cel.IntType, sizeOfNull),
	)
}

// =============================================================================
// CUSTOM CEL FUNCTIONS
// =============================================================================

// CustomFunctions returns all custom CEL functions we provide.
func CustomFunctions() []cel.EnvOption {
	return []cel.EnvOption{
		celParseJsonFunction(),
		celToJsonFunction(),
		celCoalesceFunction(),
		celGetOrDefaultFunction(),
		celParseDurationFunction(),
		celNowFunction(),
		celSpawnFunction(),
	}
}

// CelParseJsonFunction returns the parseJson CEL function.
func CelParseJsonFunction() cel.EnvOption { return celParseJsonFunction() }

// CelToJsonFunction returns the toJson CEL function.
func CelToJsonFunction() cel.EnvOption { return celToJsonFunction() }

// CelCoalesceFunction returns the coalesce CEL function.
func CelCoalesceFunction() cel.EnvOption { return celCoalesceFunction() }

// CelNowFunction returns the now CEL function.
func CelNowFunction() cel.EnvOption { return celNowFunction() }

// CelSpawnFunction returns the spawn CEL function.
func CelSpawnFunction() cel.EnvOption { return celSpawnFunction() }

// celParseJsonFunction parses a JSON string into a CEL value.
// Usage in CEL: parseJson(jsonString)
func celParseJsonFunction() cel.EnvOption {
	return cel.Function("parseJson",
		cel.Overload("parse_json_string",
			[]*cel.Type{cel.StringType},
			cel.DynType,
			cel.UnaryBinding(func(val ref.Val) ref.Val {
				jsonStr, ok := val.Value().(string)
				if !ok {
					return types.NewErr("parseJson() requires a string, got %T", val)
				}

				var result interface{}
				if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
					return types.NewErr("parseJson() failed to parse JSON: %v", err)
				}

				return types.DefaultTypeAdapter.NativeToValue(result)
			}),
		),
	)
}

// celToJsonFunction converts a CEL value to a JSON string.
// Usage in CEL: toJson(value) or value.toJson()
func celToJsonFunction() cel.EnvOption {
	toJsonImpl := func(val ref.Val) ref.Val {
		nativeVal := val.Value()
		jsonBytes, err := json.Marshal(nativeVal)
		if err != nil {
			return types.NewErr("toJson() failed to marshal: %v", err)
		}
		return types.String(string(jsonBytes))
	}

	return cel.Function("toJson",
		// Global function: toJson(value)
		cel.Overload("to_json_dyn",
			[]*cel.Type{cel.DynType},
			cel.StringType,
			cel.UnaryBinding(toJsonImpl),
		),
		// Member function: value.toJson()
		cel.MemberOverload("dyn_to_json",
			[]*cel.Type{cel.DynType},
			cel.StringType,
			cel.UnaryBinding(toJsonImpl),
		),
	)
}

// celCoalesceFunction returns the first non-null value.
// Usage in CEL: coalesce(a, b) or coalesce(a, b, c) or coalesce(a, b, c, d)
func celCoalesceFunction() cel.EnvOption {
	return cel.Function("coalesce",
		// Two-argument version: coalesce(a, b)
		cel.Overload("coalesce_two",
			[]*cel.Type{cel.DynType, cel.DynType},
			cel.DynType,
			cel.BinaryBinding(func(a, b ref.Val) ref.Val {
				if a.Type() != types.NullType {
					return a
				}
				return b
			}),
		),
		// Three-argument version: coalesce(a, b, c)
		cel.Overload("coalesce_three",
			[]*cel.Type{cel.DynType, cel.DynType, cel.DynType},
			cel.DynType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				for _, arg := range args {
					if arg.Type() != types.NullType {
						return arg
					}
				}
				return types.NullValue
			}),
		),
		// Four-argument version: coalesce(a, b, c, d)
		cel.Overload("coalesce_four",
			[]*cel.Type{cel.DynType, cel.DynType, cel.DynType, cel.DynType},
			cel.DynType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				for _, arg := range args {
					if arg.Type() != types.NullType {
						return arg
					}
				}
				return types.NullValue
			}),
		),
	)
}

// celGetOrDefaultFunction safely accesses a map key with a default value.
// Usage in CEL: getOrDefault(map, key, default)
func celGetOrDefaultFunction() cel.EnvOption {
	return cel.Function("getOrDefault",
		cel.Overload("get_or_default_dyn_string_dyn",
			[]*cel.Type{cel.DynType, cel.StringType, cel.DynType},
			cel.DynType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				if len(args) != 3 {
					return types.NewErr("getOrDefault() requires exactly 3 arguments")
				}

				mapVal := args[0]
				keyVal := args[1]
				defaultVal := args[2]

				// Handle null map
				if mapVal.Type() == types.NullType {
					return defaultVal
				}

				// Try to access the map
				mapper, ok := mapVal.(traits.Mapper)
				if !ok {
					return types.NewErr("getOrDefault() first argument must be a map, got %T", mapVal)
				}

				// Check if key exists
				result := mapper.Get(keyVal)
				if result.Type() == types.ErrType {
					return defaultVal
				}
				if result.Type() == types.NullType {
					return defaultVal
				}

				return result
			}),
		),
	)
}

// celParseDurationFunction parses a Go duration string and returns seconds as a number.
// Usage in CEL: parseDuration("5m") returns 300
func celParseDurationFunction() cel.EnvOption {
	return cel.Function("parseDuration",
		cel.Overload("parse_duration_string",
			[]*cel.Type{cel.StringType},
			cel.DoubleType,
			cel.UnaryBinding(func(val ref.Val) ref.Val {
				durStr, ok := val.Value().(string)
				if !ok {
					return types.NewErr("parseDuration() requires a string, got %T", val)
				}

				d, err := time.ParseDuration(durStr)
				if err != nil {
					return types.NewErr("parseDuration() failed to parse %q: %v", durStr, err)
				}

				return types.Double(d.Seconds())
			}),
		),
	)
}

// celNowFunction returns the current time as an RFC3339 string.
// Usage in CEL: now() returns "2024-01-15T10:30:00Z"
func celNowFunction() cel.EnvOption {
	return cel.Function("now",
		cel.Overload("now_void",
			[]*cel.Type{},
			cel.StringType,
			cel.FunctionBinding(func(args ...ref.Val) ref.Val {
				return types.String(time.Now().UTC().Format(time.RFC3339))
			}),
		),
	)
}

// celSpawnFunction implements spawn(workflowRef, presets) → "spawn:WORKFLOW(preset1,preset2)" or "".
// Used in tool_filter to spawn a child workflow as a tool.
// spawn("builtin://agent", ["general", "researcher"]) → "spawn:builtin://agent(general,researcher)"
// spawn("builtin://agent", []) → "" (disabled when no presets)
func celSpawnFunction() cel.EnvOption {
	return cel.Function("spawn",
		cel.Overload("spawn_string_list",
			[]*cel.Type{cel.StringType, cel.ListType(cel.StringType)},
			cel.StringType,
			cel.BinaryBinding(func(workflowRefVal, presetsVal ref.Val) ref.Val {
				workflowRef, ok := workflowRefVal.Value().(string)
				if !ok {
					return types.NewErr("spawn() first argument must be a string, got %T", workflowRefVal.Value())
				}

				list, ok := presetsVal.(traits.Lister)
				if !ok {
					return types.NewErr("spawn() second argument must be a list, got %T", presetsVal)
				}

				size := list.Size().(types.Int)
				if size == 0 {
					// Empty presets = disabled spawn (empty string filtered out of tool_filter)
					return types.String("")
				}

				presets := make([]string, int(size))
				for i := 0; i < int(size); i++ {
					elem := list.Get(types.Int(i))
					if s, ok := elem.Value().(string); ok {
						presets[i] = s
					} else {
						presets[i] = fmt.Sprintf("%v", elem.Value())
					}
				}

				return types.String(fmt.Sprintf("spawn:%s(%s)", workflowRef, strings.Join(presets, ",")))
			}),
		),
	)
}
