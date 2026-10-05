/**
 * A spawned sub-agent's messages render only if InterleavedTimeline can
 * classify the thread, and it classifies from exactly two sources: the
 * workflow execution tree, or the chat's live thread records. With neither, it
 * logs "Thread has messages but no workflow row and no live origin" and SKIPS
 * the thread — every message on it disappears.
 *
 * These tests drive the real component. The first pins that the tree is a
 * sufficient source on its own, at the scale of the chat that surfaced the bug
 * (73 workflows, 72 spawns, one of them a resumed thread with two workflow
 * rows): the display builder was a suspect and is not at fault. The second
 * pins the classification seam the fix relies on: a thread record that names
 * the origin classifies the thread before the tree has caught up.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render } from "@testing-library/react";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import type { WorkflowExecution } from "../../ExecutionSidebar/types";
import type { ActiveThreadUpdate } from "../../../../types/streaming";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

const activeThreadsRef = vi.hoisted(() => ({ current: [] as ActiveThreadUpdate[] }));
vi.mock("../../../../store/threadActivityStore", () => ({
  useActiveThreads: () => activeThreadsRef.current,
}));

// Spawn threads collapse into their tool call in "preview" mode; render them
// inline so the assertion can see the thread's messages directly.
vi.mock("../../../Settings/SpawnDisplaySettings", () => ({
  getSpawnDisplayMode: () => "inline",
}));

vi.mock("../../ChatMessage", () => ({
  ChatMessage: ({ message }: { message: Message }) => (
    <div data-testid={`msg-${message.id}`}>{message.id}</div>
  ),
}));

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (options: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: options.count }, (_, index) => ({
        index,
        key: index,
        start: 0,
        end: 0,
        size: 0,
        lane: 0,
      })),
    getTotalSize: () => 0,
    measureElement: () => {},
    containerRef: () => {},
    scrollToIndex: () => {},
    scrollToEnd: () => {},
  }),
}));

const loggerError = vi.hoisted(() => vi.fn());
vi.mock("../../../../lib/logger", () => ({
  logger: {
    error: loggerError,
    warn: vi.fn(),
    info: vi.fn(),
    debug: vi.fn(),
    log: vi.fn(),
  },
}));

import { InterleavedTimeline } from "../InterleavedTimeline";

const CHAT = "34538e7f-520f-4b67-b8a0-4f185301ad4c";
const RESUMED_THREAD = "fb83d8e8-4961-5419-9dd2-319d4dadbdb3";
const RESUMED_SECOND_RUN = "90f88942-f029-5652-984b-ac9207b193bc";

function message(id: string, thread: string, seq: number, role = MessageRole.ASSISTANT): Message {
  return {
    id,
    chatId: CHAT,
    seq: BigInt(seq),
    thread,
    role,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [{ id: `${id}-text`, index: 0, type: ContentBlockType.TEXT, content: id }],
    createdAt: new Date(Date.UTC(2026, 9, 4, 0, 0, seq)).toISOString(),
    updatedAt: new Date(Date.UTC(2026, 9, 4, 0, 0, seq)).toISOString(),
    sequenceNumber: BigInt(seq),
  } as Message;
}

function workflow(id: string, thread: string, extra: Partial<WorkflowExecution> = {}): WorkflowExecution {
  return {
    id,
    workflowName: "builtin://agent",
    thread,
    status: "completed",
    createdAt: 0,
    messageCount: 0,
    children: [],
    steps: [],
    ...extra,
  };
}

function spawnThreadId(i: number): string {
  return `spawn-${String(i).padStart(3, "0")}`;
}

/**
 * The shape of the chat that surfaced the bug: one root, 72 children, and one
 * thread resumed under a second workflow id — two rows, one thread.
 */
