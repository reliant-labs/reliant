// Copyright (c) 2025 Reliant Labs
package threads

import (
	"context"
	"testing"
)

// A branched chat DISPLAYS its parent's history but does not own those rows —
// they keep the parent's chat_id and are resolved on read through the context
// window chain. So an allocator that only looks at the branch's own rows starts
// it at seq 0, and every message the user sends lands numerically beneath the
// hundreds of inherited messages rendered above it. The message is saved and
// streamed correctly; it just draws at the top of the transcript, which reads as
// "sending to a branch does nothing".
//
// The allocated seq must clear everything the branch actually shows.
func TestBranchSeqAllocationClearsInheritedHistory(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	parentChat := h.createChat("parent-chat-seq")
	parentThread, parentCW := h.createThread("parent-thread-seq", parentChat)

	// Give the parent a real history.
	const parentMessages = 25
	for i := 0; i < parentMessages; i++ {
		h.addMessageWithID(
			"p-msg-"+string(rune('a'+i)), parentChat, parentThread.ID, parentCW.ID, int64(i), 1,
		)
	}

	highestInherited, err := h.repo.GetNextSeq(ctx, parentChat, parentThread.ID)
	if err != nil {
		t.Fatalf("parent next seq: %v", err)
	}

	// Fork at the parent's last message, the way BranchChat does.
	branchChat := h.createChat("branch-chat-seq")
	forkAt := h.messageIDAtOrdinal(parentCW.ID, int64(parentMessages-1))
	if forkAt == nil {
		t.Fatal("could not resolve a fork point in the parent")
	}
	branchThread, _ := h.forkThread(
		"branch-thread-seq", branchChat, parentThread.ID, int64(parentMessages-1), parentCW.ID,
	)

	// What the branch actually renders.
	inherited, err := NewService(h.repo).LoadCurrentMessages(ctx, branchThread.ID)
	if err != nil {
		t.Fatalf("load branch messages: %v", err)
	}
	if len(inherited) == 0 {
		t.Fatal("branch inherited nothing; the fixture is not exercising a fork")
	}

	var maxInheritedSeq int64 = -1
	for _, m := range inherited {
		if m.Seq > maxInheritedSeq {
			maxInheritedSeq = m.Seq
		}
	}

	// The seq the branch's first own message would receive.
	next, err := h.repo.GetNextSeq(ctx, branchChat, branchThread.ID)
	if err != nil {
		t.Fatalf("branch next seq: %v", err)
	}

	if next <= maxInheritedSeq {
		t.Fatalf(
			"branch allocated seq %d but it renders %d inherited messages up to seq %d — a new message would sort above %d of them",
			next, len(inherited), maxInheritedSeq, maxInheritedSeq-next+1,
		)
	}
	if next < highestInherited {
		t.Fatalf("branch allocated seq %d, below the parent's high-water mark %d", next, highestInherited)
	}
}
