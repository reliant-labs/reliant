/**
 * Contract tests over the real shortcut definitions.
 *
 * These guard the invariants that are easy to break by editing
 * config/shortcuts.yaml: chords that must not collide, editing surfaces that
 * must keep their native behavior, and browser-reserved chords that must have a
 * web alternative.
 */

import { describe, expect, it } from "vitest";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { parseBinding, sequencePrefix, isSequence } from "../chord";
import { ShortcutRegistry, type ResolvedShortcut } from "../registry";
import { getReservation } from "../reserved";
import { CONTEXTS, type ShortcutContext } from "../contexts";

type Platform = { isMac: boolean; isDesktop: boolean };

/** Build the registry exactly as the store does, for a given platform. */
function buildRegistry({ isMac, isDesktop }: Platform): ShortcutRegistry {
  const resolved: ResolvedShortcut[] = Object.values(defaultShortcuts).map(
    (shortcut) => ({
      id: shortcut.id,
      handler: shortcut.handler,
      binding: parseBinding(
        isDesktop ? shortcut.defaultBinding : shortcut.defaultWebBinding,
        isMac,
      ),
      context: shortcut.context,
      allowInInput: shortcut.allowInInput,
      passthrough: shortcut.passthrough,
    }),
  );
  return new ShortcutRegistry(resolved);
}

const MAC_DESKTOP: Platform = { isMac: true, isDesktop: true };
const MAC_WEB: Platform = { isMac: true, isDesktop: false };
const PC_WEB: Platform = { isMac: false, isDesktop: false };

