// Copyright (c) 2025 Reliant Labs
package tools

import "testing"

// bash_wait must reach the model through the same tag filters as the rest of
// the bash family. A tool that exists but is never offered is dead code, and
// the shell family is deliberately kept whole: an agent that can start a
// background process must be able to wait for it.
func TestBashWait_IsOfferedWithTheShellFamily(t *testing.T) {
	for _, filter := range []string{"tag:default", "tag:shell", "tag:plan", "tag:readonly"} {
		names := ExpandToolFilter([]string{filter}, nil)
		found := false
		for _, n := range names {
			if n == ToolBashWait {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("bash_wait missing from %s — it must travel with bash_output/bash_list", filter)
		}
	}
}
