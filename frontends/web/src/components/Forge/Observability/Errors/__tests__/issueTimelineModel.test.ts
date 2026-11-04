// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { IssueComment, IssueTimelineEntry, IssueTimelineList } from "@/services/controlPlane/observability/issuesTypes";

import {
  assigneeFilterOf,
  assigneeFilterWaitsForMe,
  assigneeParam,
  canDeleteComment,
  canEditComment,
  chatLabel,
  describeAssignment,
  describeStatus,
  entryOrigin,
  latestChatLink,
  mergeTimelinePages,
  serverAssignee,
} from "../issueTimelineModel";

const entry = (id: string, timeMs: number, issueId = "iss-a"): IssueTimelineEntry => ({
  id,
  timeMs,
  issueId,
  actor: null,
  body: { kind: "status", from: null, to: "new", version: "", ignoredUntilMs: 0 },
});
const page = (entries: IssueTimelineEntry[], issueId = "iss-a"): IssueTimelineList => ({ entries, nextPageToken: "", issueId });

describe("timeline ordering", () => {
  it("lists every page oldest first, keeping the server's order for entries at the same time", () => {
    const merged = mergeTimelinePages([page([entry("c", 300), entry("a", 100), entry("b", 100)]), page([entry("d", 200)])]);
    expect(merged.map((e) => e.id)).toEqual(["a", "b", "d", "c"]);
  });

  it("drops an entry a refetch repeated", () => {
    const merged = mergeTimelinePages([page([entry("a", 100), entry("b", 200)]), page([entry("b", 200), entry("c", 300)])]);
    expect(merged.map((e) => e.id)).toEqual(["a", "b", "c"]);
  });
});

describe("entries of a merged issue", () => {
  it("labels an entry made on another issue with where it came from, and leaves the timeline's own unlabelled", () => {
    const own = entry("1", 1, "iss-target");
    const mergedAway = entry("2", 2, "iss-old");
    expect(entryOrigin(own, "iss-target", "iss-target")).toBeNull();
    expect(entryOrigin(mergedAway, "iss-target", "iss-target")).toEqual({ issueId: "iss-old", label: "on a merged issue" });
  });

  it("says 'on this issue, before it was merged' when the reader opened the issue that was merged away", () => {
    // They opened iss-old; the server answered with iss-target's timeline.
    expect(entryOrigin(entry("2", 2, "iss-old"), "iss-target", "iss-old")).toEqual({ issueId: "iss-old", label: "on this issue, before it was merged" });
    expect(entryOrigin(entry("1", 1, "iss-target"), "iss-target", "iss-old")).toBeNull();
  });
});

describe("how entries read", () => {
  it("reads a resolve with its version, an ignore with its end, and a reopen with what it was", () => {
    const base = { kind: "status" as const, version: "", ignoredUntilMs: 0 };
    expect(describeStatus({ ...base, from: "unresolved", to: "resolved", version: "v1.8.4" })).toBe("resolved it in v1.8.4");
    expect(describeStatus({ ...base, from: "unresolved", to: "ignored" })).toBe("ignored it until it is unignored");
    expect(describeStatus({ ...base, from: "resolved", to: "unresolved" })).toBe("reopened it (it was resolved)");
    expect(describeStatus({ ...base, from: "resolved", to: "regressed" })).toBe("saw it regress");
    expect(describeStatus({ ...base, from: null, to: "new" })).toBe("opened the issue");
  });

  it("reads an assignment, a reassignment, taking it and clearing it", () => {
    const ada = { id: "u1", name: "ada" };
    const grace = { id: "u2", name: "grace" };
    const user = (u: typeof ada) => ({ kind: "user" as const, id: u.id, name: u.name });
    expect(describeAssignment(user(ada), null, grace)).toBe("assigned it to grace");
    expect(describeAssignment(user(ada), grace, ada)).toBe("took it");
    expect(describeAssignment(user(ada), grace, { id: "u3", name: "alan" })).toBe("reassigned it from grace to alan");
    expect(describeAssignment(user(ada), grace, null)).toBe("unassigned grace");
  });

  it("names a chat by its title, or by its id before a title is known", () => {
    expect(chatLabel({ chatId: "chat-4f2a91c07b", chatTitle: "Investigate timeouts" })).toBe("Investigate timeouts");
    expect(chatLabel({ chatId: "chat-4f2a91c07b", chatTitle: "  " })).toBe("chat chat-4f2");
  });
});

describe("who may touch a comment", () => {
  const comment = { id: "c1", author: { id: "u1", name: "ada" } } as IssueComment;
  const ada = { id: "u1", name: "ada" };
  const grace = { id: "u2", name: "grace" };

  it("lets only the author edit", () => {
    expect(canEditComment(comment, ada)).toBe(true);
    expect(canEditComment(comment, grace)).toBe(false);
    expect(canEditComment(comment, null)).toBe(false);
    expect(canEditComment({ ...comment, author: null }, ada)).toBe(false);
  });

  it("lets the author, or an owner or admin, delete — and nobody else", () => {
    expect(canDeleteComment(comment, ada, "member")).toBe(true);
    expect(canDeleteComment(comment, grace, "admin")).toBe(true);
    expect(canDeleteComment(comment, grace, "owner")).toBe(true);
    expect(canDeleteComment(comment, grace, "member")).toBe(false);
    // The roster is closed to them, so their role is unknown: only their own comments offer a delete.
    expect(canDeleteComment(comment, grace, undefined)).toBe(false);
  });
});

describe("the assignee filter", () => {
  it("round-trips through the URL", () => {
    for (const param of [undefined, "me", "u2"]) expect(assigneeParam(assigneeFilterOf(param))).toBe(param);
    expect(assigneeFilterOf("  ")).toEqual({ kind: "anyone" });
  });

  it("asks the server for a user id only: mine and a named person", () => {
    expect(serverAssignee({ kind: "me" }, "u1")).toBe("u1");
    expect(serverAssignee({ kind: "user", userId: "u2" }, "u1")).toBe("u2");
    expect(serverAssignee({ kind: "anyone" }, "u1")).toBeUndefined();
  });

  it("waits for the reader rather than showing everyone's when 'mine' cannot be asked yet", () => {
    expect(assigneeFilterWaitsForMe({ kind: "me" }, undefined)).toBe(true);
    expect(assigneeFilterWaitsForMe({ kind: "me" }, "u1")).toBe(false);
    expect(assigneeFilterWaitsForMe({ kind: "user", userId: "u2" }, undefined)).toBe(false);
  });
});

describe("the chat an issue leads with", () => {
  const chat = (id: string, timeMs: number, chatId: string): IssueTimelineEntry => ({
    id,
    timeMs,
    issueId: "iss-a",
    actor: null,
    body: { kind: "chat", link: { issueId: "iss-a", chatId, chatTitle: "", linkedBy: null, linkedAtMs: timeMs } },
  });

  it("is the newest chat started from it", () => {
    expect(latestChatLink([chat("1", 100, "chat-old"), entry("2", 300), chat("3", 200, "chat-new")])?.chatId).toBe("chat-new");
  });

  it("is none when no chat has been started", () => {
    expect(latestChatLink([entry("1", 100)])).toBeNull();
    expect(latestChatLink([])).toBeNull();
  });
});
