package testkit

import (
	"fmt"
	"reflect"
	"strings"
)

// Placeholder fills *field, when it is nil, with a stand-in that was never
// initialised. The generated harness's WithPlaceholderDeps option calls it
// for every required Deps field the harness cannot construct — a concrete
// handle such as `SQL *sql.DB`, or a func with no framework default — so a
// test whose rows never reach that dependency can still construct the
// service past validateDeps.
//
// What it stores depends on the field's kind:
//
//   - a pointer gets new(T): the zero value, which for an infrastructure
//     handle like *sql.DB panics on first use rather than doing anything.
//     For a type that works at its zero value it is NOT inert: a generated
//     config message (*configv1.FooConfig) becomes a valid, empty config —
//     every getter answers its zero value — and the service runs on it
//     (reliant's model endpoints read the empty control-plane URL as
//     self-hosted, the permissive network policy). No config value is
//     distinguishable from configuration, so a test whose behaviour turns
//     on config supplies the config it means;
//   - a map or slice gets an empty one, a channel an unbuffered one;
//   - a func gets one that panics naming the field when it is called.
//
// It is a placeholder, not a double: nothing about it is faithful to the
// real dependency, and a row that reaches it has a real dependency to
// supply. A non-nil field is left alone, so a value the test set through
// With<Service>Deps always wins, as does any kind Placeholder cannot make
// (an interface).
func Placeholder[T any](field *T, name string) {
	v := reflect.ValueOf(field).Elem()
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func:
		if !v.IsNil() {
			return
		}
	default:
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 0, 0))
	case reflect.Chan:
		v.Set(reflect.MakeChan(v.Type(), 0))
	case reflect.Func:
		v.Set(reflect.MakeFunc(v.Type(), func([]reflect.Value) []reflect.Value {
			panic(fmt.Sprintf("%s is a WithPlaceholderDeps placeholder and was called: "+
				"supply a real one through the generated With<Service>Deps option", name))
		}))
	}
}

// UnfilledDepsHint is the line a generated NewTest<Service> appends to a
// construction failure: which required dependencies the harness could not
// build and left nil, and the two ways to supply them. withDeps is the
// harness's With<Service>Deps option name; unfilled describes each field as
// "Deps.SQL (*sql.DB)". It returns "" when unfilled is empty, so the
// failure reads exactly as before when the cause is something else.
//
// Without it the failure was validateDeps' own "Deps.SQL is required",
// which says what is missing and nothing about the harness option that
// supplies it.
func UnfilledDepsHint(withDeps string, unfilled ...string) string {
	if len(unfilled) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\nThe test harness cannot construct %s, and the test left it nil. "+
		"Supply it with %s(Deps{...}), or pass WithPlaceholderDeps() for a test whose rows never reach it.",
		strings.Join(unfilled, ", "), withDeps)
}
