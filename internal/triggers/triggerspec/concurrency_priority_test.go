// Copyright (c) 2025 Reliant Labs
package triggerspec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConcurrencyPriority(t *testing.T) {
	scope := Scope{Trigger: map[string]any{"payload": map[string]any{
		"labels": []any{"main-red"},
		"number": float64(42),
		"half":   1.5,
		"name":   "x",
	}}}
	for _, tc := range []struct {
		name, expr string
		want       int64
		compileErr string
		evalErr    string
	}{
		{name: "empty is zero", expr: "", want: 0},
		{name: "constant", expr: "7", want: 7},
		{name: "negative", expr: "-3", want: -3},
		{name: "conditional", expr: "'main-red' in trigger.payload.labels ? 10 : 0", want: 10},
		{name: "conditional miss", expr: "'priority' in trigger.payload.labels ? 10 : 0", want: 0},
		{name: "whole JSON number", expr: "trigger.payload.number", want: 42},
		{name: "uint", expr: "3u", want: 3},
		{name: "fraction", expr: "trigger.payload.half", evalErr: "not an integer"},
		{name: "string payload", expr: "trigger.payload.name", evalErr: "not an integer"},
		{name: "missing key", expr: "trigger.payload.absent", evalErr: "concurrency priority"},
		{name: "static bool", expr: "true", compileErr: "must evaluate to an integer"},
		{name: "template braces", expr: "{{ 1 }}", compileErr: "without {{ }}"},
		{name: "unknown root", expr: "inputs.x", compileErr: "undeclared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := CompileConcurrencyPriority(tc.expr)
			if tc.compileErr != "" {
				require.ErrorContains(t, err, tc.compileErr)
				return
			}
			require.NoError(t, err)
			got, err := p.Evaluate(scope)
			if tc.evalErr != "" {
				require.ErrorContains(t, err, tc.evalErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
