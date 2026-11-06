import { describe, expect, it } from "vitest";
import { ToolCallStatus } from "../../../gen/reliant/v1/chat_pb";
import {
  formatTurnWorkHeadline,
  summarizeTurnWork,
  type TurnWorkExecution,
} from "../turnWorkSummary";

function exec(
  name: string,
  extra: Partial<TurnWorkExecution> & { input?: Record<string, unknown> } = {},
): TurnWorkExecution {
  const { input, ...rest } = extra;
  return {
    call: { id: `${name}-${Math.random()}`, name, input: input ?? {} },
    result: { is_error: false },
    ...rest,
  };
}

describe("summarizeTurnWork", () => {
  it("buckets calls by category, splitting web from local search", () => {
    const s = summarizeTurnWork([
      exec("view"),
      exec("view"),
      exec("grep"),
      exec("websearch"),
      exec("fetch"),
      exec("shell"),
      exec("edit"),
      exec("create_plan"),
      exec("spawn"),
      exec("mcp__thing__tool"),
    ]);
    expect(s.total).toBe(10);
    expect(s.counts).toEqual({
      read: 2,
      searched: 1,
      web: 2,
      ran: 1,
      edited: 1,
      planned: 1,
      spawned: 1,
      other: 1,
    });
    expect(s.running).toBe(0);
    expect(s.failed).toBe(0);
  });

  it("names the latest finished step when nothing is running, so the live line never blanks", () => {
    expect(summarizeTurnWork([]).current).toBeUndefined();
    const s = summarizeTurnWork([
      exec("view", { input: { file_path: "src/a.ts" } }),
      exec("view", { input: { file_path: "src/ChatInput.tsx" } }),
    ]);
    expect(s.running).toBe(0);
    expect(s.current).toContain("ChatInput.tsx");
    expect(s.current).not.toMatch(/^Reading /);
  });

  it("counts failed from error results and failed status, running from unresolved calls", () => {
    const s = summarizeTurnWork([
      exec("shell", { result: { is_error: true } }),
      exec("edit", { result: undefined, status: "failed" }),
      exec("view", { result: undefined, status: "executing" }),
      exec("grep", { result: undefined, call: { id: "g", name: "grep", durableStatus: ToolCallStatus.COMPLETED } }),
      exec("shell", { result: undefined, status: "cancelled" }),
    ]);
    expect(s.failed).toBe(2);
    expect(s.running).toBe(1);
  });

  it("labels the latest running call with its live verb and argument", () => {
    const s = summarizeTurnWork([
      exec("view", { result: undefined, status: "executing", input: { path: "src/a.ts" } }),
      exec("view", {
        result: undefined,
        status: "executing",
        input: { path: "src/components/Chat/ChatInput.tsx" },
      }),
    ]);
    expect(s.running).toBe(2);
    expect(s.current).toMatch(/^Reading /);
    expect(s.current).toContain("ChatInput.tsx");
  });

  it("truncates a long label", () => {
    const s = summarizeTurnWork(
      [exec("websearch", { result: undefined, status: "executing", input: { query: "x".repeat(200) } })],
      { maxLabelLength: 20 },
    );
    expect(s.current!.length).toBeLessThanOrEqual("Searching the web for ".length + 20);
    expect(s.current!.endsWith("…")).toBe(true);
  });

  it("takes a dispatched spawn's status from its child workflow", () => {
    const spawn = (id: string): TurnWorkExecution => ({
      call: { id, name: "spawn", childWorkflowId: `wf-${id}` },
      result: { is_error: false },
      status: "completed",
    });
    const s = summarizeTurnWork([spawn("a"), spawn("b"), spawn("c"), spawn("d")], {
      spawnStatus: (call) =>
        ({ "wf-a": "executing", "wf-b": "completed", "wf-c": "failed" })[call.childWorkflowId!],
    });
    // d: unknown workflow yet -> running, like ToolExecutionGroup
    expect(s.spawnsRunning).toBe(2);
    expect(s.running).toBe(2);
    expect(s.failed).toBe(1);
  });
});

describe("formatTurnWorkHeadline", () => {
  const base = summarizeTurnWork([]);

  it("live: Working… with the current call and running agents", () => {
    const s = { ...base, current: "Reading ChatInput.tsx" };
    expect(formatTurnWorkHeadline(s, { live: true })).toBe("Working… · Reading ChatInput.tsx");
    expect(formatTurnWorkHeadline({ ...s, spawnsRunning: 2 }, { live: true })).toBe(
      "Working… · Reading ChatInput.tsx · 2 agents running",
    );
    expect(formatTurnWorkHeadline({ ...base, spawnsRunning: 1 }, { live: true })).toBe(
      "Working… · 1 agent running",
    );
    expect(formatTurnWorkHeadline(base, { live: true })).toBe("Working…");
  });

  it("done: duration and distinct nouns, zero counts omitted", () => {
    const s = summarizeTurnWork([
      ...Array.from({ length: 6 }, () => exec("view")),
      ...Array.from({ length: 3 }, () => exec("shell")),
      ...Array.from({ length: 3 }, () => exec("edit")),
    ]);
    expect(formatTurnWorkHeadline(s, { live: false, elapsedMs: 72_000 })).toBe(
      "Worked for 1m 12s · read 6 files · ran 3 commands · edited 3 files",
    );
  });

  it("pluralizes each noun", () => {
    const one = summarizeTurnWork([
      exec("view"), exec("grep"), exec("websearch"), exec("shell"),
      exec("edit"), exec("update_plan"), exec("spawn"), exec("mcp__x__y"),
    ]);
    expect(formatTurnWorkHeadline(one, { live: false, elapsedMs: 5_000 })).toBe(
      "Worked for 5s · read 1 file · 1 search · 1 web lookup · ran 1 command · edited 1 file · 1 plan update · 1 sub-agent action · used 1 tool",
    );
    const many = summarizeTurnWork([
      exec("grep"), exec("grep"), exec("websearch"), exec("fetch"),
      exec("mcp__x__y"), exec("mcp__x__z"), exec("spawn"), exec("spawn"),
    ]);
    expect(formatTurnWorkHeadline(many, { live: false, elapsedMs: 5_000 })).toBe(
      "Worked for 5s · 2 searches · 2 web lookups · 2 sub-agent actions · used 2 tools",
    );
  });

  it("omits the duration when unknown and rounds sub-second to 1s", () => {
    const s = summarizeTurnWork([exec("view")]);
    expect(formatTurnWorkHeadline(s, { live: false })).toBe("Worked · read 1 file");
    expect(formatTurnWorkHeadline(s, { live: false, elapsedMs: null })).toBe("Worked · read 1 file");
    expect(formatTurnWorkHeadline(s, { live: false, elapsedMs: 300 })).toBe("Worked for 1s · read 1 file");
    expect(formatTurnWorkHeadline(s, { live: false, elapsedMs: 3_723_000 })).toBe(
      "Worked for 1h 02m · read 1 file",
    );
  });
});
