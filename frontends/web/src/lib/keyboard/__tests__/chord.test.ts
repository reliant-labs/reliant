import { describe, expect, it } from "vitest";
import {
  chordToString,
  eventToChordString,
  heldSecondChord,
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

  describe("when the keyboard substitutes the key", () => {
    // event.key reports what the keyboard PRODUCED. Bindings name the key that
    // was pressed. Where the two differ, the physical key (event.code) decides.

    it("matches Cmd+Option chords on macOS, where Option composes a glyph", () => {
      // Option+B types "∫", so Cmd+Option+B used to arrive as meta+alt+∫ and
      // the left-sidebar toggle never fired on a Mac.
      const fromEvent = eventToChordString(
        event({ key: "∫", code: "KeyB", metaKey: true, altKey: true }),
      );
      expect(fromEvent).toBe(parseBinding("Cmd+Alt+B", true));
    });

    it("matches Cmd+Option chords whose Option form is a dead key", () => {
      // Option+N starts a tilde: the browser reports "Dead".
      const fromEvent = eventToChordString(
        event({ key: "Dead", code: "KeyN", metaKey: true, altKey: true }),
      );
      expect(fromEvent).toBe(parseBinding("Cmd+Alt+N", true));
    });

    it("matches Ctrl+letter on a non-Latin layout", () => {
      // Russian layout: the K key types "л". Every letter shortcut was dead.
      const fromEvent = eventToChordString(
        event({ key: "л", code: "KeyK", ctrlKey: true }),
      );
      expect(fromEvent).toBe(parseBinding("Cmd+K", false));
    });

    it("matches Cmd+letter on a non-Latin layout on macOS", () => {
      const fromEvent = eventToChordString(
        event({ key: "о", code: "KeyJ", metaKey: true }),
      );
      expect(fromEvent).toBe(parseBinding("Cmd+J", true));
    });

    it("maps punctuation by position", () => {
      const fromEvent = eventToChordString(
        event({ key: "х", code: "BracketLeft", metaKey: true, shiftKey: true }),
      );
      expect(fromEvent).toBe(parseBinding("Cmd+Shift+[", true));
    });

    it("keeps the layout's own Latin letter when it is one (AZERTY, Dvorak)", () => {
      // AZERTY: the key in QWERTY's Q position types "a". The user pressed the
      // key labelled A, and that is the binding they mean.
      const fromEvent = eventToChordString(
        event({ key: "a", code: "KeyQ", ctrlKey: true }),
      );
      expect(fromEvent).toBe("ctrl+A");
    });

    it("leaves AltGr text alone on Windows", () => {
      // AltGr arrives as Ctrl+Alt. AltGr+N types "ñ" — that is typing, and
      // reading it as Ctrl+Alt+N would steal the character.
      const fromEvent = eventToChordString(
        event({ key: "ñ", code: "KeyN", ctrlKey: true, altKey: true }),
      );
      expect(fromEvent).not.toBe(parseBinding("Cmd+Alt+N", false));
    });

    it("leaves plain typing alone", () => {
      // No command modifier: a non-Latin letter is just text.
      expect(eventToChordString(event({ key: "л", code: "KeyK" }))).toBe("л");
    });
  });
});

describe("heldSecondChord", () => {
  it("is the second chord with the prefix modifier still held", () => {
    // What the browser sees when Cmd+K then W is typed without releasing Cmd.
    expect(heldSecondChord(parseBinding("Cmd+K W", true))).toBe("meta+W");
    expect(heldSecondChord(parseBinding("Cmd+K Shift+P", false))).toBe(
      "ctrl+shift+P",
    );
  });

  it("is undefined for a single chord", () => {
    expect(heldSecondChord(parseBinding("Cmd+W", true))).toBeUndefined();
  });
});

describe("chordToString", () => {
  it("is stable across modifier declaration order", () => {
    expect(chordToString({ key: "k", meta: true, shift: true })).toBe(
      chordToString({ key: "K", shift: true, meta: true }),
    );
  });
});
