import { describe, expect, it } from "vitest";
import { ShortcutRegistry, isBareKey, type ResolvedShortcut } from "../registry";

const shortcut = (over: Partial<ResolvedShortcut>): ResolvedShortcut => ({
  id: "test",
  handler: "onTest",
  binding: "meta+P",
  context: "global",
  ...over,
});

describe("ShortcutRegistry.resolve", () => {
  it("matches a chord in an active context", () => {
    const registry = new ShortcutRegistry([shortcut({ id: "quickOpen" })]);
    const result = registry.resolve("meta+P", ["global"]);

    expect(result.kind).toBe("match");
    expect(result.shortcut?.id).toBe("quickOpen");
  });

  it("does not match when the shortcut's context is inactive", () => {
    const registry = new ShortcutRegistry([
      shortcut({ id: "editorOnly", context: "monaco" }),
    ]);

    expect(registry.resolve("meta+P", ["global"]).kind).toBe("none");
  });

  it("lets an inner context shadow the same binding in an outer one", () => {
    // This is the core of the design: Cmd+F means "find in file" inside Monaco
    // and "search chats" everywhere else, with neither side special-casing.
    const registry = new ShortcutRegistry([
      shortcut({ id: "chatSearch", binding: "meta+F", context: "global" }),
      shortcut({ id: "editorFind", binding: "meta+F", context: "monaco" }),
    ]);

    expect(registry.resolve("meta+F", ["monaco", "global"]).shortcut?.id).toBe(
      "editorFind",
    );
    expect(registry.resolve("meta+F", ["global"]).shortcut?.id).toBe(
      "chatSearch",
    );
  });

  it("prefers the innermost context regardless of registration order", () => {
    const registry = new ShortcutRegistry([
      shortcut({ id: "outer", binding: "Escape", context: "global" }),
      shortcut({ id: "inner", binding: "Escape", context: "modal" }),
    ]);

    expect(
      registry.resolve("Escape", ["global", "modal"]).shortcut?.id,
    ).toBe("inner");
  });

  describe("bare keys while typing", () => {
    it("suppresses an unmodified key in a text-entry context", () => {
      const registry = new ShortcutRegistry([
        shortcut({ id: "goto", binding: "G", context: "global" }),
      ]);

      expect(registry.resolve("G", ["chat-input", "global"]).kind).toBe("none");
      expect(registry.resolve("G", ["global"]).kind).toBe("match");
    });

    it("still fires a bare key when the shortcut opts in", () => {
      const registry = new ShortcutRegistry([
        shortcut({
          id: "stop",
          binding: "Escape",
          context: "global",
          allowInInput: true,
        }),
      ]);

      expect(registry.resolve("Escape", ["chat-input", "global"]).kind).toBe(
        "match",
      );
    });

    it("still fires modified chords while typing", () => {
      const registry = new ShortcutRegistry([
        shortcut({ id: "palette", binding: "meta+shift+P" }),
      ]);

      expect(
        registry.resolve("meta+shift+P", ["chat-input", "global"]).kind,
      ).toBe("match");
    });
  });

  describe("sequences", () => {
    const registry = new ShortcutRegistry([
      shortcut({ id: "gotoChats", binding: "meta+K C" }),
      shortcut({ id: "gotoProjects", binding: "meta+K P" }),
    ]);

    it("reports the opening chord as a sequence prefix", () => {
      expect(registry.resolve("meta+K", ["global"]).kind).toBe(
        "sequence-prefix",
      );
    });

    it("completes the sequence on the second chord", () => {
      const result = registry.resolve("C", ["global"], "meta+K");
      expect(result.kind).toBe("match");
      expect(result.shortcut?.id).toBe("gotoChats");
    });

    it("returns none for an unknown completion", () => {
      expect(registry.resolve("Z", ["global"], "meta+K").kind).toBe("none");
    });

    it("allows a bare completion chord even while typing", () => {
      // The prefix already signalled intent, so "C" here is not stray typing.
      const result = registry.resolve("C", ["chat-input", "global"], "meta+K");
      expect(result.kind).toBe("match");
    });

    it("completes when the user keeps Cmd held for the second key", () => {
      // Cmd+K then Cmd+C. Holding the modifier through the sequence is how
      // most people type it (and what VS Code accepts), but it arrives as
      // "meta+C" — which matched nothing, so every Cmd+K binding appeared
      // dead unless the user released Cmd first.
      const result = registry.resolve("meta+C", ["global"], "meta+K");
      expect(result.kind).toBe("match");
      expect(result.shortcut?.id).toBe("gotoChats");
    });

    it("does not invent a match from an unrelated held modifier", () => {
      // Only modifiers carried over FROM the prefix are dropped. Alt was never
      // part of Cmd+K, so the user meant something else and this stays unbound.
      expect(registry.resolve("alt+C", ["global"], "meta+K").kind).toBe("none");
    });

    it("keeps a held prefix modifier from resurrecting an unknown key", () => {
      expect(registry.resolve("meta+Z", ["global"], "meta+K").kind).toBe("none");
    });
  });

  it("finds shortcuts that shadow each other in the same context", () => {
    const registry = new ShortcutRegistry([
      shortcut({ id: "a", binding: "meta+J", context: "global" }),
      shortcut({ id: "b", binding: "meta+J", context: "global" }),
      shortcut({ id: "c", binding: "meta+J", context: "monaco" }),
    ]);

    const conflicts = registry.findConflicts();
    expect(conflicts).toHaveLength(1);
    expect(conflicts[0].shortcuts.map((s) => s.id).sort()).toEqual(["a", "b"]);
  });
});

describe("isBareKey", () => {
  it("treats unmodified and shift-only chords as typing", () => {
    expect(isBareKey("G")).toBe(true);
    expect(isBareKey("Escape")).toBe(true);
    expect(isBareKey("shift+G")).toBe(true);
  });

  it("treats command modifiers as not typing", () => {
    expect(isBareKey("meta+G")).toBe(false);
    expect(isBareKey("ctrl+G")).toBe(false);
    expect(isBareKey("ctrl+alt+Down")).toBe(false);
    expect(isBareKey("meta+shift+P")).toBe(false);
  });
});
