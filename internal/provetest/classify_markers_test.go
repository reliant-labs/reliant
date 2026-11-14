// Copyright (c) 2025 Reliant Labs
package provetest

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// A baseline that did not build is not proof. An Objective-C baseline whose
// helper took no argument failed with 12 clang errors and was reported as
// proven (bug report 9b88de9a), because no marker knew a C-family compiler.
func TestCompileMarkersRecogniseCFamilyAndSwift(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		output string
		kind   string
	}{
		"objective-c logos":               {"src/ApolloPortraitLock.xm:41:23: error: too many arguments to function call, expected 0, have 1\n12 errors generated.", "C/C++/Objective-C compile error"},
		"c++ fatal":                       {"lib/a.cpp:3:10: fatal error: 'missing.h' file not found", "C/C++/Objective-C compile error"},
		"swift":                           {"Sources/App/Lock.swift:7:12: error: cannot find 'lock' in scope", "Swift compile error"},
		"an assertion that prints error:": {"--- FAIL: TestX\n    x_test.go:12: got error: boom", ""},
		"a log line naming a source file": {"FAIL: expected error: bad input (from parser.c)", ""},
	} {
		m := findCompileMarker(tc.output)
		if tc.kind == "" {
			assert.Nil(t, m, name)
			continue
		}
		if assert.NotNil(t, m, name) {
			assert.Equal(t, tc.kind, m.Kind, name)
		}
	}

	r := &Report{Before: &RunOutcome{ExitCode: 1, Output: "src/ApolloPortraitLock.xm:41:23: error: too many arguments to function call"}, After: &RunOutcome{}}
	classify(r)
	assert.Equal(t, VerdictBaselineDoesNotCompile, r.Verdict)
}
