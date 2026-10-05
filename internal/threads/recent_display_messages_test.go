package threads

import (
	"context"
	"fmt"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// LoadRecentDisplayMessages walks the CW chain newest-first and stops once it
// has `limit` messages, instead of resolving the whole transcript and slicing
// it. That is only acceptable if the answer is the same, so these tests hold
// it to exactly the tail of LoadDisplayMessages — ids, order, and the visual
// thread normalization — for every limit, on a chain shaped like the one that
// motivated the change (chat 8bb0a875: root -> branch -> 2 compactions ->
// branch -> 2 compactions).

const (
	roleUser      int32 = 1
	roleAssistant int32 = 2
	roleSummary   int32 = 3
)

// forkCompactionChain builds, across three chats:
//
//	root (chat A)                       6 messages, then 3 written AFTER the fork
//	└─ branch-1 (chat B), fork at root's 4th message
//	     cw0: 4 messages + summary  ── compaction
//	     cw1: 3 messages + summary  ── compaction
//	     cw2: 2 messages, then 1 written AFTER the fork
//	     └─ branch-2 (chat C), fork at cw2's 1st message
//	          cw0: 3 messages + summary  ── compaction
//	          cw1: 4 messages + summary  ── compaction
//	          cw2: 5 messages
//
// The post-fork writes in each parent window are what make the fork cut
// observable: a walk that forgot to apply it would include them.
//
// Returns the leaf thread and the size of its newest window.
func forkCompactionChain(t *testing.T, h *testHelper) (leafThreadID string, newestWindowSize int) {
	t.Helper()

	chatA := h.createChat("chain-chat-a")
	chatB := h.createChat("chain-chat-b")
	chatC := h.createChat("chain-chat-c")

	// Ordinal is unique per THREAD and keeps climbing across compactions,
	// the way the real writer allocates it.
	ordinals := map[string]int64{}
	add := func(id, chatID, threadID, cwID string, role int32) *db.Message {
		msg := h.addMessageWithID(id, chatID, threadID, cwID, ordinals[threadID], role)
		ordinals[threadID]++
		return msg
	}
	addN := func(prefix string, n int, chatID, threadID, cwID string) {
		for i := 0; i < n; i++ {
			role := roleUser
			if i%2 == 1 {
				role = roleAssistant
			}
			add(fmt.Sprintf("%s-%d", prefix, i), chatID, threadID, cwID, role)
		}
	}

	root, rootCW := h.createThread("chain-root", chatA)
	addN("root", 6, chatA, root.ID, rootCW.ID)

	branch1, b1cw0 := h.forkThread("chain-branch-1", chatB, root.ID, 3, rootCW.ID)
	addN("root-after-fork", 3, chatA, root.ID, rootCW.ID)

	addN("b1-cw0", 4, chatB, branch1.ID, b1cw0.ID)
	b1cw1 := h.compact(branch1.ID, add("b1-summary-1", chatB, branch1.ID, b1cw0.ID, roleSummary).ID)
	addN("b1-cw1", 3, chatB, branch1.ID, b1cw1.ID)
	b1cw2 := h.compact(branch1.ID, add("b1-summary-2", chatB, branch1.ID, b1cw1.ID, roleSummary).ID)
	addN("b1-cw2", 2, chatB, branch1.ID, b1cw2.ID)

	forkOrdinal := ordinals[branch1.ID] - 2 // b1-cw2-0
	branch2, b2cw0 := h.forkThread("chain-branch-2", chatC, branch1.ID, forkOrdinal, b1cw2.ID)
	addN("b1-after-fork", 1, chatB, branch1.ID, b1cw2.ID)

	addN("b2-cw0", 3, chatC, branch2.ID, b2cw0.ID)
	b2cw1 := h.compact(branch2.ID, add("b2-summary-1", chatC, branch2.ID, b2cw0.ID, roleSummary).ID)
	addN("b2-cw1", 4, chatC, branch2.ID, b2cw1.ID)
	b2cw2 := h.compact(branch2.ID, add("b2-summary-2", chatC, branch2.ID, b2cw1.ID, roleSummary).ID)
	addN("b2-cw2", 5, chatC, branch2.ID, b2cw2.ID)

	return branch2.ID, 5
}

// assertRecentDisplayIsTailOfDisplay checks LoadRecentDisplayMessages against
// the tail of LoadDisplayMessages for every limit up to past the end.
func assertRecentDisplayIsTailOfDisplay(t *testing.T, svc *Service, threadID string) {
	t.Helper()
	ctx := context.Background()

	full, err := svc.LoadDisplayMessages(ctx, threadID)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}
	if len(full) == 0 {
		t.Fatal("fixture resolved no messages; it is not exercising anything")
	}

	for limit := 1; limit <= len(full)+2; limit++ {
		got, err := svc.LoadRecentDisplayMessages(ctx, threadID, limit)
		if err != nil {
			t.Fatalf("LoadRecentDisplayMessages(limit=%d): %v", limit, err)
		}
		want := full[max(0, len(full)-limit):]
		if len(got) != len(want) {
			t.Fatalf("limit=%d: got %d messages %v, want %d %v", limit, len(got), ids(got), len(want), ids(want))
		}
		for i := range want {
			g, w := got[i], want[i]
			if g.ID != w.ID || g.Seq != w.Seq {
				t.Fatalf("limit=%d: position %d is %s (seq %d), want %s (seq %d)\n got: %v\nwant: %v",
					limit, i, g.ID, g.Seq, w.ID, w.Seq, ids(got), ids(want))
			}
			if g.ThreadID != w.ThreadID || g.ChatID != w.ChatID || !samePtr(g.WorkflowID, w.WorkflowID) {
				t.Fatalf("limit=%d: %s normalized to thread=%s chat=%s, want thread=%s chat=%s",
					limit, g.ID, g.ThreadID, g.ChatID, w.ThreadID, w.ChatID)
			}
		}
	}
}

