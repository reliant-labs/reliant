// Copyright (c) 2025 Reliant Labs

/**
 * Every query-key root the stream invalidates — the user stream's handlers
 * (globalUpdatesStore), the chat stream's refetch signals (chatStore), and the
 * refetchStore subscribers both of them drive (worktree-queries,
 * useWorkflowExecutions, queued-agent-messages).
 *
 * A root covers every key under it, so a handler may invalidate a narrower
 * scope of a registered root (`chatServiceKeys.archivedChat` under
 * `chatService`). Register the root once: two overlapping roots would sweep
 * the reads under both twice.
 *
 * Why one list: REFETCH signals are ephemeral. One sent while the stream is
 * down is never replayed, so on reconnect globalUpdatesStore re-reads every
 * root here (sweepStreamInvalidatedScopes) — once, coalesced, and only the
 * reads made before the outage. A root the stream invalidates but this list
 * omits is stale after every outage until a focus refetch happens to catch it.
 * `globalUpdatesStore.reconnect-sweep.test` drives every stream event and
 * fails when a handler invalidates a root that is not registered here.
 *
 * Registering a cache the stream now invalidates is one line here:
 *
 *     daemonRegistry: daemonRegistryServiceKeys.all,
 *
 * then invalidate it from the handler through this name
 * (`STREAM_INVALIDATED_SCOPES.daemonRegistry`), not by importing the key again.
 *
 * Stream-OWNED caches (the message envelope, which the stream writes in place
 * and must never be refetched over) do not belong here: the chat snapshot on
 * resubscribe restores those.
 */

import type { QueryKey } from "@tanstack/react-query";
import { chatKeys } from "../hooks/chat-queries";
import { chatServiceKeys } from "../hooks/chat-service-hooks_gen";
import { approvalServiceKeys } from "../hooks/approval-service-hooks_gen";
import { daemonRegistryServiceKeys } from "../hooks/daemon-registry-service-hooks_gen";
import { runServiceKeys } from "../hooks/run-service-hooks_gen";
import { triggerServiceKeys } from "../hooks/trigger-service-hooks_gen";
import { inboxKeys } from "../hooks/inbox-queries";
import { workflowServiceKeys } from "../hooks/workflow-service-hooks_gen";
import { presetServiceKeys } from "../hooks/preset-service-hooks_gen";
import { projectServiceKeys } from "../hooks/project-service-hooks_gen";
import { settingsServiceKeys } from "../hooks/settings-service-hooks_gen";
import { mcpServiceKeys } from "../hooks/mcp-service-hooks_gen";
import { worktreeServiceKeys } from "../hooks/worktree-service-hooks_gen";
import { planServiceKeys } from "../hooks/plan-service-hooks_gen";
import { taskServiceKeys } from "../hooks/task-service-hooks_gen";

export const STREAM_INVALIDATED_SCOPES = {
  // Chat list/detail reads (chat lifecycle updates). Hand keys: the stream
  // patches these in place.
  chats: chatKeys.all,
  // ChatService's pull reads on generated keys: the archived list (chat
  // lifecycle updates, `.archivedChat`), a chat's execution tree
  // (`workflow_executions`, useWorkflowExecutions), a thread's mailbox
  // (`agent_mailbox`, queued-agent-messages, `.queuedAgentMessage`).
  chatService: chatServiceKeys.all,
  // Pending approvals (a chat going idle).
  approvals: approvalServiceKeys.all,
  // The one daemon list, every reader of it (`daemons`, `daemon_local_models`,
  // heartbeats; invalidateDaemonList).
  daemonRegistry: daemonRegistryServiceKeys.all,
  runs: runServiceKeys.all,
  triggers: triggerServiceKeys.all,
  inbox: inboxKeys.all,
  // Workflow drafts, and the preset lists matched against their inputs.
  workflows: workflowServiceKeys.all,
  presets: presetServiceKeys.all,
  // `file_tree`: a queued clone landed.
  projects: projectServiceKeys.all,
  // `config_health` (settings + MCP server health).
  settings: settingsServiceKeys.all,
  mcp: mcpServiceKeys.all,
  // `worktree_changes` (worktree-queries' handleWorktreeChanges).
  worktrees: worktreeServiceKeys.all,
  // `plan_tasks` on the chat stream (invalidatePlanTasks).
  plans: planServiceKeys.all,
  tasks: taskServiceKeys.all,
} as const satisfies Record<string, QueryKey>;

export type StreamInvalidatedScope = keyof typeof STREAM_INVALIDATED_SCOPES;
