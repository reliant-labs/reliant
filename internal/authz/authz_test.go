// Copyright (c) 2025 Reliant Labs

package authz

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// The decision every kind makes today: the owner, and nobody else. These
// cases pin it so the org half (phase 4) is a visible change to this table.
var ownershipCases = []struct {
	name   string
	caller string
	owner  string
	nilRow bool
	want   bool
}{
	{name: "owner", caller: "alice", owner: "alice", want: true},
	{name: "another user", caller: "bob", owner: "alice", want: false},
	{name: "missing row", caller: "alice", nilRow: true, want: false},
	{name: "empty caller on an owned row", caller: "", owner: "alice", want: false},
	{name: "empty caller on an unowned row", caller: "", owner: "", want: false},
	{name: "caller on an unowned row", caller: "alice", owner: "", want: false},
	{name: "ids compare exactly", caller: "Alice", owner: "alice", want: false},
}

func TestEveryKindAllowsOnlyItsOwner(t *testing.T) {
	ctx := context.Background()
	kinds := map[string]func(caller, owner string, nilRow bool) []bool{
		"chat": func(caller, owner string, nilRow bool) []bool {
			var c *core.Chat
			var r *core.ChatRow
			if !nilRow {
				c, r = &core.Chat{UserID: owner}, &core.ChatRow{UserID: owner}
			}
			return []bool{
				CanReadChat(ctx, caller, c), CanWriteChat(ctx, caller, c),
				CanReadChat(ctx, caller, r), CanWriteChat(ctx, caller, r),
			}
		},
		"project": func(caller, owner string, nilRow bool) []bool {
			var p *core.Project
			if !nilRow {
				p = &core.Project{UserID: owner}
			}
			return []bool{CanReadProject(ctx, caller, p), CanWriteProject(ctx, caller, p)}
		},
		"machine": func(caller, owner string, nilRow bool) []bool {
			var d *core.Daemon
			if !nilRow {
				d = &core.Daemon{UserID: owner}
			}
			return []bool{CanReadMachine(ctx, caller, d), CanWriteMachine(ctx, caller, d)}
		},
		"workflow": func(caller, owner string, nilRow bool) []bool {
			var w *core.WorkflowDraft
			if !nilRow {
				w = &core.WorkflowDraft{UserID: owner}
			}
			return []bool{CanReadWorkflow(ctx, caller, w), CanWriteWorkflow(ctx, caller, w)}
		},
		"trigger": func(caller, owner string, nilRow bool) []bool {
			var tr *core.Trigger
			if !nilRow {
				tr = &core.Trigger{UserID: owner}
			}
			return []bool{CanReadTrigger(ctx, caller, tr), CanWriteTrigger(ctx, caller, tr)}
		},
		"trigger event": func(caller, owner string, nilRow bool) []bool {
			var ev *core.TriggerEvent
			if !nilRow {
				ev = &core.TriggerEvent{UserID: owner}
			}
			return []bool{CanReadTriggerEvent(ctx, caller, ev)}
		},
		"personal": func(caller, owner string, nilRow bool) []bool {
			if nilRow {
				// Personal rows have no common type; the call site checks nil.
				return []bool{false}
			}
			return []bool{CanAccessPersonal(caller, owner)}
		},
	}
	for kind, decide := range kinds {
		for _, tc := range ownershipCases {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				for i, got := range decide(tc.caller, tc.owner, tc.nilRow) {
					if got != tc.want {
						t.Errorf("decision %d = %v, want %v", i, got, tc.want)
					}
				}
			})
		}
	}
}

// A typed nil inside the generic chat functions is a missing chat, never a
// panic: several call sites relied on an earlier nil check that the helper
// now carries.
func TestNilChatPointersAreMissing(t *testing.T) {
	ctx := context.Background()
	var c *core.Chat
	var r *core.ChatRow
	if CanReadChat(ctx, "", c) || CanWriteChat(ctx, "", r) {
		t.Fatal("a nil chat with an empty caller must be denied")
	}
}
