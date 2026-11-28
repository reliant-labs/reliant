// Copyright (c) 2025 Reliant Labs

/**
 * ChatService, ApprovalService, QuestionService, AttachmentService and
 * SharingService: the sidebar's chats (workspace.ts) and their transcripts
 * (data/transcripts/). The approval and the questions are also the Inbox's
 * (inbox.ts).
 *
 * THE fixture chat, CHAT_IDS.cartTotals, is three turns of a get-it-right run,
 * so one chat shows every state the transcript draws:
 *
 *   turn 1  a long finished turn with every tool category (skill, load_tool,
 *           grep, view, web fetch, a plan, edits, shell, an MCP tool), a
 *           failed shell call it recovers from, thinking blocks, two
 *           sub-agents (one done, one running), and a long answer with a
 *           long code block; then the review and verify node threads
 *   turn 2  a user message with an image and a file attachment; a final
 *           failed tool call, then a final provider error
 *   turn 3  a pending question (ask_user), a provider error that is
 *           retrying, a shell call waiting for approval, and a failed send
 *
 * Threads: Main, `review` and `verify` (graph nodes, so they are tabs), and
 * the two sub-agents (spawns, so they fold into their cards). Three more chats
 * have a transcript: a dozen tool calls (taxRates), a turn that stopped on a
 * failure (stripeSdk), and the short load test (loadTest).
 *
 * The failed send ("Also add a CHANGELOG entry for the cap.", id
 * `optimistic-…`) lives in the client alone: the server never lists it, so
 * ListMessages leaves it out and only the chat stream's snapshot carries it.
 */
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { ApprovalService, ApprovalStatus, ApprovalType } from "@/gen/reliant/v1/approval_pb";
import { AttachmentService, GetAttachmentResponseSchema } from "@/gen/reliant/v1/attachment_pb";
import { ChatService, ListMessagesResponseSchema, WorkflowState, WorkflowStopReason, type Message, type WorkflowExecutionSchema } from "@/gen/reliant/v1/chat_pb";
import { QuestionService } from "@/gen/reliant/v1/question_pb";
import { SharingService } from "@/gen/reliant/v1/sharing_pb";
import { ChatUpdateDataSchema, ChatUpdateType, type ChatUpdateData } from "@/gen/reliant/v1/streaming_pb";

import checkoutTotalJson from "../../data/attachments/checkout-total.json?raw";
import cartTotalsJson from "../../data/transcripts/cart-totals.json?raw";
import loadTestJson from "../../data/transcripts/load-test.json?raw";
import stripeSdkJson from "../../data/transcripts/stripe-sdk.json?raw";
import subAgentsJson from "../../data/transcripts/sub-agents.json?raw";
import taxRatesJson from "../../data/transcripts/tax-rates.json?raw";
import { packJson } from "../../packJson";
import { MINUTE, demoNow } from "../../shopStory";
import { CHAT_IDS, chats, projects, workflowIdOf, worktreeSummary } from "../../workspace";
import { serve } from "../serve";

const MAIN = CHAT_IDS.cartTotals;
const WF_MAIN = workflowIdOf(MAIN);
/** The fixture chat's threads (a child's thread id is its workflow id). */
export const THREADS = { review: "th-c1-review", verify: "th-c1-verify", reviewer: "th-review-1", tester: "th-c1-tests" } as const;

/** The data/transcripts file of each chat that has one. */
const TRANSCRIPTS: Record<string, unknown> = {
  [CHAT_IDS.cartTotals]: cartTotalsJson,
  [CHAT_IDS.taxRates]: taxRatesJson,
  [CHAT_IDS.stripeSdk]: stripeSdkJson,
  [CHAT_IDS.loadTest]: loadTestJson,
};

const transcript = (json: unknown): Message[] => packJson(ListMessagesResponseSchema, json).messages;
const isFailedSend = (m: Message) => m.id.startsWith("optimistic-");

/** A chat's whole transcript, every thread and the client-only failed send included; [] when it has none. */
export function chatTranscript(chatId: string): Message[] {
  const json = TRANSCRIPTS[chatId];
  return json ? transcript(json) : [];
}

/** The fixture chat's nth message's time (40s apart from 18m ago), plus `plus` ms. */
const atSeq = (seq: number, plus = 0) => new Date(demoNow() - 18 * MINUTE + seq * 40_000 + plus).toISOString();
/** The stripeSdk chat's nth message's time (40s apart from 95m ago), plus `plus` ms. */
const atStripeSeq = (seq: number, plus = 0) => new Date(demoNow() - 95 * MINUTE + seq * 40_000 + plus).toISOString();

/** The stream's sequence after the last update below: what "caught up" means. */
export const LATEST_SEQUENCE = 103n;

