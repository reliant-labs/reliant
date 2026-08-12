import { describe, expect, it } from "vitest";
import { ContentBlockType, MessageRole } from "../../gen/reliant/v1/chat_pb";
import { formatTranscript, messageText } from "../transcript";
import type { Message } from "../../api/client";

/** Minimal message stub — only the fields the formatter reads. */
function message(
  role: MessageRole,
  blocks: Array<{ type: ContentBlockType; content?: string }>,
): Message {
  return {
    id: `m-${Math.random()}`,
    role,
    contentBlocks: blocks,
  } as unknown as Message;
}

const text = (content: string) => ({
  type: ContentBlockType.TEXT,
  content,
});

describe("messageText", () => {
  it("joins text blocks", () => {
    const msg = message(MessageRole.USER, [text("Hello "), text("world")]);
    expect(messageText(msg)).toBe("Hello world");
  });

  it("ignores non-text blocks", () => {
    const msg = message(MessageRole.ASSISTANT, [
      text("Answer"),
      { type: ContentBlockType.TOOL_CALL, content: "{}" },
    ]);
    expect(messageText(msg)).toBe("Answer");
  });

  it("handles a message with no blocks", () => {
    expect(messageText(message(MessageRole.USER, []))).toBe("");
  });
});

describe("formatTranscript", () => {
  it("formats a conversation as Markdown", () => {
    const transcript = formatTranscript([
      message(MessageRole.USER, [text("How do I run tests?")]),
      message(MessageRole.ASSISTANT, [text("Use npm test.")]),
    ]);

    expect(transcript).toBe(
      "### You\n\nHow do I run tests?\n\n### Assistant\n\nUse npm test.\n",
    );
  });

  it("includes a title when given one", () => {
    const transcript = formatTranscript(
      [message(MessageRole.USER, [text("Hi")])],
      { title: "My Chat" },
    );

    expect(transcript.startsWith("# My Chat\n\n")).toBe(true);
  });

  it("omits tool messages", () => {
    // Tool traffic is the model's working; pasting it is never what someone
    // means by "copy the conversation".
    const transcript = formatTranscript([
      message(MessageRole.USER, [text("Read the file")]),
      message(MessageRole.TOOL, [text("file contents here")]),
      message(MessageRole.ASSISTANT, [text("Done.")]),
    ]);

    expect(transcript).not.toContain("file contents here");
    expect(transcript).toContain("Read the file");
    expect(transcript).toContain("Done.");
  });

  it("skips turns with no prose", () => {
    const transcript = formatTranscript([
      message(MessageRole.USER, [text("Question")]),
      message(MessageRole.ASSISTANT, [
        { type: ContentBlockType.TOOL_CALL, content: "{}" },
      ]),
    ]);

    expect(transcript).toBe("### You\n\nQuestion\n");
  });

  it("returns an empty string when nothing is quotable", () => {
    // Callers use this to tell "no transcript" from "a transcript of blanks".
    expect(formatTranscript([])).toBe("");
    expect(formatTranscript([message(MessageRole.TOOL, [text("x")])])).toBe("");
  });

  it("trims surrounding whitespace from each turn", () => {
    const transcript = formatTranscript([
      message(MessageRole.USER, [text("  padded  ")]),
    ]);

    expect(transcript).toBe("### You\n\npadded\n");
  });
});
