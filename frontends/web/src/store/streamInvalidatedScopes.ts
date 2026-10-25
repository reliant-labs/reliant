// Copyright (c) 2025 Reliant Labs

/**
 * Every query-key root the stream invalidates — the user stream's handlers
 * (globalUpdatesStore), the chat stream's refetch signals (chatStore), and the
 * refetchStore subscribers both of them drive (worktree-queries,
 * useWorkflowExecutions, queued-agent-messages).
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
import { chatDetailKeys } from "../hooks/chat-detail-keys";
import { queuedAgentMessageKeys } from "../hooks/queued-agent-messages";
import { approvalServiceKeys } from "../hooks/approval-service-hooks_gen";
import { DAEMON_LIST_QUERY_KEY } from "../hooks/daemonListQuery";
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
  // Chat list/detail/archived reads (chat lifecycle updates).
  chats: chatKeys.all,
  // A chat's execution tree (`workflow_executions`, useWorkflowExecutions).
  chatDetails: chatDetailKeys.all,
  // A thread's mailbox (`agent_mailbox`, queued-agent-messages).
  queuedAgentMessages: queuedAgentMessageKeys.all,
  // Pending approvals (a chat going idle).
  approvals: approvalServiceKeys.all,
  // The daemon list and the onboarding gate's reads (`daemons`, heartbeats;
  // invalidateDaemonList).
  daemonList: DAEMON_LIST_QUERY_KEY,
  onboardingDaemons: ["onboarding", "daemons"],
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
