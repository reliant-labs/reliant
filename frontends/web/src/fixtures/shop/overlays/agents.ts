// Copyright (c) 2025 Reliant Labs

/**
 * A chat the screenshot harness opens by URL, and that no list names (so the
 * sidebar and the chat list do not move): one turn that starts twelve
 * sub-agents at once, 8 done, 3 running and 1 failed. The transcript draws
 * them as one group row with a dense row per agent (Chat/SubAgentGroup).
 *
 * An overlay (../overlays.ts `chat-agents`): its GetChat, ListMessages,
 * GetWorkflowExecutions and StreamChatUpdates answers come first for
 * AGENTS_CHAT_ID, and every other chat goes to the pack's own handler.
 *
 * Ported from the visual harness's own fixtures (e2e/visual, before it served
 * this pack). Every time is relative to the pack clock.
 */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import {
  ChatActivity,
  ChatService,
  ListMessagesResponseSchema,
  WorkflowState,
  WorkflowStopReason,
  type Message,
  type WorkflowExecutionSchema,
} from "@/gen/reliant/v1/chat_pb";
import { ChatStreamEventSchema, type StreamChatUpdatesRequest } from "@/gen/reliant/v1/streaming_pb";

import { chatHandlers, LATEST_SEQUENCE } from "../handlers/reliant/chats";
import { chatStreamEvents, untilAborted } from "../handlers/reliant/streaming";
import { decodeFixtureJson } from "../packJson";
import { MINUTE, demoNow } from "../shopStory";
import { serve } from "../handlers/serve";
import { chat, workflowIdOf } from "../workspace";

/** The chat's id: e2e/visual/surfaces.visual.ts writes it out. */
export const AGENTS_CHAT_ID = "shop-chat-agents";
const WF = workflowIdOf(AGENTS_CHAT_ID);
/** The turn started ten minutes ago; the agents nine. */
const AGO = 10 * MINUTE;

const AGENT_TITLES = [
  "Audit checkout for N+1 queries", "Port the cart store to signals", "Write bundle discount tests", "Profile totals on 10k-line carts",
  "Review the tax config change", "Update the pricing docs", "Check Stripe v18 webhooks", "Add tracing to the checkout API",
  "Benchmark cart totals", "Draft the release notes", "Fix flaky invoice snapshot", "Survey currency rounding rules",
];
const agentState = (i: number) => (i === 8 ? "failed" : i >= 9 ? "running" : "done");
const agentThread = (i: number) => `th-agents-${String(i + 1).padStart(2, "0")}`;

/** The nth message's time (40s apart from the turn's start), plus `plus` ms. */
const atSeq = (seq: number, plus = 0) => new Date(demoNow() - AGO + seq * 40_000 + plus).toISOString();

function agentsChat() {
  return chat({
    id: AGENTS_CHAT_ID, title: "Pre-ship checks for the checkout rewrite", activity: ChatActivity.RUNNING,
    workflowState: WorkflowState.ACTIVE, workflowStopReason: WorkflowStopReason.UNSPECIFIED, ago: AGO,
  });
}

type Block = Record<string, unknown>;

function message(seq: number, role: "user" | "assistant", blocks: Block[]): Record<string, unknown> {
  const id = `sc-ag-m${seq}`;
  const assistant = role === "assistant";
  return {
    id, chatId: AGENTS_CHAT_ID, seq: String(seq), thread: AGENTS_CHAT_ID,
    role: assistant ? "MESSAGE_ROLE_ASSISTANT" : "MESSAGE_ROLE_USER", streamingState: "STREAMING_STATE_COMPLETE",
    ...(assistant ? { model: "claude-sonnet-5", agent: "agent" } : {}),
    // Tool calls run in the seconds after their message starts.
    contentBlocks: blocks.map((b, j) => ({
      id: `${id}-b${j}`, ...b, index: j,
      ...(b.type === "CONTENT_BLOCK_TYPE_TOOL_CALL" ? { startedAt: atSeq(seq, j * 2_000), completedAt: atSeq(seq, j * 2_000 + 1_500) } : {}),
    })),
    attachments: [], createdAt: atSeq(seq), updatedAt: atSeq(seq), sequenceNumber: String(seq),
  };
}

const text = (content: string): Block => ({ type: "CONTENT_BLOCK_TYPE_TEXT", content });