describe("shortcut definitions", () => {
  it("has no two shortcuts claiming the same chord in the same context", () => {
    for (const platform of [MAC_DESKTOP, MAC_WEB, PC_WEB]) {
      const conflicts = buildRegistry(platform).findConflicts();
      expect(
        conflicts.map((c) => `${c.binding}: ${c.shortcuts.map((s) => s.id)}`),
      ).toEqual([]);
    }
  });

  it("gives every hard-reserved chord a web alternative", () => {
    // A hard-reserved chord is never delivered to the page, so a web binding
    // that used one would be dead on arrival.
    for (const shortcut of Object.values(defaultShortcuts)) {
      for (const platform of [MAC_WEB, PC_WEB]) {
        const binding = parseBinding(
          shortcut.defaultWebBinding,
          platform.isMac,
        );
        const reservation = getReservation(binding, platform.isMac);
        expect(
          reservation?.level === "hard" ? `${shortcut.id}: ${binding}` : null,
        ).toBeNull();
      }
    }
  });

  it("never puts a browser-reserved chord under a held sequence key on the web", () => {
    // Cmd+K then W is commonly typed with Cmd still held, and the browser sees
    // that as Cmd+W before the page does: it closed Reliant's own tab. The
    // second key of a web sequence must survive being typed either way.
    for (const shortcut of Object.values(defaultShortcuts)) {
      for (const platform of [MAC_WEB, PC_WEB]) {
        const binding = parseBinding(
          shortcut.defaultWebBinding,
          platform.isMac,
        );
        const reservation = getReservation(binding, platform.isMac);
        expect(
          reservation?.level === "held" ? `${shortcut.id}: ${binding}` : null,
        ).toBeNull();
      }
    }
  });

  describe("in a browser tab", () => {
    // The actions the app leans on must each have a binding that reaches the
    // page in a browser — and actually resolves to that action when typed.
    const ESSENTIAL = [
      "commandPalette",
      "newChat",
      "closeTab",
      "quickFileOpen",
      "toggleSidebar",
      "toggleFileBrowser",
      "toggleTerminal",
      "newTerminal",
      "reopenLastClosedFile",
      "toggleSettings",
      "focusChat",
      // Cmd+Shift+[ / ] on desktop: Chrome's tab switching, never delivered.
      "nextFileTab",
      "prevFileTab",
    ];

    /** Type a canonical binding chord by chord, the way the dispatcher sees it. */
    function typed(registry: ShortcutRegistry, binding: string) {
      let pending = "";
      for (const chord of binding.split(" ")) {
        const result = registry.resolve(chord, ["global"], pending);
        if (result.kind === "sequence-prefix") {
          pending = chord;
          continue;
        }
        return result.shortcut?.id;
      }
      return undefined;
    }

    it.each(ESSENTIAL)("%s fires from its web binding", (id) => {
      for (const platform of [MAC_WEB, PC_WEB]) {
        const shortcut = defaultShortcuts[id];
        const binding = parseBinding(
          shortcut.defaultWebBinding,
          platform.isMac,
        );

        expect(getReservation(binding, platform.isMac)?.level).not.toBe("hard");
        expect(typed(buildRegistry(platform), binding)).toBe(id);
      }
    });
  });

  describe("Cmd+Shift+Arrow", () => {
    // These navigate chats and panels, but the same chord extends a text
    // selection in any editor. Context precedence has to keep both working.
    const cases = [
      { chord: "Cmd+Shift+Down", nav: "nextChat" },
      { chord: "Cmd+Shift+Up", nav: "prevChat" },
      { chord: "Cmd+Shift+Right", nav: "nextRightSidebarTab" },
      { chord: "Cmd+Shift+Left", nav: "prevRightSidebarTab" },
    ];

    it.each(cases)("$chord navigates from the global context", ({ chord, nav }) => {
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve(parseBinding(chord, true), ["global"]);

      expect(result.kind).toBe("match");
      expect(result.shortcut?.id).toBe(nav);
    });

    it.each(cases)("$chord leaves selection alone in Monaco", ({ chord }) => {
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve(parseBinding(chord, true), [
        "monaco",
        "global",
      ]);

      // Must resolve to the passthrough entry, NOT the navigation one, so the
      // editor's own selection handling still runs.
      expect(result.shortcut?.handler).toBe("onNativeSelection");
      expect(result.shortcut?.passthrough).toBe(true);
    });

    it.each(cases)("$chord leaves selection alone in the composer", ({ chord }) => {
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve(parseBinding(chord, true), [
        "chat-input",
        "global",
      ]);

      expect(result.shortcut?.handler).toBe("onNativeSelection");
      expect(result.shortcut?.passthrough).toBe(true);
    });
  });

  describe("Escape", () => {
    it("stops streaming from the global context", () => {
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve("Escape", ["global"]);

      expect(result.shortcut?.id).toBe("stopStreaming");
    });

    it("still reaches the composer while typing", () => {
      // stopStreaming opts in via allow_in_input; without that, Escape would be
      // suppressed as ordinary typing exactly when it is needed most.
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve("Escape", ["chat-input", "global"]);

      expect(result.shortcut?.id).toBe("stopStreaming");
    });

    it("closes the modal instead of pausing the chat behind it", () => {
      // The regression this guards: with an open modal, Escape resolved to
      // stopStreaming and paused the underlying chat. Search, file search and
      // every other overlay looked like they were "eating" Escape when they
      // were in fact closing AND pausing the chat behind them.
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve("Escape", ["modal", "global"]);

      expect(result.shortcut?.id).toBe("dismissModal");
    });

    it("closes the modal even while its search box has focus", () => {
      // Every one of these overlays autofocuses a text input, so the modal
      // context is almost never active on its own.
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve("Escape", [
        "modal",
        "chat-input",
        "global",
      ]);

      expect(result.shortcut?.id).toBe("dismissModal");
    });

    it("closes the slash menu instead of stopping the response", () => {
      // The regression this guards: without a slash-menu entry the global
      // Escape wins and the menu cannot be dismissed with the keyboard.
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve("Escape", [
        "slash-menu",
        "chat-input",
        "global",
      ]);

      expect(result.shortcut?.id).toBe("dismissSlashMenu");
    });
  });

  describe("Cmd+F", () => {
    it.each([MAC_DESKTOP, MAC_WEB, PC_WEB])(
      "searches the conversation on screen, wherever focus is",
      (platform) => {
        const registry = buildRegistry(platform);
        const chord = parseBinding("Cmd+F", platform.isMac);

        for (const contexts of [
          ["chat-view", "global"],
          ["chat-input", "chat-view", "global"],
          ["right-sidebar", "chat-view", "global"],
        ]) {
          expect(registry.resolve(chord, contexts).shortcut?.id).toBe("chatSearch");
        }
      },
    );

    it.each([MAC_WEB, PC_WEB])(
      "is left to the browser's find with no conversation on screen",
      (platform) => {
        // The dashboard, Settings, the Inbox, Runs: "find" there means the
        // page, not chat history (Cmd+E is the chat switcher everywhere), and
        // a browser tab has its own find bar for that.
        const registry = buildRegistry(platform);
        const chord = parseBinding("Cmd+F", platform.isMac);

        expect(registry.resolve(chord, ["global"]).kind).toBe("none");
        expect(registry.resolve(chord, ["chat-input", "global"]).kind).toBe("none");
      },
    );

    it("finds in the page with no conversation on screen in the desktop app", () => {
      // The desktop app has no browser find bar to leave the key to.
      const registry = buildRegistry(MAC_DESKTOP);
      const chord = parseBinding("Cmd+F", true);

      for (const contexts of [["global"], ["chat-input", "global"], ["file-tree", "global"]]) {
        expect(registry.resolve(chord, contexts).shortcut?.id).toBe("findInPage");
      }
    });

    it("leaves find-in-page out of a browser tab entirely", () => {
      const shortcut = defaultShortcuts.findInPage;
      expect(shortcut.desktopOnly).toBe(true);
      expect(shortcut.defaultWebBinding).toBe("");
      expect(shortcut.context).toBe("global");
    });

    it.each([MAC_DESKTOP, MAC_WEB, PC_WEB])(
      "defers to the editor's own find inside Monaco",
      (platform) => {
        const registry = buildRegistry(platform);
        const result = registry.resolve(parseBinding("Cmd+F", platform.isMac), [
          "monaco",
          "chat-view",
          "global",
        ]);

        expect(result.shortcut?.id).toBe("editorFind");
        expect(result.shortcut?.passthrough).toBe(true);
        expect(
          registry.resolve(parseBinding("Cmd+F", platform.isMac), ["monaco", "global"])
            .shortcut?.id,
        ).toBe("editorFind");
      },
    );
  });

  describe("Cmd+L", () => {
    it.each([MAC_DESKTOP, MAC_WEB, PC_WEB])(
      "adds the selection to the chat from the editor, and focuses the chat elsewhere",
      (platform) => {
        const registry = buildRegistry(platform);
        const shortcut = defaultShortcuts.focusChat;
        const binding = parseBinding(
          platform.isDesktop ? shortcut.defaultBinding : shortcut.defaultWebBinding,
          platform.isMac,
        );
        const [first, second] = binding.split(" ");
        const resolve = (contexts: string[]) =>
          second
            ? registry.resolve(second, contexts, first).shortcut?.id
            : registry.resolve(first, contexts).shortcut?.id;

        expect(resolve(["monaco", "global"])).toBe("addSelectionToChat");
        expect(resolve(["global"])).toBe("focusChat");
      },
    );
  });

  describe("clipboard", () => {
    it.each(["Cmd+C", "Cmd+X", "Cmd+V"])(
      "%s is a file operation only in the file tree",
      (chord) => {
        const registry = buildRegistry(MAC_DESKTOP);
        const binding = parseBinding(chord, true);

        // Outside the tree the chord is unclaimed, so the browser's native
        // copy/cut/paste runs untouched.
        expect(registry.resolve(binding, ["global"]).kind).toBe("none");
        expect(
          registry.resolve(binding, ["file-tree", "global"]).kind,
        ).toBe("match");
      },
    );
  });

  describe("sequences", () => {
    it("routes every Cmd+K sequence through one prefix", () => {
      const prefixes = new Set<string>();
      for (const shortcut of Object.values(defaultShortcuts)) {
        const binding = parseBinding(shortcut.defaultWebBinding, true);
        if (isSequence(binding)) prefixes.add(sequencePrefix(binding));
      }

      // A second prefix would mean a second chord the browser must yield.
      expect([...prefixes]).toEqual(["meta+K"]);
    });

    it("recognizes the prefix as a sequence opener on the web", () => {
      const registry = buildRegistry(MAC_WEB);
      expect(registry.resolve("meta+K", ["global"]).kind).toBe(
        "sequence-prefix",
      );
    });
  });

  describe("chords owned by direct listeners", () => {
    // These are handled by component-level listeners rather than the registry,
    // for the reasons documented in contexts.ts. If a shortcut ever claims one,
    // the two would fight and one would silently lose — so assert they stay
    // disjoint rather than discovering it as a bug report.
    const RESERVED_FOR_COMPONENTS = [
      { chord: "Cmd+S", owner: "FileViewerTab (save file in Monaco)" },
      { chord: "Cmd+Z", owner: "ModernApp (file-operation undo)" },
      { chord: "Cmd+Shift+Z", owner: "ModernApp (file-operation redo)" },
      { chord: "Cmd+Y", owner: "ModernApp (file-operation redo)" },
      { chord: "PageUp", owner: "FileTree (scroll by viewport)" },
      { chord: "PageDown", owner: "FileTree (scroll by viewport)" },
      // The find bar's next/previous match. It is a page of its own (in the
      // desktop app's find-bar view), but a shortcut on these would put a
      // second meaning on the chord Chrome users know as "find next".
      { chord: "Cmd+G", owner: "FindBar (next match)" },
      { chord: "Cmd+Shift+G", owner: "FindBar (previous match)" },
    ];

    it.each(RESERVED_FOR_COMPONENTS)(
      "$chord stays unclaimed — owned by $owner",
      ({ chord }) => {
        const registry = buildRegistry(MAC_DESKTOP);
        const binding = parseBinding(chord, true);

        // Unclaimed in every context, including the editing surfaces where
        // these listeners actually run.
        for (const contexts of [
          ["global"],
          ["monaco", "global"],
          ["file-tree", "global"],
          ["chat-input", "global"],
        ]) {
          expect(registry.resolve(binding, contexts).kind).toBe("none");
        }
      },
    );
  });

  describe("Cmd+Enter", () => {
    it("approves tool requests outside the composer", () => {
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve(parseBinding("Cmd+Enter", true), [
        "global",
      ]);

      expect(result.shortcut?.id).toBe("approveToolRequests");
    });

    it("inserts a newline while the composer has focus", () => {
      // The original bug: ChatTextArea's window capture listener stopped
      // propagation, so approveToolRequests was dead whenever the composer had
      // focus. Both behaviors now coexist by context.
      const registry = buildRegistry(MAC_DESKTOP);
      const result = registry.resolve(parseBinding("Cmd+Enter", true), [
        "chat-input",
        "global",
      ]);

      expect(result.shortcut?.id).toBe("composerNewline");
    });
  });

  it("declares a context that the detector can actually produce", () => {
    // A shortcut in a context nothing marks would never resolve — it would look
    // configured but silently do nothing. This caught a workflow-canvas binding
    // that could never fire because no component set that data-context.
    const detectable = new Set(CONTEXTS);
    for (const shortcut of Object.values(defaultShortcuts)) {
      expect({
        id: shortcut.id,
        context: shortcut.context,
        known: detectable.has(shortcut.context as ShortcutContext),
      }).toMatchObject({ known: true });
    }
  });

  it("keeps the two sidebar toggles distinct", () => {
    // These previously both flipped the same boolean, so two different
    // shortcuts silently did the same thing.
    const registry = buildRegistry(MAC_DESKTOP);
    const right = registry.resolve(parseBinding("Cmd+B", true), ["global"]);
    const left = registry.resolve(parseBinding("Cmd+Alt+B", true), ["global"]);
    const focus = registry.resolve(parseBinding("Cmd+Shift+B", true), [
      "global",
    ]);

    expect(right.shortcut?.handler).toBe("onToggleFileBrowser");
    expect(left.shortcut?.handler).toBe("onToggleSidebar");
    expect(focus.shortcut?.handler).toBe("onFocusRightSidebar");
  });
});

describe("workflow builder shortcuts", () => {
  it("opens the step palette inside the builder, even from a config field, and nowhere else", () => {
    for (const platform of [MAC_DESKTOP, MAC_WEB, PC_WEB]) {
      const registry = buildRegistry(platform);
      const chord = parseBinding("Cmd+I", platform.isMac);
      expect(registry.resolve(chord, ["workflow-canvas", "global"]).shortcut?.handler).toBe("onOpenStepPalette");
      expect(registry.resolve(chord, ["global"]).kind).toBe("none");
    }
  });

  it("connects the selected steps inside the builder, not while typing in a field, and nowhere else", () => {
    for (const platform of [MAC_DESKTOP, MAC_WEB, PC_WEB]) {
      const registry = buildRegistry(platform);
      const chord = parseBinding("Cmd+Shift+L", platform.isMac);
      expect(registry.resolve(chord, ["workflow-canvas", "global"]).shortcut?.handler).toBe("onConnectSelectedSteps");
      expect(registry.resolve(chord, ["global"]).kind).toBe("none");
      expect(getReservation(chord, platform.isMac)).toBeUndefined();
    }
  });
});
