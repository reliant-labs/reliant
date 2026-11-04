import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { act, cleanup, render, screen } from "@testing-library/react";
import React from "react";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";

// ---------------------------------------------------------------------------
// Prod, 2026-11-04: the main workspace's terminal had been saved before
// terminals recorded their chat. With no chat, the server opened it on
// whichever of the user's three machines it picked — not the chat's — and
// nothing on screen said which. A terminal shown beside a chat now belongs to
// that chat (so it opens on the chat's machine), and the panel names the
// machine the shown terminal is on.
// ---------------------------------------------------------------------------

setTransport(mockTransport({ "TerminalService.CloseSession": { response: { success: true, message: "" } } }));

vi.mock("../Terminal", () => ({
  Terminal: ({ sessionId, chatId }: { sessionId: string; chatId?: string }) =>
    React.createElement("div", { "data-testid": `terminal-${sessionId}`, "data-chat-id": chatId ?? "" }),
}));

import { TerminalPanel } from "../TerminalPanel";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useTerminalStore } from "../../../store/terminalStore";
import { useProjectStore } from "../../../store/projectStore";
import { useChatStore } from "../../../store/chatStore";
import { type Worktree } from "../../../hooks/worktree-queries";
import { setWorktreesForTest } from "../../../test/worktreeCache";
import { withAppQueryClient } from "../../../test/withAppQueryClient";
import { queryClient } from "../../../lib/query-client";

const projectId = "project-1";
const projectPath = "/home/workspace/projects/reliant-labs";
const now = "2026-01-01T00:00:00.000Z";

const mainWorktree: Worktree = {
  id: "wt-main",
  name: "Main workspace",
  path: projectPath,
  branch: "main",
  base_branch: "main",
  project_id: projectId,
  status: WorktreeStatus.ACTIVE,
  is_main: true,
  created_at: now,
  updated_at: now,
  last_active: now,
  deleted_at: null,
};

beforeEach(() => {
  useWorktreeStore.getState().reset();
  useProjectStore.setState({ currentProject: { id: projectId, path: projectPath } as never });
  setWorktreesForTest({ worktrees: [mainWorktree], currentWorktree: mainWorktree });
  useChatStore.setState({ activeChatId: "chat-on-default" } as never);
  // A terminal saved before terminals recorded their chat.
  useTerminalStore.setState({
    sessions: [
      {
        id: "terminal-legacy",
        title: "Terminal 1",
        workingDir: projectPath,
        projectId,
        worktreeId: mainWorktree.id,
        createdAt: new Date(now),
        isActive: true,
      },
    ],
    activeSessionId: "terminal-legacy",
    activeSessionPerWorktree: { [mainWorktree.id]: "terminal-legacy" },
    isOpen: true,
  });
});

afterEach(() => {
  cleanup();
  queryClient.clear();
});

describe("TerminalPanel machine", () => {
  it("gives a terminal saved without a chat the chat it is shown beside", () => {
    render(withAppQueryClient(React.createElement(TerminalPanel)));

    expect(useTerminalStore.getState().sessions[0].chatId).toBe("chat-on-default");
    expect(screen.getByTestId("terminal-terminal-legacy").getAttribute("data-chat-id")).toBe("chat-on-default");
  });

  it("keeps a terminal's own chat", () => {
    useTerminalStore.setState((s) => ({ sessions: s.sessions.map((x) => ({ ...x, chatId: "chat-on-cicd" })) }));

    render(withAppQueryClient(React.createElement(TerminalPanel)));

    expect(useTerminalStore.getState().sessions[0].chatId).toBe("chat-on-cicd");
  });

  it("names the machine the shown terminal is on", () => {
    render(withAppQueryClient(React.createElement(TerminalPanel)));
    expect(screen.queryByTestId("terminal-machine")).toBeNull();

    act(() => {
      useTerminalStore.getState().setSessionMachine("terminal-legacy", "2aab1465", "default");
    });

    expect(screen.getByTestId("terminal-machine").textContent).toBe("on default");
  });

  it("does not persist the machine, which is the connection's", () => {
    useTerminalStore.getState().setSessionMachine("terminal-legacy", "2aab1465", "default");
    const persisted = JSON.parse(localStorage.getItem("terminal-store") ?? "{}");
    expect(persisted.state.sessions[0].machineName).toBeUndefined();
    expect(persisted.state.sessions[0].daemonId).toBeUndefined();
  });
});
