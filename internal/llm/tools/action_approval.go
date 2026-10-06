// Copyright (c) 2025 Reliant Labs
package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// A mutating integration action (MutatingIntegrationAction) does not run on
// the model's say-so in a run a person is attending: the workflow asks them
// first, with the approval card, and they answer Allow once, Always allow or
// Deny. An unattended run is never asked — it is handed such an action only
// when its step names the tool, and that name is the author's approval
// (UnattendedWithholding).
//
// "Always allow" is remembered per user and per action, as one settings row:
// ActionApprovalSettingKey(tool) holds an ActionApprovalSetting. The row is
// what ApprovalCreate consults before asking, and deleting it is how the user
// revokes the decision.

// ActionApprovalSettingPrefix namespaces the per-user "always allow" rows, one
// per action: `tool.approval.slack__message_post`.
const ActionApprovalSettingPrefix = "tool.approval."

// ActionApprovalAlwaysAllow is both the decision an "always allow" row records
// and the approval's action_taken when the person chose it.
const ActionApprovalAlwaysAllow = "always_allow"

// ActionApprovalSettingKey is the settings key holding a user's standing
// decision about one action.
func ActionApprovalSettingKey(toolName string) string {
	return ActionApprovalSettingPrefix + toolName
}

// ActionApprovalSetting is the value of one "always allow" row. The labels are
// copied from the manifest when the row is written, so the settings page can
// list what the user allowed without a catalog lookup per row.
type ActionApprovalSetting struct {
	Decision    string `json:"decision"`
	DisplayName string `json:"display_name,omitempty"`
	Integration string `json:"integration,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

// AlwaysAllowSettingValue is the row that records "always allow" for action.
func AlwaysAllowSettingValue(action IntegrationAction) string {
	b, _ := json.Marshal(ActionApprovalSetting{
		Decision:    ActionApprovalAlwaysAllow,
		DisplayName: action.DisplayName,
		Integration: action.Integration,
		Icon:        action.Icon,
	})
	return string(b)
}

// AllowsAlways reports whether a stored row's value says "always allow". A
// value that does not parse allows nothing: the person is asked.
func AllowsAlways(value string) bool {
	var setting ActionApprovalSetting
	if err := json.Unmarshal([]byte(value), &setting); err != nil {
		return false
	}
	return setting.Decision == ActionApprovalAlwaysAllow
}

// maxApprovalTargetRunes bounds the part of a title taken from the call's own
// parameters; the card shows every parameter in full beneath it.
const maxApprovalTargetRunes = 80

// ActionApprovalTitle is the question the approval card asks about a call to
// a mutating integration action, built from the action's display name and the
// parameter that says where it lands: "Send email to ann@example.com?",
// "Post message in #general?", "Comment on issue or pull request on
// acme/api#12?", "POST request to https://example.com/hook?". An action with
// none of those parameters is asked about by name alone.
func ActionApprovalTitle(toolName, input string) string {
	label := "Run " + toolName
	if action, ok := MutatingIntegrationActionInfo(toolName); ok && action.DisplayName != "" {
		label = action.DisplayName
	}
	var params map[string]any
	_ = json.Unmarshal([]byte(input), &params)

	if url := paramString(params, "url"); url != "" {
		method := strings.ToUpper(paramString(params, "method"))
		if method == "" {
			method = "GET"
		}
		return fmt.Sprintf("%s request to %s?", method, truncateRunes(url, maxApprovalTargetRunes))
	}
	if to := paramList(params, "to"); to != "" {
		return fmt.Sprintf("%s to %s?", label, truncateRunes(to, maxApprovalTargetRunes))
	}
	if owner, repo := paramString(params, "owner"), paramString(params, "repo"); owner != "" && repo != "" {
		target := owner + "/" + repo
		for _, key := range []string{"issue_number", "pull_number"} {
			if n := paramString(params, key); n != "" {
				return fmt.Sprintf("%s on %s#%s?", label, target, n)
			}
		}
		return fmt.Sprintf("%s in %s?", label, target)
	}
	if channel := paramString(params, "channel"); channel != "" {
		return fmt.Sprintf("%s in %s?", label, truncateRunes(channel, maxApprovalTargetRunes))
	}
	return label + "?"
}

// paramString is params[key] as text: a string as is, a number as written.
func paramString(params map[string]any, key string) string {
	switch v := params[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// paramList is params[key] as a short list: one value, or the first three and
// how many more.
func paramList(params map[string]any, key string) string {
	values, ok := params[key].([]any)
	if !ok {
		return paramString(params, key)
	}
	var items []string
	for _, v := range values {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			items = append(items, strings.TrimSpace(s))
		}
	}
	const shown = 3
	if len(items) > shown {
		return fmt.Sprintf("%s and %d more", strings.Join(items[:shown], ", "), len(items)-shown)
	}
	return strings.Join(items, ", ")
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
