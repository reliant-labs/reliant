// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Issue } from "@/services/controlPlane/observability/issuesTypes";

import { conflictSummary, frameText, ignoreChange, resolveChange, triageActionsOf } from "../issueModel";

describe("triage rules", () => {
  it("offers resolve and ignore on an open issue, reopen on a closed one", () => {
    expect(triageActionsOf("new")).toEqual(["resolve", "ignore"]);
    expect(triageActionsOf("regressed")).toEqual(["resolve", "ignore"]);
    expect(triageActionsOf("resolved")).toEqual(["unresolve"]);
    expect(triageActionsOf("ignored")).toEqual(["unresolve"]);
  });

  it("turns a resolve choice into a request, and refuses 'in version' without one", () => {
    expect(resolveChange("now", "")).toEqual({ status: "resolved", resolveIn: { kind: "now" } });
    expect(resolveChange("next_release", "")).toEqual({ status: "resolved", resolveIn: { kind: "next_release" } });
    expect(resolveChange("version", "  v1.8.4 ")).toEqual({ status: "resolved", resolveIn: { kind: "version", version: "v1.8.4" } });
    expect(resolveChange("version", " ")).toBeNull();
  });

  it("ends an ignore after the chosen span, or never", () => {
    expect(ignoreChange("1h", 1000)).toEqual({ status: "ignored", untilMs: 1000 + 3_600_000 });
    expect(ignoreChange("forever", 1000)).toEqual({ status: "ignored" });
  });

  it("says what changed in a conflict", () => {
    const read = { status: "unresolved", resolvedVersion: "" } as Issue;
    expect(conflictSummary(read, { ...read, status: "resolved", resolvedVersion: "v1.8.4" })).toBe("It is resolved in v1.8.4 now, not unresolved.");
    expect(conflictSummary(read, { ...read, status: "ignored" })).toBe("It is ignored now, not unresolved.");
    expect(conflictSummary(read, read)).toMatch(/changed since you opened it/);
  });

  it("reads a frame as function in file:line:col", () => {
    expect(frameText({ function: "render", file: "a.tsx", line: 4, column: 2 })).toBe("render in a.tsx:4:2");
    expect(frameText({ function: "", file: "bundle.js", line: 1, column: 0 })).toBe("bundle.js:1");
    expect(frameText({ function: "", file: "", line: 0, column: 0 })).toBe("(unknown frame)");
  });
});

describe("a conflict on the assignee", () => {
  const read = { status: "unresolved", resolvedVersion: "", assignee: null } as unknown as Issue;
  it("says who it is assigned to now", () => {
    expect(conflictSummary(read, { ...read, assignee: { id: "u3", name: "alan" } })).toBe("It is assigned to alan now, not unassigned.");
    const assigned = { ...read, assignee: { id: "u1", name: "ada" } } as Issue;
    expect(conflictSummary(assigned, { ...assigned, assignee: null })).toBe("It is unassigned now, not assigned to ada.");
  });
});
