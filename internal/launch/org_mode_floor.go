// Copyright (c) 2025 Reliant Labs

package launch

import (
	"context"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgpolicy"
)

// The agent workflows' execution mode (`mode: manual | auto | plan`,
// internal/workflow/builtin/agent.yaml): manual routes every tool call
// through an approval, auto approves them itself, plan is read-only.
const (
	modeInput  = "mode"
	modeAuto   = "auto"
	modeManual = "manual"
)

// applyChatModeFloor coerces a chat's `auto` mode to `manual` when its org's
// policy sets chat_mode_floor NO_AUTO (control-plane
// docs/design/org-policies.md §4.4). It runs where a run's inputs are built
// and where a later message re-resolves them, so a chat that began before the
// policy is held to it from its next turn. It only raises strictness: `plan`
// (read-only) and `manual` pass untouched, and a mode that is not the
// string "auto" is never the floor's to change. A floor the org sets in audit,
// or the platform has not turned on, changes nothing and is recorded.
func applyChatModeFloor(ctx context.Context, chat *db.Chat, inputs map[string]interface{}) {
	FloorChatMode(ctx, chat, inputs)
}

// ChatModeFloorEnforced reports whether chat's org holds its runs to the
// NO_AUTO chat mode floor (enforced, not audited). It records nothing: a
// caller asks it before the work of finding the run's current mode, which
// FloorChatMode then floors (and records).
func ChatModeFloorEnforced(ctx context.Context, chat *db.Chat) bool {
	if chat == nil || chat.OrgID == nil || *chat.OrgID == "" {
		return false
	}
	return orgpolicy.ChatNoAuto(ctx, *chat.OrgID).Enforced
}

// FloorChatMode is the floor applyChatModeFloor applies, for a caller that
// holds a live run's inputs: inputs["mode"] "auto" becomes "manual" when the
// chat's org enforces NO_AUTO.
func FloorChatMode(ctx context.Context, chat *db.Chat, inputs map[string]interface{}) {
	if chat == nil || chat.OrgID == nil || *chat.OrgID == "" || inputs == nil {
		return
	}
	if mode, _ := inputs[modeInput].(string); mode != modeAuto {
		return
	}
	d := orgpolicy.ChatNoAuto(ctx, *chat.OrgID)
	d.Record(ctx, "chat_id", chat.ID, "from", modeAuto, "to", modeManual)
	if d.Enforced {
		inputs[modeInput] = modeManual
	}
}
