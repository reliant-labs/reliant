/**
 * The background-work strip above the composer.
 *
 * An async spawn is launched by a tool call that scrolls up and out of the
 * timeline, so once the transcript grows there is no way to see that a
 * sub-agent is still running or to get back to it. This strip is that surface,
 * and the behaviors pinned here are the ones where being wrong misleads the
 * user about what the system is doing:
 *
 *  - a RUNNING spawn is visible and reachable (clicking focuses its thread).
 *  - a spawn that is BLOCKED on a pending question is called out distinctly,
 *    and attributed to the ONE spawn that asked — the whole point of the
 *    strip is answering "who needs me", so lighting up every running child
 *    would make the signal useless.
 *  - thread records OUTLIVE the run (they carry titles for the timeline), so
 *    the strip must go away when the chat goes idle rather than claiming
 *    agents are still working.
 *  - non-spawn threads (forks, workflow nodes) do NOT appear. Those belong to
 *    ThreadTabs; mixing them here was explicitly not wanted.
 *  - background commands are chat-attributed when the daemon set a chat_id,
 *    so another chat's dev server does not sit in your strip forever.
 */

import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import type { ActiveThreadUpdate } from "../../../types/streaming";

const CHAT_ID = "chat-abc";
const WORKTREE_ID = "wt-1";

const { usePendingQuestionMock } = vi.hoisted(() => ({
  usePendingQuestionMock: vi.fn(),
}));

vi.mock("../../../hooks/approval-queries", () => ({
  usePendingQuestion: usePendingQuestionMock,
}));

import { useThreadActivityStore } from "../../../store/threadActivityStore";
import { useProcessStore } from "../../../store/processStore";
import { useActivityStore } from "../../../store/activityStore";
import { ChatActivity } from "../../../gen/reliant/v1/chat_pb";
import { BackgroundProcessStatus } from "../../../api/background-grpc";
import { BackgroundWorkPill } from "../BackgroundWorkPill";

function thread(overrides: Partial<ActiveThreadUpdate> = {}): ActiveThreadUpdate {
  return {
    update_type: "thread",
    id: "t-1",
    chat_id: CHAT_ID,
    thread: "thread-1",
    workflow_id: "wf-1",
    origin: "spawn",
    status: "running",
    is_planning_mode: false,
    thread_title: "researcher",
    current_activity: "CallLLM",
    created_at: new Date().toISOString(),
    ...overrides,
  };
}

function setChatRunning(running: boolean) {
  useActivityStore
    .getState()
    .setActivity(CHAT_ID, running ? ChatActivity.RUNNING : ChatActivity.IDLE);
}

function renderPill(props: Partial<React.ComponentProps<typeof BackgroundWorkPill>> = {}) {
  return renderWithQuery(
    <BackgroundWorkPill
      chatId={CHAT_ID}
      worktreeId={WORKTREE_ID}
      {...props}
    />,
  );
}

beforeEach(() => {
  usePendingQuestionMock.mockReturnValue({ data: null });
  useThreadActivityStore.getState().clearAll();
  useProcessStore.getState().reset();
  setChatRunning(true);
});

describe("BackgroundWorkPill", () => {
  it("renders nothing when there is no background work", () => {
    renderPill();
    expect(screen.queryByTestId("background-work-pill")).toBeNull();
  });

  it("surfaces a running spawn and focuses its thread when clicked", () => {
    useThreadActivityStore
      .getState()
      .setThreads(CHAT_ID, [thread({ thread: "thread-42" })]);

    const onSelectThread = vi.fn();
    renderPill({ onSelectThread });

    expect(screen.getByTestId("background-work-pill")).toHaveTextContent("1 agent");

    fireEvent.click(screen.getByTestId("background-work-pill"));
    fireEvent.click(screen.getByTestId("background-work-spawn-thread-42"));

    expect(onSelectThread).toHaveBeenCalledWith("thread-42");
  });

  it("calls out the specific spawn blocked on a pending question", () => {
    useThreadActivityStore.getState().setThreads(CHAT_ID, [
      thread({ thread: "thread-a", workflow_id: "wf-a", thread_title: "researcher" }),
      thread({ thread: "thread-b", workflow_id: "wf-b", thread_title: "implementer" }),
    ]);
    // The question names the workflow that asked it; only that spawn is gated.
    usePendingQuestionMock.mockReturnValue({
      data: { question_id: "q1", chat_id: CHAT_ID, workflow_id: "wf-b", step_id: "s1", status: "pending", created_at: "" },
    });

    renderPill();

    const pill = screen.getByTestId("background-work-pill");
    expect(pill).toHaveTextContent("implementer waiting on you");
    expect(pill).not.toHaveTextContent("researcher waiting on you");
  });

  it("keeps showing a running spawn after the chat goes idle", () => {
    // The whole point of an async spawn is outliving the turn that launched
    // it: the parent finishes its message and the chat goes IDLE while the
    // children keep working. An earlier version gated this on the chat being
    // RUNNING, which hid exactly the case the pill exists for — six agents
    // working against a chat reporting idle, with no way to reach them.
    useThreadActivityStore.getState().setThreads(CHAT_ID, [thread()]);
    setChatRunning(false);

    renderPill();

    expect(screen.getByTestId("background-work-pill")).toHaveTextContent("1 agent");
  });

  it("drops a spawn once its own thread reports terminal", () => {
    // Thread status is the authority now that the chat-level gate is gone,
    // so a completed child must leave the list on its own.
    useThreadActivityStore
      .getState()
      .setThreads(CHAT_ID, [thread({ status: "completed" })]);

    renderPill();

    expect(screen.queryByTestId("background-work-pill")).toBeNull();
  });

  it("ignores non-spawn threads, which belong to ThreadTabs", () => {
    useThreadActivityStore
      .getState()
      .setThreads(CHAT_ID, [thread({ origin: "node", thread: "thread-node" })]);

    renderPill();

    expect(screen.queryByTestId("background-work-pill")).toBeNull();
  });

  it("shows a running command for this chat and excludes another chat's", () => {
    useProcessStore.setState({
      processes: [
        {
          id: "proc-mine",
          command: "npm run dev",
          status: BackgroundProcessStatus.RUNNING,
          start_time: new Date().toISOString(),
          working_dir: "/tmp",
          session_id: "s",
          worktree_id: WORKTREE_ID,
          chat_id: CHAT_ID,
        },
        {
          id: "proc-other",
          command: "npm run other",
          status: BackgroundProcessStatus.RUNNING,
          start_time: new Date().toISOString(),
          working_dir: "/tmp",
          session_id: "s",
          worktree_id: WORKTREE_ID,
          chat_id: "some-other-chat",
        },
      ],
    });

    const onSelectCommand = vi.fn();
    renderPill({ onSelectCommand });

    expect(screen.getByTestId("background-work-pill")).toHaveTextContent("1 command");

    fireEvent.click(screen.getByTestId("background-work-pill"));
    expect(screen.getByTestId("background-work-command-proc-mine")).toBeTruthy();
    expect(screen.queryByTestId("background-work-command-proc-other")).toBeNull();

    fireEvent.click(screen.getByTestId("background-work-command-proc-mine"));
    expect(onSelectCommand).toHaveBeenCalledWith("proc-mine");
  });
});
