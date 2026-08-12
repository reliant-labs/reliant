import { describe, it, expect, afterEach } from "vitest";
import { __chatTextAreaTestUtils } from "../ChatTextArea";

const { normalizeComposerDom, serializeFromDom, insertPlainTextAtCursor } =
  __chatTextAreaTestUtils;

/**
 * Mount a composer root. Caret APIs only work on a node that is in the
 * document, so these cases cannot use a detached div — `withRoot` makes sure
 * the node comes back out again afterwards.
 */
const mountedRoots: HTMLElement[] = [];
function mountRoot(): HTMLElement {
  const root = document.createElement("div");
  document.body.appendChild(root);
  mountedRoots.push(root);
  return root;
}

afterEach(() => {
  // Leaving a mounted node — or a live Selection pointing into one — behind
  // corrupts unrelated suites that share this jsdom environment.
  window.getSelection()?.removeAllRanges();
  while (mountedRoots.length > 0) mountedRoots.pop()!.remove();
});

/** Put a collapsed caret at `offset` within `node`. */
function placeCaret(node: Node, offset: number) {
  const range = document.createRange();
  range.setStart(node, offset);
  range.collapse(true);
  const sel = window.getSelection()!;
  sel.removeAllRanges();
  sel.addRange(range);
}

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

  it("treats a lone trailing <br> as scaffolding, not a newline", () => {
    // Browsers park a <br> after the last line so the caret has somewhere to
    // sit; it renders no extra line.
    const root = document.createElement("div");
    root.appendChild(document.createTextNode("a"));
    root.appendChild(document.createElement("br"));

    expect(serializeFromDom(root)).toBe("a");
  });

  it("reports a trailing newline once a second <br> follows", () => {
    // Shift+Enter at the end of the text produces "a<br><br>": the first break
    // is the newline the user asked for, the second is the scaffolding that
    // renders the now-empty final line. Collapsing both is what made a
    // Shift+Enter right after a paste look like it did nothing.
    const root = document.createElement("div");
    root.appendChild(document.createTextNode("a"));
    root.appendChild(document.createElement("br"));
    root.appendChild(document.createElement("br"));

    expect(serializeFromDom(root)).toBe("a\n");
  });

  it("preserves deliberate blank lines at the end of a paste", () => {
    const root = document.createElement("div");
    root.appendChild(document.createTextNode("a"));
    root.appendChild(document.createElement("br"));
    root.appendChild(document.createElement("br"));
    root.appendChild(document.createElement("br"));

    expect(serializeFromDom(root)).toBe("a\n\n");
  });

  it("does not treat a <br> between text as trailing scaffolding", () => {
    const root = document.createElement("div");
    root.appendChild(document.createTextNode("a"));
    root.appendChild(document.createElement("br"));
    root.appendChild(document.createTextNode("b"));

    expect(serializeFromDom(root)).toBe("a\nb");
  });

  it("drops only one block-wrapper boundary newline", () => {
    // A block wrapper contributes a closing "\n" that ends the last line
    // rather than starting a new one; an explicit empty block after it is a
    // real blank line and must survive.
    const withOneBlock = document.createElement("div");
    const block = document.createElement("div");
    block.appendChild(document.createTextNode("only line"));
    withOneBlock.appendChild(block);
    expect(serializeFromDom(withOneBlock)).toBe("only line");

    const withBlankBlock = document.createElement("div");
    const first = document.createElement("div");
    first.appendChild(document.createTextNode("first"));
    const blank = document.createElement("div");
    blank.appendChild(document.createElement("br"));
    withBlankBlock.appendChild(first);
    withBlankBlock.appendChild(blank);
    expect(serializeFromDom(withBlankBlock)).toBe("first\n");
  });

  it("round-trips a multi-line paste through normalization", () => {
    // The shape execCommand('insertText') leaves behind in Chrome: first line
    // inline, the rest wrapped in blocks.
    const root = document.createElement("div");
    root.appendChild(document.createTextNode("line 1"));
    for (const line of ["line 2", "line 3"]) {
      const block = document.createElement("div");
      block.appendChild(document.createTextNode(line));
      root.appendChild(block);
    }

    normalizeComposerDom(root);

    expect(serializeFromDom(root)).toBe("line 1\nline 2\nline 3");
  });

  it("keeps a newline inserted at the end of the text", () => {
    // Cmd+Enter at the end: the inserted <br> would sit in final position and
    // read back as scaffolding, so the newline needs its own trailing break.
    const root = mountRoot();
    const text = document.createTextNode("abc");
    root.appendChild(text);
    placeCaret(text, 3);

    insertPlainTextAtCursor(root, "\n");

    expect(serializeFromDom(root)).toBe("abc\n");
  });

  it("adds one newline per insertion at the end, not two", () => {
    const root = mountRoot();
    const text = document.createTextNode("abc");
    root.appendChild(text);
    placeCaret(text, 3);

    insertPlainTextAtCursor(root, "\n");
    expect(serializeFromDom(root)).toBe("abc\n");

    // Caret is already before a trailing scaffold <br>; a second insert must
    // reuse it rather than stacking another.
    insertPlainTextAtCursor(root, "\n");
    expect(serializeFromDom(root)).toBe("abc\n\n");
  });

  it("does not pad a newline inserted mid-text", () => {
    const root = mountRoot();
    const text = document.createTextNode("abcdef");
    root.appendChild(text);
    placeCaret(text, 3);

    insertPlainTextAtCursor(root, "\n");

    expect(serializeFromDom(root)).toBe("abc\ndef");
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
