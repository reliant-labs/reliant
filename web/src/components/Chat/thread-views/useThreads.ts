/**
 * Hook for deriving thread information from WorkflowExecution + messages
 * 
 * Threads map 1:1 with workflows. WorkflowExecution provides structure,
 * messages provide counts and timestamps. Activity state comes from
 * activityStore (chat-level) and threadActivityStore (thread-level),
 * both populated by the server's ChatActivity enum.
 */

import { useMemo } from "react";
import type { Message } from "../../../api/client";
import type { WorkflowExecution } from "../ExecutionSidebar/types";
import { getThreadColor, formatNodeId, resolveThreadNameFromActiveThreads } from "./threadUtils";
import { useActiveThreadIds, useActiveThreads } from "../../../store/threadActivityStore";
import { useIsChatRunning } from "../../../store/activityStore";

// Re-export utilities for backwards compatibility
export { getThreadColor, formatNodeId, resolveThreadNameFromActiveThreads } from "./threadUtils";

export interface ThreadInfo {
  id: string;
  name: string;
  messageCount: number;
  isMain: boolean;
  isActive: boolean;  // Whether this thread's workflow is currently running
  isSpawn: boolean;
  color: string;
  firstMessageAt?: string;
}


/**
 * Check if a workflow record is a thread metadata record (not real workflow execution).
 * 
 * Thread records are created by fork() and new() CEL functions:
 * - "thread:*" - New naming (threads owned by steps, properly complete)
 * - "fork:*" - Legacy naming (for backwards compatibility)
 * 
 * These record thread creation/completion and inheritance relationships,
 * but are not actual workflow executions. They should be skipped when
 * building the thread list since the actual threads are derived from messages.
 */
function isThreadMetadataRecord(workflowName: string): boolean {
  return workflowName.startsWith("thread:") || workflowName.startsWith("fork:");
}

/**
 * Derive thread list from WorkflowExecution tree + message counts.
 * Activity state is read from activityStore and threadActivityStore.
 */