func TestLoadRecentDisplayMessages_EqualsTailAcrossForksAndCompactions(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()

	leaf, _ := forkCompactionChain(t, h)

	full, err := h.svc.LoadDisplayMessages(context.Background(), leaf)
	if err != nil {
		t.Fatalf("LoadDisplayMessages: %v", err)
	}
	// Pin the fixture's shape so a change to the full resolution cannot make
	// this test vacuous: the inherited root history must be reachable, and
	// the post-fork writes must not.
	seen := map[string]bool{}
	for _, m := range full {
		seen[m.ID] = true
	}
	for _, id := range []string{"root-0", "root-3", "b1-summary-1", "b1-cw2-0", "b2-summary-2"} {
		if !seen[id] {
			t.Fatalf("fixture: %s missing from the full transcript %v", id, ids(full))
		}
	}
	for _, id := range []string{"root-4", "root-after-fork-0", "b1-cw2-1", "b1-after-fork-0"} {
		if seen[id] {
			t.Fatalf("fixture: %s is past a fork point but in the full transcript %v", id, ids(full))
		}
	}
	if len(full) != 28 {
		t.Fatalf("fixture: full transcript has %d messages, want 28: %v", len(full), ids(full))
	}

	assertRecentDisplayIsTailOfDisplay(t, h.svc, leaf)
}

// A fork of a thread with no messages has no fork message, and the full walk
// then includes nothing from the parent's own window.
func TestLoadRecentDisplayMessages_EqualsTailForForkOfEmptyThread(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()

	parentChat := h.createChat("empty-parent-chat")
	childChat := h.createChat("empty-child-chat")
	parent, parentCW := h.createThread("empty-parent", parentChat)
	child, childCW := h.forkThread("empty-child", childChat, parent.ID, -1, parentCW.ID)

	// Written after the fork, so the parent window is not empty at read time:
	// it must still contribute nothing.
	h.addMessageWithID("parent-late", parentChat, parent.ID, parentCW.ID, 0, roleUser)

	h.addMessageWithID("child-0", childChat, child.ID, childCW.ID, 0, roleUser)
	summary := h.addMessageWithID("child-summary", childChat, child.ID, childCW.ID, 1, roleSummary)
	cw1 := h.compact(child.ID, summary.ID)
	h.addMessageWithID("child-1", childChat, child.ID, cw1.ID, 2, roleUser)

	assertRecentDisplayIsTailOfDisplay(t, h.svc, child.ID)
}

// The point of the change: a window that fits in the newest context window
// reads that window alone, and a deeper one reads only as far as it needs.
// Neither touches the unbounded per-window read the full walk uses.
func TestLoadRecentDisplayMessages_ReadsOnlyTheWindowsItNeeds(t *testing.T) {
	h := newTestHelper(t)
	defer h.Close()
	ctx := context.Background()

	leaf, newest := forkCompactionChain(t, h)
	repo := &windowReadCounter{Repository: h.repo}
	svc := NewService(repo)

	if _, err := svc.LoadRecentDisplayMessages(ctx, leaf, newest); err != nil {
		t.Fatalf("LoadRecentDisplayMessages: %v", err)
	}
	if repo.boundedReads != 1 || repo.fullReads != 0 {
		t.Fatalf("a limit the newest window satisfies read %d windows bounded and %d whole; want 1 and 0",
			repo.boundedReads, repo.fullReads)
	}

	*repo = windowReadCounter{Repository: h.repo}
	// newest (5) + b2 cw1's 5 + 1 from b2 cw0: three windows, not seven.
	if _, err := svc.LoadRecentDisplayMessages(ctx, leaf, newest+6); err != nil {
		t.Fatalf("LoadRecentDisplayMessages: %v", err)
	}
	if repo.boundedReads != 3 || repo.fullReads != 0 {
		t.Fatalf("a limit spanning three windows read %d windows bounded and %d whole; want 3 and 0",
			repo.boundedReads, repo.fullReads)
	}
}

type windowReadCounter struct {
	Repository
	boundedReads int
	fullReads    int
}

func (c *windowReadCounter) ListRecentMessagesInContextWindowBeforeSeq(ctx context.Context, contextWindowID string, beforeSeq int64, limit int) ([]*db.Message, error) {
	c.boundedReads++
	return c.Repository.ListRecentMessagesInContextWindowBeforeSeq(ctx, contextWindowID, beforeSeq, limit)
}

func (c *windowReadCounter) GetMessagesByContextWindow(ctx context.Context, contextWindowID string, maxOrdinal *int64) ([]*db.Message, error) {
	c.fullReads++
	return c.Repository.GetMessagesByContextWindow(ctx, contextWindowID, maxOrdinal)
}

func ids(msgs []*db.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}

func samePtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