/** A non-message update of the snapshot: `dataJson` is the SDK event, snake_case. */
function errorUpdate(seq: bigint, chatId: string, id: string, data: Record<string, unknown>, at: string): ChatUpdateData {
  return create(ChatUpdateDataSchema, {
    updateType: ChatUpdateType.ERROR, sequenceNumber: seq, entityId: id, chatId,
    dataJson: JSON.stringify({ id, chat_id: chatId, activity_type: "V2_CallLLM", ...data, timestamp: at, workflow_id: workflowIdOf(chatId), thread: chatId }),
    createdAt: at,
  });
}

/** The errors a chat's turns hit, placed by time between its messages (the stream's otherUpdates). */
export function chatErrors(chatId: string): ChatUpdateData[] {
  if (chatId === MAIN) {
    return [
      errorUpdate(101n, MAIN, "err-c1-final", { activity_id: "act-c1-9", error_message: "anthropic: 500 api_error: Internal server error", error_summary: "The AI provider failed", attempt_number: 5, max_attempts: 5, is_retrying: false }, atSeq(10, 20_000)),
      errorUpdate(102n, MAIN, "err-c1-retry", { activity_id: "act-c1-12", error_message: "anthropic: 529 overloaded_error: Overloaded", error_summary: "The provider didn't respond", attempt_number: 2, max_attempts: 5, is_retrying: true }, atSeq(12, 20_000)),
    ];
  }
  if (chatId === CHAT_IDS.stripeSdk) {
    return [
      errorUpdate(103n, chatId, "err-fail-final", { activity_id: "act-fail-3", error_message: "anthropic: 500 api_error: Internal server error", error_summary: "The AI provider failed", attempt_number: 5, max_attempts: 5, is_retrying: false }, atStripeSeq(2, 20_000)),
    ];
  }
  return [];
}

/** A thread's messages: a node thread of the fixture chat, or a sub-agent's (data/transcripts/sub-agents.json). */
function threadMessages(chatId: string, thread: string): Message[] {
  const own = chatTranscript(chatId).filter((m) => m.thread === thread);
  return own.length || chatId !== MAIN ? own : transcript(subAgentsJson).filter((m) => m.thread === thread);
}

type ExecutionInit = MessageInitShape<typeof WorkflowExecutionSchema>;

function workflowTree(): ExecutionInit[] {
  const node = (id: string, thread: string, o: ExecutionInit = {}): ExecutionInit => ({
    id, workflowName: "builtin://agent", thread, createdAt: atSeq(1), messageCount: 0, children: [], steps: [],
    origin: "main", state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED, ...o,
  });
  const running = { state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED };
  const child = { parentId: WF_MAIN, parentThread: MAIN };
  return [
    node(WF_MAIN, MAIN, {
      workflowName: "builtin://get-it-right", ...running,
      children: [
        node("wf-c1-review", THREADS.review, { origin: "node", originNodeId: "review", spawnedByNodeId: "review", threadTitle: "review", ...child }),
        node("wf-c1-verify", THREADS.verify, { origin: "node", originNodeId: "verify", spawnedByNodeId: "verify", threadTitle: "verify", ...child }),
        node(THREADS.reviewer, THREADS.reviewer, { origin: "spawn", threadTitle: "Review discount fix", spawnedByNodeId: "implement", ...child }),
        node(THREADS.tester, THREADS.tester, { origin: "spawn", threadTitle: "Write edge-case tests", spawnedByNodeId: "implement", ...child, ...running }),
      ],
    }),
  ];
}

const ASK_USER = {
  type: "ask_user",
  questions: [{
    question: "A flat code larger than a bundle: cap it at the bundle price, or reject the code?",
    options: [{ label: "Cap at the bundle price", description: "The total never goes negative" }, { label: "Reject the code", description: "Checkout shows an error" }],
  }],
};

/** The rate-limit chat's blocking question: the run waits on it (the chat is awaiting input). */
const ASK_RATE_LIMIT = {
  type: "ask_user",
  questions: [{
    question: "How many /checkout requests a minute should each client get?",
    options: [
      { label: "60 per IP, 600 per API key", description: "Anonymous shoppers are held tighter than integrations" },
      { label: "120 per IP, 1200 per API key", description: "Twice the headroom for flash sales" },
    ],
  }],
};

/** What the chat shows, and the Inbox lists: the fixture chat's shell call waiting for approval. */
export const cartTotalsApproval = () => ({
  id: "appr-c1-push", chatId: MAIN, approvalType: ApprovalType.TOOL_CALL, entityId: "blk-c1-approve",
  status: ApprovalStatus.PENDING, title: "Run shell command", description: "git push origin fix/per-unit-discounts",
  createdAt: atSeq(13), messageId: "lm-13", workflowId: WF_MAIN,
});

/** The fixture chat's async question (ask_user without blocking): the agent kept working. */
export const cartTotalsQuestion = () => ({
  questionId: "q-c1-cap", chatId: MAIN, workflowId: WF_MAIN, stepId: "implement", status: "pending", createdAt: atSeq(12),
  metadata: JSON.stringify(ASK_USER), threadId: MAIN, async: true, toolCallId: "ask1",
});

