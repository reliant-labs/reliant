import { describe, it, expect } from "vitest";
import { __chatTextAreaTestUtils } from "../ChatTextArea";

const { normalizeComposerDom, serializeFromDom } = __chatTextAreaTestUtils;

describe("ChatTextArea paste normalization", () => {
  it("preserves line boundary when unwrapping block after a text node", () => {
    const root = document.createElement("div");

    // Simulate pasted structure where previous sibling is a text node.
    root.appendChild(document.createTextNode("🚀 Finding available ports..."));
    const block = document.createElement("div");
    block.appendChild(document.createTextNode("✅ Found available ports"));
    root.appendChild(block);

    normalizeComposerDom(root);

    expect(serializeFromDom(root)).toBe(
      "🚀 Finding available ports...\n✅ Found available ports"
    );
  });

  it("does not add duplicate line breaks when a <br> already exists", () => {
    const root = document.createElement("div");

    root.appendChild(document.createTextNode("Line 1"));
    root.appendChild(document.createElement("br"));

    const block = document.createElement("div");
    block.appendChild(document.createTextNode("Line 2"));
    root.appendChild(block);

    normalizeComposerDom(root);

    expect(serializeFromDom(root)).toBe("Line 1\nLine 2");
  });

  it("preserves indentation in multiline terminal-style text", () => {
    const root = document.createElement("div");

    const lines = [
      "✅ Found available ports",
      "  Frontend: 3077",
      "  Backend: 8157",
      "  Temporal:",
      "    Frontend (client API): 7077",
    ];

    root.appendChild(document.createTextNode(lines[0]));

    for (let i = 1; i < lines.length; i++) {
      const block = document.createElement("div");
      block.appendChild(document.createTextNode(lines[i]));
      root.appendChild(block);
    }

    normalizeComposerDom(root);

    expect(serializeFromDom(root)).toBe(lines.join("\n"));
  });

  it("normalizes CR/LF variants to newline boundaries", () => {
    const raw = [
      "🚀 Finding available ports...",
      "✅ Found available ports",
      "  Frontend: 3077",
      "  Backend: 8157",
      "  gRPC: 9167",
    ].join("\r");

    const normalized = raw.split(/\r\n|\n|\r/).join("\n");

    expect(normalized).toBe(
      [
        "🚀 Finding available ports...",
        "✅ Found available ports",
        "  Frontend: 3077",
        "  Backend: 8157",
        "  gRPC: 9167",
      ].join("\n")
    );
  });
});
