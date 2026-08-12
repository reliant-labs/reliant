import { beforeEach, describe, expect, it, vi } from "vitest";
import { createDispatcher, SEQUENCE_TIMEOUT_MS } from "../dispatcher";
import { ShortcutRegistry, type ResolvedShortcut } from "../registry";
import type { ShortcutContext } from "../contexts";

const shortcut = (over: Partial<ResolvedShortcut>): ResolvedShortcut => ({
  id: "test",
  handler: "onTest",
  binding: "meta+P",
  context: "global",
  ...over,
});

function setup(
  shortcuts: ResolvedShortcut[],
  handlers: Record<string, () => void>,
  contexts: ShortcutContext[] = ["global"],
) {
  const registry = new ShortcutRegistry(shortcuts);
  const dispatcher = createDispatcher({
    registry,
    getHandler: (name) => handlers[name],
    detectContexts: () => contexts,
  });
  return dispatcher;
}

function press(
  dispatcher: ReturnType<typeof setup>,
  init: Partial<KeyboardEventInit> & { key: string },
) {
  const event = new KeyboardEvent("keydown", { cancelable: true, ...init });
  const preventDefault = vi.spyOn(event, "preventDefault");
  const stopImmediate = vi.spyOn(event, "stopImmediatePropagation");
  dispatcher.handleKeyDown(event);
  return { event, preventDefault, stopImmediate };
}

