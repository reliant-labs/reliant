// Copyright (c) 2025 Reliant Labs
package validation

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

// These tests pin the guard analysis (guarded_access.go) at the AST level:
// which reads of a maybe-absent `nodes.<id>` are protected, and which are not.
//
// Every "protected" verdict below corresponds to an expression that evaluates
// WITHOUT error at runtime when the node is absent, and every "unprotected"
// one to an expression that fails with "no such key". The CEL behaviours this
// relies on were verified against wfcel.EvaluateValue:
//
//	has(m.k) && m.k != ''      => false, no error
//	m.?k.orValue('x')          => 'x'
//	'k' in m                   => false
//	m.k != null                => no such key: k   <- NOT a guard for absence
//	m.k == 1 && has(m.k)       => false            (&& absorbs the error)
//	m.k == 1 || !has(m.k)      => true             (|| absorbs the error)

// absentNodes classifies every nodes.<id> in ids as possibly absent.
func absentNodes(ids ...string) func([]string) []accessRisk {
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	return func(segs []string) []accessRisk {
		if len(segs) >= 2 && segs[0] == "nodes" && set[segs[1]] {
			return []accessRisk{{path: segs[:2], kind: riskAbsent, severity: SeverityError, message: segs[1]}}
		}
		return nil
	}
}

// nullableAt classifies the value at path as possibly null.
func nullableAt(path ...string) func([]string) []accessRisk {
	return func(segs []string) []accessRisk {
		if len(segs) >= len(path) && pathKey(segs[:len(path)]) == pathKey(path) {
			return []accessRisk{{path: path, kind: riskNull, severity: SeverityError, message: pathKey(path)}}
		}
		return nil
	}
}

func unguarded(expr string, classify func([]string) []accessRisk) []string {
	var out []string
	for _, f := range analyzeGuardedAccess(expr, newFactSet(), classify) {
		out = append(out, f.message)
	}
	sort.Strings(out)
	return out
}

func TestGuardedAccess_AbsentNode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		expr      string
		unguarded []string
	}{
		// Direct reads fail.
		{"simple field access", "nodes.c.output", []string{"c"}},
		{"nested field access", "nodes.c.message.content", []string{"c"}},
		{"multiple nodes", "nodes.c1.output + nodes.c2.output", []string{"c1", "c2"}},
		{"inside size()", "size(nodes.c.message.content) > 10", []string{"c"}},
		{"deeply nested", "nodes.c.message.content.text.value", []string{"c"}},
		{"ternary condition", "nodes.c.output > 10 ? 'high' : 'low'", []string{"c"}},
		{"comprehension body", "[1, 2, 3].map(x, x * nodes.c.multiplier)", []string{"c"}},
		{"disjunction does not guard its operands", "(nodes.c1.value > 5 || nodes.c2.value < 10) && nodes.other.flag", []string{"c1", "c2"}},

		// != null reads the key first: NOT a guard for an absent node.
		{"null comparison is not an absence guard", "nodes.c != null && nodes.c.output", []string{"c"}},
		{"reverse null comparison", "null != nodes.c", []string{"c"}},
		{"field null check", "nodes.c.output != null", []string{"c"}},

		// has() / ?. / in are error-free presence tests.
		{"has on node", "has(nodes.c) && nodes.c.output == 'x'", nil},
		// has(x.f) evaluates x: when the node itself is absent it fails
		// ("no such key: c"), so it guards the field, not the node.
		{"has on field does not guard an absent node", "has(nodes.c.output) ? nodes.c.output : ''", []string{"c"}},
		{"has on node then field", "has(nodes.c) && has(nodes.c.output) ? nodes.c.output : ''", nil},
		{"optional select", "nodes.?c.output.orValue('x')", nil},
		{"optional select on field", "nodes.c.?output.orValue('x')", []string{"c"}},
		{"in operator", "'c' in nodes && nodes.c.output == 'x'", nil},
		{"hasValue", "nodes.?c.hasValue() && nodes.c.output == 'x'", nil},

		// Guard position: && and || absorb errors in either operand order.
		{"guard after the read under &&", "nodes.c.output == 'x' && has(nodes.c)", nil},
		{"negated guard under ||", "!has(nodes.c) || nodes.c.output == 'x'", nil},
		{"negated guard after the read under ||", "nodes.c.output == 'x' || !has(nodes.c)", nil},

		// Guards only protect the branch they dominate.
		{"ternary protects the true branch only", "has(nodes.c) ? nodes.c.output : nodes.c.fallback", []string{"c"}},
		{"negated ternary protects the false branch", "!has(nodes.c) ? '' : nodes.c.output", nil},
		{"mixed guarded and unguarded", "has(nodes.safe) && nodes.unsafe.output", []string{"unsafe"}},
		{"guard on one node does not cover another", "has(nodes.c1) ? nodes.c2.output : ''", []string{"c2"}},
		{"disjunctive guard proves neither", "(has(nodes.c1) || has(nodes.c2)) && nodes.c1.output == 'x'", []string{"c1"}},
		{"conjunctive guard under negation", "!(has(nodes.c1) && has(nodes.c2)) || nodes.c2.output == 'x'", nil},

		// String contents are not reads.
		{"string literal", "'nodes.c.output'", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.unguarded, unguarded(tt.expr, absentNodes("c", "c1", "c2", "safe", "unsafe")), tt.expr)
		})
	}
}

// A null value (a response tool the LLM did not call) fails only when read
// BENEATH; != null is a valid guard for it, and has() on a child is too.
func TestGuardedAccess_NullValue(t *testing.T) {
	t.Parallel()
	classify := nullableAt("output", "response_data", "audit")
	tests := []struct {
		name      string
		expr      string
		unguarded bool
	}{
		{"read beneath null", "output.response_data.audit.approved", true},
		{"reading the null itself is fine", "output.response_data.audit == null", false},
		{"!= null guards", "output.response_data.audit != null && output.response_data.audit.approved", false},
		{"== null ternary guards the false branch", "output.response_data.audit == null ? false : output.response_data.audit.approved", false},
		{"has on a child guards", "has(output.response_data.audit.approved) && output.response_data.audit.approved", false},
		{"optional chaining", "output.response_data.?audit.?approved.orValue(false)", false},
		{"'in' proves presence, not non-null", "'audit' in output.response_data && output.response_data.audit.approved", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := unguarded(tt.expr, classify)
			if tt.unguarded {
				assert.NotEmpty(t, got, tt.expr)
			} else {
				assert.Empty(t, got, tt.expr)
			}
		})
	}
}

// A gating condition's facts protect the expressions it gates.
func TestGuardedAccess_GateFacts(t *testing.T) {
	t.Parallel()
	gate := provenByCondition("has(nodes.c) && nodes.c.status == 'ok'")
	findings := analyzeGuardedAccess("nodes.c.output", gate, absentNodes("c"))
	assert.Empty(t, findings)

	disjunctive := provenByCondition("has(nodes.c) || inputs.force")
	findings = analyzeGuardedAccess("nodes.c.output", disjunctive, absentNodes("c"))
	assert.Len(t, findings, 1, "a disjunctive gate proves nothing about nodes.c")
}

func TestGuardedAccess_UnparseableExpressionReportsNothing(t *testing.T) {
	t.Parallel()
	assert.Empty(t, analyzeGuardedAccess("nodes.c.", newFactSet(), absentNodes("c")))
	assert.Empty(t, analyzeGuardedAccess("", newFactSet(), absentNodes("c")))
}
