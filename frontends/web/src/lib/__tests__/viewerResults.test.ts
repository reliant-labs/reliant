/**
 * A viewer of a shared chat never sees a tool's result, however it arrived:
 * elided (the server's answer) or whole (an older server, a cache filled
 * before the share). Only the built-ins whose results are the conversation's
 * own state are kept, and that list is the server's.
 */
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import type { ProcessedMessage, ToolExecution } from "../messageProcessor";
import { VIEWER_KEPT_RESULT_TOOLS, viewerSafeMessage, viewerSafeResult } from "../viewerResults";

const whole = {
  tool_call_id: "t1",
  name: "shell",
  content: "SECRET=hunter2",
  metadata: '{"exit":0}',
  attachments: [{ id: "a1" }] as never[],
  truncated: true,
  original_size: 99,
  block_id: "b1",
};

describe("viewerSafeResult", () => {
  it("leaves a non-viewer's result alone", () => {
    expect(viewerSafeResult(whole, "shell", false)).toBe(whole);
  });

  it("removes every part of a result a viewer was sent whole", () => {
    const safe = viewerSafeResult(whole, "shell", true);
    expect(safe).toMatchObject({ tool_call_id: "t1", name: "shell", content: "", elided: true });
    expect(JSON.stringify(safe)).not.toContain("hunter2");
    expect(safe?.metadata).toBeUndefined();
    expect(safe?.attachments).toBeUndefined();
    expect(safe?.block_id).toBeUndefined();
  });

  it("keeps a plan or task tool's result whole, by exact name only", () => {
    const plan = { ...whole, name: "create_plan", content: "the plan" };
    expect(viewerSafeResult(plan, "create_plan", true)).toBe(plan);
    expect(viewerSafeResult(plan, "mcp__evil__create_plan", true)?.content).toBe("");
    expect(viewerSafeResult(plan, "Create_Plan", true)?.content).toBe("");
  });

  it("is the server's kept list, exactly", () => {
    const go = readFileSync(resolve(__dirname, "../../../../../internal/messageproto/viewer.go"), "utf8");
    const block = go.match(/var viewerKeptResultTools = map\[string\]bool\{([\s\S]*?)\n\}/);
    expect(block).not.toBeNull();
    const server = [...block![1].matchAll(/"([^"]+)":\s*true/g)].map((m) => m[1]).sort();
    expect([...VIEWER_KEPT_RESULT_TOOLS].sort()).toEqual(server);
  });
});

describe("viewerSafeMessage", () => {
  it("removes the result from every view of the message", () => {
    const execution = { call: { id: "t1", name: "shell", input: {} }, result: whole } as unknown as ToolExecution;
    const processed = {
      id: "m1",
      toolExecutions: [execution],
      segments: [{ kind: "text", text: "hi" }, { kind: "tools", executions: [execution] }],
      hasToolCalls: true,
      hasText: true,
      isStreaming: false,
    } as ProcessedMessage;
    expect(viewerSafeMessage(processed, false)).toBe(processed);
    const safe = viewerSafeMessage(processed, true);
    expect(JSON.stringify(safe)).not.toContain("hunter2");
    expect(safe.segments[0]).toEqual({ kind: "text", text: "hi" });
  });
});