describe("dispatcher", () => {
  it("fires the handler for a registered chord", () => {
    const onTest = vi.fn();
    const dispatcher = setup([shortcut({})], { onTest });

    press(dispatcher, { key: "p", metaKey: true });

    expect(onTest).toHaveBeenCalledOnce();
  });

  it("claims the event so nothing downstream also acts on it", () => {
    const dispatcher = setup([shortcut({})], { onTest: vi.fn() });

    const { preventDefault, stopImmediate } = press(dispatcher, {
      key: "p",
      metaKey: true,
    });

    expect(preventDefault).toHaveBeenCalled();
    expect(stopImmediate).toHaveBeenCalled();
  });

  it("leaves unregistered chords alone for the browser and the page", () => {
    const dispatcher = setup([shortcut({})], { onTest: vi.fn() });

    const { preventDefault } = press(dispatcher, { key: "q", metaKey: true });

    expect(preventDefault).not.toHaveBeenCalled();
  });

  it("does not preventDefault for a passthrough shortcut", () => {
    const onTest = vi.fn();
    const dispatcher = setup([shortcut({ passthrough: true })], { onTest });

    const { preventDefault } = press(dispatcher, { key: "p", metaKey: true });

    expect(onTest).toHaveBeenCalledOnce();
    expect(preventDefault).not.toHaveBeenCalled();
  });

  it("ignores auto-repeat so a held key fires once", () => {
    const onTest = vi.fn();
    const dispatcher = setup([shortcut({})], { onTest });

    press(dispatcher, { key: "p", metaKey: true, repeat: true });

    expect(onTest).not.toHaveBeenCalled();
  });

  it("does not throw when a handler is missing", () => {
    const dispatcher = setup([shortcut({ handler: "onAbsent" })], {});

    expect(() => press(dispatcher, { key: "p", metaKey: true })).not.toThrow();
  });

  it("reports handler errors without breaking dispatch", () => {
    const onError = vi.fn();
    const registry = new ShortcutRegistry([shortcut({})]);
    const dispatcher = createDispatcher({
      registry,
      getHandler: () => () => {
        throw new Error("boom");
      },
      detectContexts: () => ["global"],
      onError,
    });

    expect(() =>
      dispatcher.handleKeyDown(new KeyboardEvent("keydown", { key: "p", metaKey: true })),
    ).not.toThrow();
    expect(onError).toHaveBeenCalled();
  });

  describe("context precedence", () => {
    it("routes the same chord to different handlers by context", () => {
      const onGlobal = vi.fn();
      const onEditor = vi.fn();
      const shortcuts = [
        shortcut({ id: "g", binding: "meta+F", handler: "onGlobal" }),
        shortcut({
          id: "e",
          binding: "meta+F",
          handler: "onEditor",
          context: "monaco",
        }),
      ];

      const inEditor = setup(shortcuts, { onGlobal, onEditor }, [
        "monaco",
        "global",
      ]);
      press(inEditor, { key: "f", metaKey: true });
      expect(onEditor).toHaveBeenCalledOnce();
      expect(onGlobal).not.toHaveBeenCalled();

      const outside = setup(shortcuts, { onGlobal, onEditor }, ["global"]);
      press(outside, { key: "f", metaKey: true });
      expect(onGlobal).toHaveBeenCalledOnce();
    });

    it("does not steal a bare key while typing", () => {
      const onTest = vi.fn();
      const dispatcher = setup([shortcut({ binding: "G" })], { onTest }, [
        "chat-input",
        "global",
      ]);

      const { preventDefault } = press(dispatcher, { key: "g" });

      expect(onTest).not.toHaveBeenCalled();
      expect(preventDefault).not.toHaveBeenCalled();
    });
  });

  describe("sequences", () => {
    beforeEach(() => {
      vi.useFakeTimers();
    });

    const sequenceShortcuts = [
      shortcut({ id: "chats", binding: "meta+K C", handler: "onChats" }),
      shortcut({ id: "projects", binding: "meta+K P", handler: "onProjects" }),
    ];

    it("completes a two-chord sequence", () => {
      const onChats = vi.fn();
      const dispatcher = setup(sequenceShortcuts, { onChats });

      press(dispatcher, { key: "k", metaKey: true });
      expect(dispatcher.getPending()).toBe("meta+K");
      expect(onChats).not.toHaveBeenCalled();

      press(dispatcher, { key: "c" });
      expect(onChats).toHaveBeenCalledOnce();
      expect(dispatcher.getPending()).toBe("");
    });

    it("swallows the prefix chord so it never reaches the browser", () => {
      const dispatcher = setup(sequenceShortcuts, { onChats: vi.fn() });

      const { preventDefault } = press(dispatcher, { key: "k", metaKey: true });

      expect(preventDefault).toHaveBeenCalled();
    });

    it("completes a bare second chord even while typing in the composer", () => {
      const onChats = vi.fn();
      const dispatcher = setup(sequenceShortcuts, { onChats }, [
        "chat-input",
        "global",
      ]);

      press(dispatcher, { key: "k", metaKey: true });
      press(dispatcher, { key: "c" });

      expect(onChats).toHaveBeenCalledOnce();
    });

    it("abandons the sequence on an unknown completion", () => {
      const onChats = vi.fn();
      const dispatcher = setup(sequenceShortcuts, { onChats });

      press(dispatcher, { key: "k", metaKey: true });
      const { preventDefault } = press(dispatcher, { key: "z" });

      expect(onChats).not.toHaveBeenCalled();
      expect(dispatcher.getPending()).toBe("");
      // The stray key still belongs to the page.
      expect(preventDefault).not.toHaveBeenCalled();
    });

    it("expires the prefix after the timeout", () => {
      const onChats = vi.fn();
      const dispatcher = setup(sequenceShortcuts, { onChats });

      press(dispatcher, { key: "k", metaKey: true });
      vi.advanceTimersByTime(SEQUENCE_TIMEOUT_MS + 1);
      expect(dispatcher.getPending()).toBe("");

      press(dispatcher, { key: "c" });
      expect(onChats).not.toHaveBeenCalled();
    });

    it("keeps the prefix armed while a modifier is held down", () => {
      const onChats = vi.fn();
      const dispatcher = setup(sequenceShortcuts, { onChats });

      press(dispatcher, { key: "k", metaKey: true });
      // Releasing Cmd fires a keydown for Meta itself on some platforms.
      press(dispatcher, { key: "Meta", metaKey: true });
      expect(dispatcher.getPending()).toBe("meta+K");

      press(dispatcher, { key: "c" });
      expect(onChats).toHaveBeenCalledOnce();
    });

    it("notifies listeners when the pending prefix changes", () => {
      const onPendingChange = vi.fn();
      const registry = new ShortcutRegistry(sequenceShortcuts);
      const dispatcher = createDispatcher({
        registry,
        getHandler: () => vi.fn(),
        detectContexts: () => ["global"],
        onPendingChange,
      });

      dispatcher.handleKeyDown(
        new KeyboardEvent("keydown", { key: "k", metaKey: true }),
      );

      expect(onPendingChange).toHaveBeenCalledWith("meta+K");
    });
  });
});
