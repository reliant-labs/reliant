import { describe, expect, it } from "vitest";
import {
  chordToString,
  eventToChordString,
  isSequence,
  normalizeKey,
  parseBinding,
  sequencePrefix,
} from "../chord";

describe("normalizeKey", () => {
  it("maps arrow keys to their short names", () => {
    expect(normalizeKey("ArrowDown")).toBe("Down");
    expect(normalizeKey("ArrowUp")).toBe("Up");
    expect(normalizeKey("ArrowLeft")).toBe("Left");
    expect(normalizeKey("ArrowRight")).toBe("Right");
  });

  it("upper-cases letters so case never affects matching", () => {
    expect(normalizeKey("a")).toBe("A");
    expect(normalizeKey("A")).toBe("A");
  });

  it("folds shifted punctuation back to the physical key", () => {
    // Shift+[ reports "{" — a binding written as Cmd+Shift+[ must still match.
    expect(normalizeKey("{", true)).toBe("[");
    expect(normalizeKey("}", true)).toBe("]");
    expect(normalizeKey("?", true)).toBe("/");
  });

  it("leaves punctuation alone when shift is not held", () => {
    expect(normalizeKey("[", false)).toBe("[");
  });
});

describe("parseBinding", () => {
  it("maps Cmd to Meta on macOS and Control elsewhere", () => {
    expect(parseBinding("Cmd+P", true)).toBe("meta+P");
    expect(parseBinding("Cmd+P", false)).toBe("ctrl+P");
  });

  it("orders modifiers canonically regardless of how they were written", () => {
    expect(parseBinding("Cmd+Shift+P", true)).toBe("meta+shift+P");
    expect(parseBinding("Shift+Cmd+P", true)).toBe("meta+shift+P");
  });

  it("substitutes Alt for the second modifier off macOS so Cmd+Ctrl stays typeable", () => {
    expect(parseBinding("Cmd+Ctrl+Up", true)).toBe("ctrl+meta+Up");
    expect(parseBinding("Cmd+Ctrl+Up", false)).toBe("ctrl+alt+Up");
  });

  it("parses bare keys with no modifiers", () => {
    expect(parseBinding("Escape", true)).toBe("Escape");
  });

  it("parses multi-chord sequences", () => {
    expect(parseBinding("Cmd+K G", true)).toBe("meta+K G");
    expect(isSequence(parseBinding("Cmd+K G", true))).toBe(true);
    expect(sequencePrefix(parseBinding("Cmd+K G", true))).toBe("meta+K");
  });

  it("treats a single chord as not a sequence", () => {
    expect(isSequence(parseBinding("Cmd+P", true))).toBe(false);
  });

  it("accepts Mod as a synonym for Cmd", () => {
    expect(parseBinding("Mod+P", true)).toBe(parseBinding("Cmd+P", true));
  });
});

describe("eventToChordString", () => {
  const event = (init: Partial<KeyboardEventInit> & { key: string }) =>
    new KeyboardEvent("keydown", init);

  it("serializes a modified chord", () => {
    expect(eventToChordString(event({ key: "p", metaKey: true }))).toBe(
      "meta+P",
    );
  });

  it("produces the same string as the equivalent parsed binding", () => {
    const fromEvent = eventToChordString(
      event({ key: "P", metaKey: true, shiftKey: true }),
    );
    expect(fromEvent).toBe(parseBinding("Cmd+Shift+P", true));
  });

  it("matches parsed bindings for shifted punctuation", () => {
    // The browser reports "{" for Shift+[; both sides must normalize alike.
    const fromEvent = eventToChordString(
      event({ key: "{", metaKey: true, shiftKey: true }),
    );
    expect(fromEvent).toBe(parseBinding("Cmd+Shift+[", true));
  });

  it("returns null for modifier-only presses so sequences are not reset", () => {
    expect(eventToChordString(event({ key: "Shift", shiftKey: true }))).toBeNull();
    expect(eventToChordString(event({ key: "Meta", metaKey: true }))).toBeNull();
  });

  it("normalizes arrow keys to match config spelling", () => {
    const fromEvent = eventToChordString(
      event({ key: "ArrowDown", metaKey: true, ctrlKey: true }),
    );
    expect(fromEvent).toBe(parseBinding("Cmd+Ctrl+Down", true));
  });
});

describe("chordToString", () => {
  it("is stable across modifier declaration order", () => {
    expect(chordToString({ key: "k", meta: true, shift: true })).toBe(
      chordToString({ key: "K", shift: true, meta: true }),
    );
  });
});
