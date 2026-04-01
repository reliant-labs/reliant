// Package threads provides thread and context window management for conversations.
//
// # Thread Model
//
// A Thread represents a conversation branch. Threads can be:
//   - Root threads: Fresh conversations with no parent
//   - Forked threads: Branched from a parent thread at a specific point
//   - Cross-chat forks: Forked from a thread in a different conversation
//
// # Context Windows
//
// A ContextWindow represents an atomic unit of LLM context within a thread.
// Each thread has one or more context windows, identified by sequence number:
//   - Sequence 0: Initial context window
//   - Sequence N (N > 0): Post-compaction context window
//
// # Sequence Inheritance
//
// When forking from a parent thread:
//   - The child inherits the parent's context sequence from ForkAtContextWindowID
//   - This ensures the compaction boundary check works correctly
//   - sequence > 0 means "this context includes compacted/summarized history"
//
// # Compaction Boundaries
//
// When resolving messages for LLM context:
//   - Fork chains are traversed to collect inherited messages
//   - Traversal STOPS at compaction boundaries (sequence transitions)
//   - This prevents re-inheriting messages that are already summarized
//
// # Token Counting
//
// Token counting follows the same fork-chain traversal as message resolution,
// ensuring consistent behavior between "what messages will the LLM see" and
// "how many tokens is that".
//
// # Usage
//
// Create a new threads.Service with a repository:
//
//	svc := threads.NewService(repo)
//
// Create a root thread:
//
//	thread, cw, err := svc.CreateThread(ctx, threads.CreateThreadOpts{
//	    ConversationID: chatID,
//	})
//
// Create a workflow with a forked thread (from a specific message):
//
//	_, thread, cw, err := svc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
//	    Workflow:        workflow,
//	    ThreadID:        newThreadID,
//	    ChatID:          chatID,
//	    ForkFromMessage: &branchPointMessageID,
//	})
//
// Create a workflow with a forked thread (from thread's latest state):
//
//	_, thread, cw, err := svc.CreateWorkflowWithThread(ctx, threads.CreateWorkflowWithThreadOpts{
//	    Workflow:       workflow,
//	    ThreadID:       newThreadID,
//	    ChatID:         chatID,
//	    ForkFromThread: &parentThreadID,
//	})
//
// Compact a thread:
//
//	cw, err := svc.Compact(ctx, threadID, summaryMessageID)
package threads
