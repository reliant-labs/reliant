// Copyright (c) 2026 Reliant Labs
package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TL-45: activate resolves a bare --workflow as a project workflow, and its
// refusal never said a built-in has that name.
func TestBuiltinRefHint(t *testing.T) {
	assert.Contains(t, builtinRefHint("autopilot-oncall"), "--workflow builtin://autopilot-oncall")
	assert.Empty(t, builtinRefHint("builtin://autopilot-oncall"), "already a ref")
	assert.Empty(t, builtinRefHint("my-own-release"), "no built-in by that name")
}