export function useThreads(
  messages: Message[],
  chatId: string,
  workflowExecution?: WorkflowExecution
): ThreadInfo[] {
// Use centralized activity selectors (SINGLE SOURCE OF TRUTH)
  const isChatActive = useIsChatRunning(chatId);
  const activeThreadIds = useActiveThreadIds(chatId);
  const activeThreads = useActiveThreads(chatId);
  
  return useMemo(() => {
    // Count messages and track timestamps per thread
    const messageCounts = new Map<string, number>();
    const firstTimestamps = new Map<string, string>();
    const sortTimestamps = new Map<string, number>();
    
    for (const msg of messages) {
      // Thread defaults to chatId (main thread) if not set
      const thread = msg.thread || chatId;
      messageCounts.set(thread, (messageCounts.get(thread) || 0) + 1);
      
      const time = new Date(msg.createdAt || "").getTime();
      const existing = sortTimestamps.get(thread);
      if (!existing || time < existing) {
        sortTimestamps.set(thread, time);
        firstTimestamps.set(thread, msg.createdAt || "");
      }
    }

    const threads: ThreadInfo[] = [];
    const seenThreads = new Set<string>();

    // Walk workflow tree to build thread list in order
    function processWorkflow(wf: WorkflowExecution) {
      if (seenThreads.has(wf.thread)) return;
      
      // Skip thread metadata records - they track thread lifecycle, not workflow execution
      // The actual threads are derived from messages on those threads
      if (isThreadMetadataRecord(wf.workflowName)) {
        // Still process children though
        for (const child of wf.children) {
          processWorkflow(child);
        }
        return;
      }
      
      seenThreads.add(wf.thread);

      const isMain = wf.thread === chatId || wf.thread === "0";
      let name = "Main";
      if (!isMain) {
        if (wf.threadTitle) {
          name = formatNodeId(wf.threadTitle);
        } else if (wf.spawnedByNodeId) {
          name = formatNodeId(wf.spawnedByNodeId);
        } else {
          name = "Thread";
        }
      }

      // Activity detection:
      // - Main thread is active if root workflow is running (isChatActive)
      // - Child threads: check activeThreadIds first (for Temporal child workflows),
      //   but also consider active if root is running (for inline workflows)
      //   since inline workflows don't emit separate thread status updates
      const threadIsActive = isMain 
        ? isChatActive 
        : (activeThreadIds.has(wf.thread) || (isChatActive && wf.status === 'running'));

      threads.push({
        id: wf.thread,
        name,
        messageCount: messageCounts.get(wf.thread) || 0,
        isMain,
        isActive: threadIsActive,
        isSpawn: wf.spawnedByNodeId === "spawn_tool",
        color: getThreadColor(wf.thread, isMain),
        firstMessageAt: firstTimestamps.get(wf.thread),
      });

      for (const child of wf.children) {
        processWorkflow(child);
      }
    }

    if (workflowExecution) {
      processWorkflow(workflowExecution);
    }

    // Add main thread if no workflow execution yet
    if (!seenThreads.has(chatId)) {
      threads.unshift({
        id: chatId,
        name: "Main",
        messageCount: messageCounts.get(chatId) || 0,
        isMain: true,
        isActive: isChatActive, // Main is active if any thread is active
        isSpawn: false,
        color: getThreadColor(chatId, true),
        firstMessageAt: firstTimestamps.get(chatId),
      });
      seenThreads.add(chatId);
    }

    // Add threads from messages that weren't discovered from workflow tree
    // This handles inline workflows that create messages on new threads
    // without spawning separate child workflows
    for (const [threadId, count] of messageCounts) {
      if (seenThreads.has(threadId)) continue;
      if (count === 0) continue;
      
      // Determine if this is the main thread
      const isMain = threadId === chatId;
      // For message-derived threads (from inline workflows), they're active
      // when the root workflow is running, since inline workflows don't
      // emit separate thread status updates
      const threadIsActive = isMain ? isChatActive : (activeThreadIds.has(threadId) || isChatActive);
      
      // Resolve thread name and spawn status from activeThreads streaming data
      const activeThread = activeThreads.find(at => at.thread === threadId);
      const resolvedName = !isMain ? resolveThreadNameFromActiveThreads(threadId, activeThreads) : undefined;
      threads.push({
        id: threadId,
        name: isMain ? "Main" : (resolvedName || "Thread"),
        messageCount: count,
        isMain,
        isActive: threadIsActive,
        isSpawn: activeThread?.spawned_by_node_id === "spawn_tool",
        color: getThreadColor(threadId, isMain),
        firstMessageAt: firstTimestamps.get(threadId),
      });
      seenThreads.add(threadId);
    }

    // Sort: main first, then by first message timestamp
    threads.sort((a, b) => {
      if (a.isMain && !b.isMain) return -1;
      if (!a.isMain && b.isMain) return 1;
      const timeA = sortTimestamps.get(a.id) || 0;
      const timeB = sortTimestamps.get(b.id) || 0;
      return timeA - timeB;
    });

    return threads;
  }, [messages, chatId, workflowExecution, isChatActive, activeThreadIds, activeThreads]);
}

/**
 * Group messages by thread
 */
export function useMessagesByThread(
  messages: Message[],
  chatId: string
): Map<string, Message[]> {
  return useMemo(() => {
    const groups = new Map<string, Message[]>();

    for (const msg of messages) {
      // Thread defaults to chatId (main thread) if not set
      const thread = msg.thread || chatId;
      if (!groups.has(thread)) {
        groups.set(thread, []);
      }
      groups.get(thread)!.push(msg);
    }

    // Sort messages within each group by timestamp
    for (const [, msgs] of groups) {
      msgs.sort(
        (a, b) => new Date(a.createdAt || "").getTime() - new Date(b.createdAt || "").getTime()
      );
    }

    return groups;
  }, [messages, chatId]);
}