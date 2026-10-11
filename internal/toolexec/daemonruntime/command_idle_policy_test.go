// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"sort"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/daemonactivity"
)

// Every command the daemon serves must have a deliberate place in the
// machine's idle policy: either forwarding it is activity that keeps a cloud
// machine awake, or it is background traffic that must not. An unclassified
// command does not count, which is the safe default for cost — but a new
// user-facing command left out by accident would let the machine suspend
// under someone who is using it, so adding a command forces the decision
// here rather than leaving it to whoever notices.
func TestEveryDaemonCommandHasAnIdlePolicy(t *testing.T) {
	defaultRegistry.mu.RLock()
	var unclassified []string
	for commandType := range defaultRegistry.handlers {
		// Registered by tests in this package; never on the wire.
		if strings.HasPrefix(commandType, "test.") {
			continue
		}
		if !daemonactivity.IsClassifiedCommand(commandType) {
			unclassified = append(unclassified, commandType)
		}
	}
	defaultRegistry.mu.RUnlock()

	if len(unclassified) > 0 {
		sort.Strings(unclassified)
		t.Errorf("daemon commands with no idle policy: %v\n"+
			"Add each to userActionCommands (a person or agent asked for this work; it keeps a cloud machine awake) "+
			"or backgroundCommands (a read, status poll or housekeeping; it must not) in internal/daemonactivity/reason.go.",
			unclassified)
	}
}
