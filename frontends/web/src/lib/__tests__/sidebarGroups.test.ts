import { describe, expect, it } from "vitest";

import { WorktreeStatus, type WorktreeSummary } from "../../gen/reliant/v1/worktree_pb";
import { activeWorkspaceChats, groupByProject, groupByWorkspace, type GroupableChat } from "../sidebarGroups";

const main = (projectId: string): WorktreeSummary =>
  ({
    id: `main-${projectId}`,
    name: "main",
    branch: "main",
    path: `/p/${projectId}`,
    isMain: true,
    status: WorktreeStatus.ACTIVE,
    projectId,
  }) as WorktreeSummary;

const feature = (projectId: string, id: string, status = WorktreeStatus.ACTIVE): WorktreeSummary =>
  ({
    id,
    name: id,
    branch: `feat/${id}`,
    path: `/w/${id}`,
    isMain: false,
    status,
    projectId,
  }) as WorktreeSummary;

let seq = 0;
function chat(projectId: string, worktree?: WorktreeSummary, lastMessageAt = "2025-01-01T00:00:00Z"): GroupableChat {
  seq += 1;
  return {
    id: `c${seq}`,
    projectId,
    worktreeId: worktree?.id,
    worktree,
    createdAt: lastMessageAt,
    lastMessageAt,
  };
}

const options = { sortOrder: "recent_activity" as const, needsAttention: () => false };

describe("groupByWorkspace", () => {
  it("groups one project's chats by workspace and keeps its main workspace even with no chats", () => {
    const f = feature("a", "f1");
    const onFeature = chat("a", f);
    const groups = groupByWorkspace("a", [onFeature], [main("a"), main("b")], options);
    expect(groups.map((g) => g.worktreeId)).toEqual(["main-a", "f1"]);
    expect(groups[0]!.chats).toEqual([]);
    expect(groups[1]!.chats).toEqual([onFeature]);
    expect(groups.every((g) => g.projectId === "a")).toBe(true);
  });

  it("puts a chat with no worktree in its own project's main workspace", () => {
    const noWorktree = chat("a");
    const groups = groupByWorkspace("a", [noWorktree], [main("b"), main("a")], options);
    expect(groups).toHaveLength(1);
    expect(groups[0]!.worktreeId).toBe("main-a");
    expect(groups[0]!.chats).toEqual([noWorktree]);
  });
});

describe("groupByProject", () => {
  const projects = [
    { id: "a", name: "Alpha", last_active: "2025-01-01T00:00:00Z" },
    { id: "b", name: "Beta", last_active: "2025-03-01T00:00:00Z" },
    { id: "c", name: "Gamma", last_active: "2025-02-01T00:00:00Z" },
  ];

  it("gives every project a section, ranks those with chats by their leading chat, then the rest by recency", () => {
    const older = chat("a", undefined, "2025-01-02T00:00:00Z");
    const newer = chat("c", feature("c", "fc"), "2025-01-05T00:00:00Z");
    const groups = groupByProject([older, newer], [main("a"), main("b"), main("c")], projects, options);

    expect(groups.map((g) => g.projectName)).toEqual(["Gamma", "Alpha", "Beta"]);
    expect(groups[0]!.chats).toEqual([newer]);
    expect(groups[1]!.chats).toEqual([older]);
    // A project with no chats still lists its main workspace, so a chat can start there.
    expect(groups[2]!.chats).toEqual([]);
    expect(groups[2]!.workspaces.map((w) => w.worktreeId)).toEqual(["main-b"]);
  });

  it("keeps a chat whose project is not loaded yet, under a fallback name", () => {
    const stray = chat("z");
    const groups = groupByProject([stray], [], projects, { ...options, fallbackName: "Loading…" });
    const strayGroup = groups.find((g) => g.projectId === "z");
    expect(strayGroup?.projectName).toBe("Loading…");
    expect(strayGroup?.chats).toEqual([stray]);
  });
});

describe("activeWorkspaceChats", () => {
  it("drops chats on a closed workspace but keeps one whose workspace row is gone", () => {
    const done = chat("a", feature("a", "done", WorktreeStatus.COMPLETED));
    const live = chat("a", feature("a", "live"));
    const orphan = { ...chat("a"), worktreeId: "gone" };
    expect(activeWorkspaceChats([done, live, orphan], [main("a")])).toEqual([live, orphan]);
  });
});