function bigTree(): WorkflowExecution {
  const children: WorkflowExecution[] = [];
  for (let i = 0; i < 71; i += 1) {
    const thread = spawnThreadId(i);
    children.push(
      workflow(thread, thread, { parentId: CHAT, parentThread: CHAT, origin: "spawn", threadTitle: `agent ${i}` }),
    );
  }
  children.push(
    workflow(RESUMED_THREAD, RESUMED_THREAD, { parentId: CHAT, parentThread: CHAT, origin: "spawn", threadTitle: "first run" }),
  );
  children.push(
    workflow(RESUMED_SECOND_RUN, RESUMED_THREAD, {
      parentId: CHAT,
      parentThread: CHAT,
      origin: "spawn",
      threadTitle: "resumed run",
    }),
  );
  return workflow(CHAT, CHAT, { origin: "main", children });
}

function transcript(): Message[] {
  const messages: Message[] = [message("main-user", CHAT, 0, MessageRole.USER)];
  let seq = 1;
  for (let i = 0; i < 71; i += 1) {
    messages.push(message(`m-${spawnThreadId(i)}`, spawnThreadId(i), seq++));
  }
  messages.push({ ...message("m-resumed-1", RESUMED_THREAD, seq++), workflowId: RESUMED_THREAD } as Message);
  messages.push({ ...message("m-resumed-2", RESUMED_THREAD, seq++), workflowId: RESUMED_SECOND_RUN } as Message);
  return messages;
}

function unclassifiedThreads(): string[] {
  return loggerError.mock.calls
    .filter(([msg]) => typeof msg === "string" && msg.includes("cannot classify it"))
    .map(([, ctx]) => (ctx as { thread: string }).thread);
}

beforeEach(() => {
  loggerError.mockReset();
  activeThreadsRef.current = [];
});

describe("spawn thread classification", () => {
  it("classifies every spawn from the tree alone — 73 workflows, including a resumed thread", () => {
    const view = render(
      <InterleavedTimeline messages={transcript()} chatId={CHAT} workflowExecution={bigTree()} isStreaming={false} />,
    );

    expect(unclassifiedThreads()).toEqual([]);
    for (let i = 0; i < 71; i += 1) {
      expect(view.queryByTestId(`msg-m-${spawnThreadId(i)}`)).not.toBeNull();
    }
    // Both runs of the resumed thread render: the display is keyed by thread,
    // so the second workflow row is the same display, not a lost one.
    expect(view.queryByTestId("msg-m-resumed-1")).not.toBeNull();
    expect(view.queryByTestId("msg-m-resumed-2")).not.toBeNull();
  });

  it("drops a spawn's messages when neither the tree nor a thread record knows the thread", () => {
    // The state the timeline was left in on re-entry after the tree's cache
    // entry was garbage-collected, and on a new spawn whose first message
    // arrived before its announcement. Pinned so the log line stays honest.
    const view = render(
      <InterleavedTimeline messages={transcript()} chatId={CHAT} workflowExecution={undefined} isStreaming={false} />,
    );

    expect(unclassifiedThreads()).toContain(RESUMED_THREAD);
    expect(view.queryByTestId("msg-m-resumed-1")).toBeNull();
  });

  it("classifies a spawn from its thread record before the tree knows about it", () => {
    // A brand-new spawn: the tree predates it, and the only thing that can
    // classify it is the thread record. The server now announces the thread
    // when it CREATES it — before any message on it exists — so this record
    // is always ahead of the first message.
    const newThread = "ac40f731-788a-52d8-bf6d-6c6c80684dcf";
    activeThreadsRef.current = [
      {
        update_type: "thread",
        id: newThread,
        chat_id: CHAT,
        thread: newThread,
        workflow_id: newThread,
        is_planning_mode: false,
        status: "running",
        origin: "spawn",
        thread_title: "Fix QA findings",
        created_at: "2026-10-04T23:25:40Z",
      },
    ];

    const messages = [...transcript(), message("m-new-spawn", newThread, 500, MessageRole.USER)];
    const view = render(
      <InterleavedTimeline messages={messages} chatId={CHAT} workflowExecution={bigTree()} isStreaming />,
    );

    expect(unclassifiedThreads()).toEqual([]);
    // A user message can also be mirrored into the pinned header, so match
    // "rendered at least once" rather than exactly one node.
    expect(view.queryAllByTestId("msg-m-new-spawn").length).toBeGreaterThan(0);
  });
});
