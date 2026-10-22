import { describe, expect, it } from "vitest";
import { ContentBlockType } from "../../gen/reliant/v1/chat_pb";
import type { Message } from "../../types/chat";
import { processMessage } from "../messageProcessor";

// Content blocks are constructed partially; the processor only reads the
// fields set here. Cast keeps fixtures readable without full proto boilerplate.
type BlockInput = {
  type: ContentBlockType;
  index: number;
  content?: string;
  toolCallId?: string;
  toolName?: string;
  input?: string;
};

function msg(id: string, blocks: BlockInput[]): Message {
  return { id, contentBlocks: blocks } as unknown as Message;
}

function textBlock(index: number, content: string): BlockInput {
  return { type: ContentBlockType.TEXT, index, content };
}

function toolBlock(index: number, name: string, callId: string): BlockInput {
  return {
    type: ContentBlockType.TOOL_CALL,
    index,
    toolName: name,
    toolCallId: callId,
    input: "{}",
  };
}

describe("processMessage segment ordering", () => {
  it("interleaves tools between text runs in block order (the 'tools at the end' bug)", () => {
    // Mirrors the real transcript: prose, then a tool call mid-turn, then the
    // closing summary prose. The tool must render BETWEEN the two text runs,
    // not after both.
    const processed = processMessage(
      msg("m1", [
        textBlock(0, "Here's the plan.\n"),
        toolBlock(1, "bash", "call-1"),
        textBlock(2, "Done — all green."),
      ]),
    );

    expect(processed.segments.map((s) => s.kind)).toEqual([
      "text",
      "tools",
      "text",
    ]);
    const [first, tools, last] = processed.segments;
    expect(first).toMatchObject({ kind: "text", text: "Here's the plan.\n" });
    expect(last).toMatchObject({ kind: "text", text: "Done — all green." });
    if (tools.kind !== "tools") throw new Error("expected tools segment");
    expect(tools.executions.map((e) => e.call.name)).toEqual(["bash"]);
  });

  it("coalesces consecutive text blocks into one segment", () => {
    const processed = processMessage(
      msg("m2", [textBlock(0, "Hello "), textBlock(1, "world")]),
    );
    expect(processed.segments).toHaveLength(1);
    expect(processed.segments[0]).toMatchObject({
      kind: "text",
      text: "Hello world",
    });
  });

  it("coalesces consecutive tool calls into one tools segment", () => {
    const processed = processMessage(
      msg("m3", [
        toolBlock(0, "read", "c1"),
        toolBlock(1, "grep", "c2"),
      ]),
    );
    expect(processed.segments).toHaveLength(1);
    const seg = processed.segments[0];
    if (seg.kind !== "tools") throw new Error("expected tools segment");
    expect(seg.executions.map((e) => e.call.name)).toEqual(["read", "grep"]);
  });

  it("keeps the flattened text/toolExecutions views consistent with segments", () => {
    const processed = processMessage(
      msg("m4", [
        textBlock(0, "a"),
        toolBlock(1, "bash", "c1"),
        textBlock(2, "b"),
      ]),
    );
    // Flattened views (used for copy + visibility) still aggregate everything.
    expect(processed.text).toBe("ab");
    expect(processed.toolExecutions).toHaveLength(1);
    // Each execution object appears once across all tools segments.
    const inSegments = processed.segments
      .filter((s) => s.kind === "tools")
      .flatMap((s) => (s.kind === "tools" ? s.executions : []));
    expect(inSegments).toHaveLength(1);
    expect(inSegments[0]).toBe(processed.toolExecutions![0]);
  });

  it("returns an empty segment list for a message with no blocks", () => {
    const processed = processMessage(msg("m5", []));
    expect(processed.segments).toEqual([]);
    expect(processed.hasText).toBe(false);
    expect(processed.hasToolCalls).toBe(false);
  });

  it("drops empty (streaming placeholder) text but marks streaming", () => {
    const processed = processMessage(
      msg("m6", [{ type: ContentBlockType.TEXT, index: 0, content: "" }]),
    );
    expect(processed.segments).toEqual([]);
    expect(processed.isStreaming).toBe(true);
  });

  it("orders by block index even when blocks arrive out of order", () => {
    const processed = processMessage(
      msg("m7", [
        textBlock(2, "third"),
        toolBlock(1, "bash", "c1"),
        textBlock(0, "first"),
      ]),
    );
    expect(processed.segments.map((s) => s.kind)).toEqual([
      "text",
      "tools",
      "text",
    ]);
    expect(processed.segments[0]).toMatchObject({ text: "first" });
    expect(processed.segments[2]).toMatchObject({ text: "third" });
  });
});

describe("processMessage tool input", () => {
  // A tool call whose input has not streamed in yet must stay undefined.
  // Substituting a placeholder makes it indistinguishable from a call invoked
  // with no arguments, and the renderers' pending state keys off undefined.
  it("leaves input undefined while the call is still streaming", () => {
    const processed = processMessage(
      msg("s1", [
        {
          type: ContentBlockType.TOOL_CALL,
          index: 0,
          toolName: "bash",
          toolCallId: "call-1",
          // no input yet
        },
      ]),
    );

    expect(processed.toolExecutions?.[0].call.input).toBeUndefined();
    expect(processed.toolExecutions?.[0].call.finished).toBe(false);
    expect(processed.isStreaming).toBe(true);
  });

  it("parses input once it arrives", () => {
    const processed = processMessage(
      msg("s2", [
        {
          type: ContentBlockType.TOOL_CALL,
          index: 0,
          toolName: "bash",
          toolCallId: "call-1",
          input: JSON.stringify({ command: "ls -la" }),
        },
      ]),
    );

    expect(processed.toolExecutions?.[0].call.input).toEqual({ command: "ls -la" });
    expect(processed.toolExecutions?.[0].call.finished).toBe(true);
  });

  it("preserves an empty argument object as distinct from absent input", () => {
    const processed = processMessage(
      msg("s3", [
        {
          type: ContentBlockType.TOOL_CALL,
          index: 0,
          toolName: "list_tasks",
          toolCallId: "call-1",
          input: "{}",
        },
      ]),
    );

    expect(processed.toolExecutions?.[0].call.input).toEqual({});
  });
});