/** A spawn: its call starts the agent and returns at once; the agent's own state is its workflow's. */
const spawn = (i: number, title: string): Block[] => [
  {
    type: "CONTENT_BLOCK_TYPE_TOOL_CALL", toolCallId: `ag${i + 1}`, toolName: "spawn",
    input: JSON.stringify({ title, preset: "general", prompt: title }), toolCallStatus: "TOOL_CALL_STATUS_COMPLETED", childWorkflowId: agentThread(i),
  },
  { type: "CONTENT_BLOCK_TYPE_TOOL_RESULT", toolCallId: `ag${i + 1}`, toolName: "spawn", content: `Started ${title}.`, isError: false },
];

function agentsTranscript(): Message[] {
  const messages = [
    message(1, "user", [text("Before we ship the checkout rewrite, fan out and check everything on the list in parallel.")]),
    message(2, "assistant", [text("Twelve independent checks: I am starting one agent for each."), ...AGENT_TITLES.flatMap((title, i) => spawn(i, title))]),
  ];
  return decodeFixtureJson(ListMessagesResponseSchema, { messages }).messages;
}

type ExecutionInit = MessageInitShape<typeof WorkflowExecutionSchema>;

function agentsTree(): ExecutionInit[] {
  const running = { state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED };
  const started = demoNow() - 9 * MINUTE;
  const node = (id: string, thread: string, o: ExecutionInit = {}): ExecutionInit => ({
    id, workflowName: "builtin://agent", thread, createdAt: atSeq(1), messageCount: 0, children: [], steps: [],
    origin: "main", state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED, ...o,
  });
  return [
    node(WF, AGENTS_CHAT_ID, {
      ...running,
      children: AGENT_TITLES.map((title, i) =>
        node(agentThread(i), agentThread(i), {
          origin: "spawn", parentId: WF, parentThread: AGENTS_CHAT_ID, threadTitle: title, createdAt: new Date(started).toISOString(),
          ...(agentState(i) === "running"
            ? running
            : {
                completedAt: new Date(started + (i + 2) * 37_000).toISOString(),
                ...(agentState(i) === "failed" ? { stopReason: WorkflowStopReason.FAILED } : {}),
              }),
        }),
      ),
    }),
  ];
}

const key = (name: string) => `${ChatService.typeName}/${name}`;

/** The pack's chat handlers, with the agents chat answered first. */
export function agentsChatHandlers(): MockHandlers {
  const base = chatHandlers() as unknown as MockHandlers;
  return serve(ChatService, {
    getChat: (req) => (req.chatId === AGENTS_CHAT_ID ? { chat: agentsChat() } : (base[key("GetChat")]!(req) as never)),
    listMessages: (req) => {
      if (req.chatId !== AGENTS_CHAT_ID) return base[key("ListMessages")]!(req) as never;
      // A sub-agent's own thread is never opened here: its messages are none.
      const messages = req.threadId && req.threadId !== AGENTS_CHAT_ID ? [] : agentsTranscript();
      return { messages, total: messages.length, count: messages.length, hasMore: false, oldestSeq: 1n };
    },
    getWorkflowExecutions: (req) =>
      req.chatId === AGENTS_CHAT_ID ? { allRootWorkflows: agentsTree() } : (base[key("GetWorkflowExecutions")]!(req) as never),
  });
}

/** The chat stream: the agents chat's snapshot, or the pack's own; then open until aborted. */
export async function* agentsChatStream(req: StreamChatUpdatesRequest, ctx: { signal?: AbortSignal } | undefined) {
  if (req.chatId !== AGENTS_CHAT_ID) {
    yield* chatStreamEvents(req.chatId);
  } else {
    const messages = agentsTranscript();
    yield create(ChatStreamEventSchema, {
      event: {
        case: "snapshot",
        value: {
          messages, otherUpdates: [], latestSequence: LATEST_SEQUENCE, total: messages.length, hasMore: false,
          oldestSeq: 1n, threadTokenCount: 41_200n, compactionThreshold: 120_000n, activeWorkflows: [],
        },
      },
    });
    yield create(ChatStreamEventSchema, { event: { case: "caughtUp", value: { latestSequence: LATEST_SEQUENCE } } });
  }
  await untilAborted(ctx?.signal);
}
