package handlers

import "testing"

// Keep the positional call forms usable alongside explicit mailbox wiring.
func TestActivityConstructorCompatibility(t *testing.T) {
	store := &MockDrainAgentMessagesStore{}
	// A legacy drain store also supplied the mailbox operations.
	legacyDrain := struct {
		DrainAgentMessagesStore
		MailboxQueue
	}{store, &MockMailboxQueue{}}
	drain := NewDrainAgentMessagesActivity(legacyDrain, nil)
	if drain.repo != legacyDrain || drain.mailbox != legacyDrain {
		t.Fatal("legacy drain dependencies were not preserved")
	}
	execute := NewExecuteToolsActivity(nil, nil, nil, nil)
	invoke := NewInvokeToolActivity(nil, nil, nil, nil)
	if execute == nil || invoke.executeTools == nil {
		t.Fatal("legacy tool constructors returned no activity")
	}
	status := NewThreadStatusActivity(nil)
	if status == nil {
		t.Fatal("legacy status constructor returned no activity")
	}
}
