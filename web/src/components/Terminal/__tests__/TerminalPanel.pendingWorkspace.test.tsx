import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import React from "react";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import type { Worktree } from "../../../store/worktreeStore";

// ---------------------------------------------------------------------------
// Opening the terminal on a just-created workspace started the shell in the
// PROJECT ROOT. CreateWorktree returns the row CREATING with an empty path, and
// the panel's `worktreePath || project.path` chain fell through to the main
// checkout — under the new workspace's tab, so `pwd` showed the wrong tree and
// a `git commit` would land on the wrong branch.
//
// These tests drive the real worktree/terminal/project stores and assert on the
// session the panel creates: none while the workspace has no directory, and one
// in the workspace's own directory once it settles.
// ---------------------------------------------------------------------------

vi.mock("../../../api/worktree-grpc", () => ({
  worktreeGrpc: { list: vi.fn(), create: vi.fn() },
}));

vi.mock("../../../api/terminal-grpc", () => ({
  terminalApi: { closeSession: vi.fn().mockResolvedValue({ success: true, message: "" }) },
}));

// The xterm child is not what this is about; render a marker carrying the
// working directory the session was created with.
vi.mock("../Terminal", () => ({
  Terminal: ({ sessionId, workingDir }: { sessionId: string; workingDir?: string }) =>
    React.createElement("div", { "data-testid": `terminal-${sessionId}`, "data-working-dir": workingDir }),
}));

import { TerminalPanel } from "../TerminalPanel";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useTerminalStore } from "../../../store/terminalStore";
import { useProjectStore } from "../../../store/projectStore";

const projectId = "project-1";
const projectPath = "/home/u/src/proj";
const workspacePath = "/home/u/.reliant/worktrees/proj/triggers-9c37c238";
const now = "2026-01-01T00:00:00.000Z";

function worktree(overrides: Partial<Worktree>): Worktree {
  return {
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
    ...overrides,
  };
}

const mainWorktree = worktree({});
const creatingWorktree = worktree({
  id: "wt-new",
  name: "triggers",
  path: "",
  branch: "triggers",
  is_main: false,
  status: WorktreeStatus.CREATING,
});

function sessionsFor(worktreeId: string) {
  return useTerminalStore.getState().sessions.filter((s) => s.worktreeId === worktreeId);
}

beforeEach(() => {
  useWorktreeStore.getState().reset();
  useTerminalStore.setState({
    sessions: [],
    activeSessionId: null,
    activeSessionPerWorktree: {},
    isOpen: true,
  });
  useProjectStore.setState({
    currentProject: { id: projectId, path: projectPath } as never,
  });
  useWorktreeStore.setState({
    worktrees: [mainWorktree, creatingWorktree],
    currentWorktree: creatingWorktree,
    hasLoaded: true,
  });
});

afterEach(() => {
  cleanup();
});

describe("TerminalPanel on a workspace that is still being created", () => {
  it("does not start a terminal in the project root", () => {
    render(React.createElement(TerminalPanel));

    expect(sessionsFor("wt-new")).toEqual([]);
    expect(useTerminalStore.getState().sessions.map((s) => s.workingDir)).not.toContain(projectPath);
    expect(screen.getByText(/waiting for the workspace/i)).toBeInTheDocument();
  });

  it("starts the terminal in the workspace directory once it settles", () => {
    render(React.createElement(TerminalPanel));
    expect(sessionsFor("wt-new")).toEqual([]);

    const settled = { ...creatingWorktree, path: workspacePath, status: WorktreeStatus.ACTIVE };
    act(() => {
      useWorktreeStore.setState({
        worktrees: [mainWorktree, settled],
        currentWorktree: settled,
      });
    });

    const sessions = sessionsFor("wt-new");
    expect(sessions).toHaveLength(1);
    expect(sessions[0].workingDir).toBe(workspacePath);
  });

  it("explains a failed workspace instead of opening a terminal anywhere", () => {
    const failed = { ...creatingWorktree, status: WorktreeStatus.FAILED };
    useWorktreeStore.setState({ worktrees: [mainWorktree, failed], currentWorktree: failed });

    render(React.createElement(TerminalPanel));

    expect(useTerminalStore.getState().sessions).toEqual([]);
    expect(screen.getByText(/failed to set up/i)).toBeInTheDocument();
  });

  it("does not mount a persisted terminal for an archived workspace", () => {
    const archived = worktree({
      id: "wt-archived",
      name: "archived",
      path: "/home/u/.reliant/worktrees/proj/archived",
      branch: "archived",
      is_main: false,
      deleted_at: now,
    });
    useWorktreeStore.setState({ worktrees: [mainWorktree, archived], currentWorktree: mainWorktree });
    useTerminalStore.setState({
      sessions: [{
        id: "terminal-archived",
        title: "Terminal 1",
        workingDir: archived.path,
        projectId,
        worktreeId: archived.id,
        createdAt: new Date(now),
        isActive: true,
      }],
      activeSessionId: "terminal-archived",
      activeSessionPerWorktree: { [archived.id]: "terminal-archived" },
      isOpen: true,
    });

    render(React.createElement(TerminalPanel));

    expect(screen.queryByTestId("terminal-terminal-archived")).not.toBeInTheDocument();
  });

  it("still uses the project path for the main workspace", () => {
    useWorktreeStore.setState({ currentWorktree: mainWorktree });

    render(React.createElement(TerminalPanel));

    const sessions = sessionsFor("wt-main");
    expect(sessions).toHaveLength(1);
    expect(sessions[0].workingDir).toBe(projectPath);
  });
});
