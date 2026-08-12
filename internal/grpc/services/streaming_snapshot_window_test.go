// Copyright (c) 2025 Reliant Labs
package services

import (
	"sort"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
)

// trimSnapshotWindow mirrors the trimming buildChatSnapshot applies to its
// merged message set, by calling the shared windowByMainThread helper that
// both buildChatSnapshot and paginateBySeq use.
func trimSnapshotWindow(messages []*db.Message, mainThread string, limit int) []*db.Message {
	sort.Slice(messages, func(i, j int) bool { return messages[i].Seq < messages[j].Seq })
	return windowByMainThread(messages, mainThread, limit)
}

func snapMsg(id string, seq int64, thread string) *db.Message {
	return &db.Message{ID: id, Seq: seq, ThreadID: thread}
}

// The initial snapshot is what the user sees when they open a chat. Bounding it
// by the newest N messages ACROSS ALL THREADS is wrong once a chat spawns
// sub-agents: spawn threads write far more messages than the main thread and
// finish later, so they occupy the top of the chat's seq range and crowd the
// transcript out of its own window entirely.
//
// This reproduces the real shape that broke a user's chat — 998 main-thread
// messages topping out at seq 1171, and 501 spawn messages running to seq 1498,
// so every one of the newest 200 rows was a spawn message. Spawn messages
// render collapsed inside their tool call rather than in the transcript, so the
// chat appeared empty.
func TestSnapshotWindow_SpawnHeavyChatStillShowsTranscript(t *testing.T) {
	const mainThread = "main"
	var messages []*db.Message

	// Main thread: seq 0..1171 (sparse is fine; only order matters).
	for seq := int64(0); seq <= 1171; seq += 2 {
		messages = append(messages, snapMsg("main-"+string(rune(seq)), seq, mainThread))
	}
	// Spawn threads: seq 362..1498, i.e. overlapping and extending past main.
	for seq := int64(362); seq <= 1498; seq += 2 {
		messages = append(messages, snapMsg("spawn-"+string(rune(seq)), seq+1, "spawn-1"))
	}

	window := trimSnapshotWindow(messages, mainThread, 200)

	mainCount := 0
	for _, m := range window {
		if m.ThreadID == mainThread {
			mainCount++
		}
	}

	if mainCount != 200 {
		t.Fatalf("snapshot carried %d main-thread messages, want 200 — the transcript is what the user reads, so the window must be measured on it", mainCount)
	}
	// Sibling messages inside the range must survive: the spawn tool-call
	// preview renders from them.
	if len(window) <= mainCount {
		t.Fatalf("window has %d messages and %d are main-thread; sibling-thread messages were dropped and spawn previews would render empty", len(window), mainCount)
	}
}

// A chat with no spawn threads must be unaffected — same bound, same result.
func TestSnapshotWindow_SingleThreadChatUnchanged(t *testing.T) {
	const mainThread = "main"
	var messages []*db.Message
	for seq := int64(0); seq < 500; seq++ {
		messages = append(messages, snapMsg("m", seq, mainThread))
	}

	window := trimSnapshotWindow(messages, mainThread, 200)
	if len(window) != 200 {
		t.Fatalf("single-thread chat window = %d, want exactly the limit of 200", len(window))
	}
	if window[len(window)-1].Seq != 499 {
		t.Fatalf("window should end at the newest message (seq 499), got %d", window[len(window)-1].Seq)
	}
}

// A chat shorter than the limit is returned whole.
func TestSnapshotWindow_ShortChatReturnedWhole(t *testing.T) {
	const mainThread = "main"
	messages := []*db.Message{
		snapMsg("a", 0, mainThread),
		snapMsg("b", 1, "spawn-1"),
		snapMsg("c", 2, mainThread),
	}

	window := trimSnapshotWindow(messages, mainThread, 200)
	if len(window) != 3 {
		t.Fatalf("short chat window = %d, want all 3 messages", len(window))
	}
}

// Without an identifiable main thread the chat-wide bound is the only option,
// and must still apply.
func TestSnapshotWindow_NoMainThreadFallsBackToChatWideBound(t *testing.T) {
	var messages []*db.Message
	for seq := int64(0); seq < 500; seq++ {
		messages = append(messages, snapMsg("m", seq, "some-thread"))
	}

	window := trimSnapshotWindow(messages, "", 200)
	if len(window) != 200 {
		t.Fatalf("fallback window = %d, want 200", len(window))
	}
}