/** The rate-limit chat's pending question, asked two minutes ago. */
export const rateLimitQuestion = () => ({
  questionId: "q-rl-limits", chatId: CHAT_IDS.rateLimit, workflowId: workflowIdOf(CHAT_IDS.rateLimit), stepId: "agent", status: "pending",
  createdAt: new Date(demoNow() - 2 * MINUTE).toISOString(), metadata: JSON.stringify(ASK_RATE_LIMIT), threadId: CHAT_IDS.rateLimit,
  async: false, toolCallId: "ask-rl-1",
});

/** A chat's projects: every chat works in its own project alone (the `chat-attached-projects` overlay attaches docs). */
function chatProjects(req: { chatId: string }) {
  const chat = chats().find((c) => c.id === req.chatId);
  if (!chat) return { projects: [] };
  const project = projects().find((p) => p.id === chat.projectId)!;
  return { projects: [{ projectId: project.id, name: project.name, primary: true, daemonId: chat.activeDaemonId, path: project.path, attachedAt: "" }] };
}

export function chatHandlers() {
  return {
    ...serve(ChatService, {
      listChats: (req) => {
        // Honours the project filter, one id or several; none is every project.
        const wanted = new Set([req.projectId, ...req.projectIds].filter(Boolean));
        const list = chats().filter((c) => !wanted.size || wanted.has(c.projectId));
        const mainWorktrees = [worktreeSummary("main"), worktreeSummary("docsMain")].filter((w) => !wanted.size || wanted.has(w.projectId));
        return { chats: list, total: list.length, lastUserUpdateSequence: 10n, mainWorktrees };
      },
      listArchivedChats: () => ({ chats: [], total: 0 }),
      getChat: (req) => {
        const chat = chats().find((c) => c.id === req.chatId);
        if (!chat) throw new ConnectError("chat not found", Code.NotFound);
        return { chat };
      },
      listMessages: (req) => {
        const thread = req.threadId;
        const messages = thread && thread !== req.chatId ? threadMessages(req.chatId, thread) : chatTranscript(req.chatId).filter((m) => !isFailedSend(m));
        return { messages, total: messages.length, count: messages.length, hasMore: false, oldestSeq: 1n };
      },
      getWorkflowExecutions: (req) => ({ allRootWorkflows: req.chatId === MAIN ? workflowTree() : [] }),
      // No node history is recorded for any run: the run page shows its nodes from the tree alone.
      getWorkflowNodeExecutions: () => ({ updates: [] }),
      // A thread's workflow (the fixture chat's tree names its threads; any other chat is its one
      // thread), run with its defaults: nobody set an input.
      getThreadWorkflowInputs: (req) => {
        const thread = req.threadId || req.chatId;
        const flat = (nodes: ExecutionInit[]): ExecutionInit[] => nodes.flatMap((n) => [n, ...flat((n.children ?? []) as ExecutionInit[])]);
        const node = req.chatId === MAIN ? flat(workflowTree()).find((n) => n.thread === thread) : undefined;
        const chat = thread === req.chatId ? chats().find((c) => c.id === req.chatId) : undefined;
        if (!node && !chat) throw new ConnectError("thread not found", Code.NotFound);
        const [workflowName, state] = node ? [node.workflowName ?? "", node.state] : [chat!.workflowName, chat!.workflowState];
        return { workflowName, inputs: {}, isRunning: state === WorkflowState.ACTIVE, resumable: false };
      },
      listChatProjects: chatProjects,
      // Nothing is queued for an agent: every message Alex sent has been delivered.
      listQueuedAgentMessages: () => ({ messages: [] }),
    }),
    // Nobody has shared a chat with the org yet.
    ...serve(SharingService, { listSharedChats: () => ({ chats: [] }) }),
    ...serve(ApprovalService, {
      listApprovalsByChat: (req) => (req.chatId === MAIN ? { approvals: [cartTotalsApproval()], total: 1 } : { approvals: [], total: 0 }),
    }),
    ...serve(QuestionService, {
      listAsyncQuestions: (req) => ({ questions: req.chatId === MAIN ? [cartTotalsQuestion()] : [] }),
      // The rate-limit chat waits on its question; an async question is never pending here.
      listPendingQuestions: (req) => ({ questions: !req.chatId || req.chatId === CHAT_IDS.rateLimit ? [rateLimitQuestion()] : [] }),
    }),
    ...serve(AttachmentService, {
      // The fixture chat's turn 2 image ("checkout-total.png"); its markdown spec is listed on the message, never fetched.
      getAttachment: (req) => {
        const answer = packJson(GetAttachmentResponseSchema, checkoutTotalJson);
        if (req.id !== answer.attachment?.id) throw new ConnectError("attachment not found", Code.NotFound);
        return answer;
      },
    }),
  };
}
