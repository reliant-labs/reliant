// Copyright (c) 2025 Reliant Labs
package core

import "testing"

// Product events carry ChatOriginLabel as their origin prop and the usage
// dashboards filter on it (origin = 'user'), so each value is pinned by name:
// changing one silently empties a dashboard.
func TestChatOriginLabel(t *testing.T) {
	t.Parallel()

	want := map[ChatOrigin]string{
		ChatOriginUser:        "user",
		ChatOriginAutomation:  "automation",
		ChatOriginAgent:       "agent",
		ChatOriginManualRun:   "manual_run",
		ChatOriginBuilderTest: "builder_test",
		ChatOriginCLI:         "cli",
		ChatOriginUnspecified: ChatOriginUnknownLabel,
		ChatOrigin(9999):      ChatOriginUnknownLabel,
	}
	for origin, label := range want {
		if got := ChatOriginLabel(origin); got != label {
			t.Errorf("ChatOriginLabel(%d) = %q, want %q", origin, got, label)
		}
	}
}
