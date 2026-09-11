// Copyright (c) 2025 Reliant Labs
package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The store is keyed by (chatID, thread) rather than chatID alone. Thread is
// unique per run — a new run sets targetThread = workflowID
// (chat_send.go:1005) — and a spawned child runs on the SAME chat with a new
// thread. Keying by chat alone therefore conflated three different scopes, and
// each of the tests below pins one of the resulting leaks shut.

// TestLoadedToolsStore_DoesNotLeakAcrossRuns covers the plan-after-auto case: an
// `auto` run loads `write`, the run ends, and a later `plan` run on the same chat
// must not inherit it.
func TestLoadedToolsStore_DoesNotLeakAcrossRuns(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	const chatID = "chat-across-runs"

	s.Add(scope(chatID, "run-1"), "write")
	assert.True(t, s.Has(scope(chatID, "run-1"), "write"))

	assert.False(t, s.Has(scope(chatID, "run-2"), "write"),
		"a later run on the same chat must not inherit the previous run's loaded tools")
}

// TestLoadedToolsStore_DoesNotLeakAcrossSpawn covers both directions of the
// spawn boundary. A child runs on the same chatID with only a new thread, so
// under chat-only keying a child's grant outlived the child and a parent's
// grant silently widened the child.
func TestLoadedToolsStore_DoesNotLeakAcrossSpawn(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	const chatID = "chat-spawn"

	parent := scope(chatID, "thread:root")
	child := scope(chatID, "thread:child-1")

	s.Add(parent, "edit")
	s.Add(child, "write")

	assert.False(t, s.Has(child, "edit"),
		"a spawned child must not inherit the parent's dynamically loaded tools")
	assert.False(t, s.Has(parent, "write"),
		"a child's loaded tool must not persist to the parent after the child returns")
}

// TestLoadedToolsStore_PermissionIsPerScope pins the parent-cap bug. call_llm
// computes the cap correctly (call_llm.go:809) but used to store it in a slot
// the parent and child shared, so concurrent spawns overwrote each other and the
// cap could be undone by whichever activity wrote last.
func TestLoadedToolsStore_PermissionIsPerScope(t *testing.T) {
	t.Parallel()
	s := newTestStore()
	const chatID = "chat-perm-scope"

	parent := scope(chatID, "thread:root")
	child := scope(chatID, "thread:child-1")

	s.SetPermission(parent, PermissionOrchestrator)
	s.SetPermission(child, PermissionReadOnly)

	assert.Equal(t, PermissionOrchestrator, s.GetPermission(parent),
		"a child's lower permission must not overwrite the parent's")
	assert.Equal(t, PermissionReadOnly, s.GetPermission(child),
		"the child must keep the capped permission it was given")
}

// TestLoadedToolsStore_UnknownScopeFailsClosed is the fail-open fix. The default
// used to be PermissionOrchestrator, so a worker restart — which empties this
// in-memory store — briefly granted maximum privilege to any execute_tools call
// landing between the restart and the next call_llm.
func TestLoadedToolsStore_UnknownScopeFailsClosed(t *testing.T) {
	t.Parallel()
	s := newTestStore()

	assert.Equal(t, PermissionReadOnly, s.GetPermission(scope("chat-never-seen", "thread:root")),
		"an unknown scope must fail closed, not grant orchestrator")
}
