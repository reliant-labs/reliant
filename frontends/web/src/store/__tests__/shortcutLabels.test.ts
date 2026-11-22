/**
 * What the UI says a shortcut is, on each surface.
 *
 * A browser tab and the desktop app bind some actions differently — the
 * browser never releases Cmd+T, Cmd+W or Cmd+Shift+[ to the page. A tooltip
 * that hardcodes the desktop chord teaches a browser user a key that does
 * nothing, or worse: "Close tab (Cmd+W)" closed Reliant's own tab. So every
 * label comes from shortcutLabel(), and the source scan below rejects a
 * literal registry chord in a component.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";

import { describe, expect, it } from "vitest";
import { parseBinding } from "../../lib/keyboard/chord";
import { effectiveAliases, shortcutLabel, withShortcut } from "../shortcutsStore";
import { defaultShortcuts } from "../shortcutsData.generated";

const MAC_WEB = { isMac: true, isDesktop: false };
const MAC_DESKTOP = { isMac: true, isDesktop: true };
const PC_WEB = { isMac: false, isDesktop: false };

const label = (id: string, platform: typeof MAC_WEB, currentBinding: string | null = null) =>
  shortcutLabel({ ...defaultShortcuts[id], currentBinding }, platform);

describe("shortcutLabel", () => {
  it("shows the same binding in a browser tab and in the desktop app", () => {
    for (const platform of [MAC_WEB, MAC_DESKTOP]) {
      expect(label("newChat", platform)).toBe("⌘⌥N");
      expect(label("closeTab", platform)).toBe("⌘K then X");
      expect(label("nextFileTab", platform)).toBe("⌘K then ]");
      expect(label("quickFileOpen", platform)).toBe("⌘P");
    }
    expect(label("newChat", PC_WEB)).toBe("Ctrl+Alt+N");
  });

  it("follows a remap, unless the browser would never deliver it", () => {
    expect(label("newChat", MAC_WEB, "Cmd+Shift+Y")).toBe("⌘⇧Y");
    // A desktop remap onto Cmd+T reaches a browser tab too; it cannot fire
    // there, so the label keeps the chord that does.
    expect(label("newChat", MAC_WEB, "Cmd+T")).toBe("⌘⌥N");
  });

  it("leaves the text alone for an unbound shortcut", () => {
    expect(withShortcut("New chat", "")).toBe("New chat");
    expect(withShortcut("New chat", "⌘⌥N")).toBe("New chat (⌘⌥N)");
  });
});

describe("effectiveAliases", () => {
  const aliases = (id: string, platform: typeof MAC_WEB, currentBinding: string | null = null) =>
    effectiveAliases({ ...defaultShortcuts[id], currentBinding }, platform);

  it("keeps the old desktop chords in the desktop app", () => {
    expect(aliases("newChat", MAC_DESKTOP)).toEqual(["Cmd+T"]);
    expect(aliases("closeTab", MAC_DESKTOP)).toEqual(["Cmd+W"]);
    expect(aliases("reopenLastClosedFile", MAC_DESKTOP)).toEqual(["Cmd+Shift+T"]);
  });

  it("has none in a browser tab, where the browser keeps those chords", () => {
    expect(aliases("newChat", MAC_WEB)).toEqual([]);
    expect(aliases("closeTab", PC_WEB)).toEqual([]);
  });

  it("is replaced, with the binding, by a remap", () => {
    expect(aliases("newChat", MAC_DESKTOP, "Cmd+Shift+Y")).toEqual([]);
  });
});

describe("a remap stored before bindings were unified", () => {
  // Overrides are stored per user, as one string per shortcut id, never per
  // surface — so there is no stored web_binding to go stale. What a user
  // recorded is still theirs, whichever chord it was.
  it("keeps a remap onto a chord that used to be the browser's default", () => {
    // Recorded in a tab when Quick File Open was Cmd+K P there.
    expect(label("quickFileOpen", MAC_WEB, "Cmd+K P")).toBe("⌘K then P");
    expect(label("quickFileOpen", MAC_DESKTOP, "Cmd+K P")).toBe("⌘K then P");
  });

  it("keeps a remap onto an old desktop default in the desktop app", () => {
    // Recorded as Cmd+T: it is the user's binding, not an alias, so it stays.
    expect(label("newChat", MAC_DESKTOP, "Cmd+T")).toBe("⌘T");
    expect(effectiveAliases({ ...defaultShortcuts.newChat, currentBinding: "Cmd+T" }, MAC_DESKTOP)).toEqual([]);
  });

  it("falls back to the unified default in a tab when the remap is a chord the browser keeps", () => {
    expect(label("closeTab", MAC_WEB, "Cmd+W")).toBe("⌘K then X");
    expect(label("reopenLastClosedFile", PC_WEB, "Cmd+Shift+T")).toBe("Ctrl+K then Z");
  });
});

// ── No hardcoded chords ─────────────────────────────────────────────────────

const SRC = join(__dirname, "..", "..");
const COMPONENTS = join(SRC, "components");

/**
 * Files that may spell a registry chord, and why. Keep this short: anything
 * shown as "the key for this action" belongs to shortcutLabel().
 */
const ALLOWED: Record<string, string> = {
  // The remap editor explains the binding syntax itself ("try a Cmd+K sequence").
  "components/Settings/KeyboardShortcutsSettings.tsx": "explains binding syntax",
};

function componentFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const path = join(dir, entry);
    if (statSync(path).isDirectory()) {
      if (entry === "__tests__" || entry === "__preview__") continue;
      out.push(...componentFiles(path));
    } else if (entry.endsWith(".tsx") && !/\.test\.tsx$/.test(entry)) {
      out.push(path);
    }
  }
  return out;
}

/** Drop comments: they may name chords freely. */
function withoutComments(text: string): string {
  return text.replace(/\/\*[\s\S]*?\*\//g, "").replace(/(^|[^:])\/\/[^\n]*/gm, "$1");
}

const KEY = String.raw`(?:Enter|Escape|Up|Down|Left|Right|[A-Z0-9](?![a-z])|[,\[\]/.](?![A-Za-z]))`;
/** "Cmd+Shift+J", "Ctrl+Alt+N" */
const WORD_CHORD = new RegExp(String.raw`\b(?:Cmd|Ctrl)(?:\+(?:Shift|Alt|Ctrl|Option))*\+${KEY}`, "g");
/** "⌘J", "⌘⇧J", "⌥⌘B" */
const GLYPH_CHORD = new RegExp(String.raw`[⌘⌥⇧⌃]*⌘[⌘⌥⇧⌃]*${KEY}`, "g");

/** A literal glyph chord ("⌥⌘B") as an authored binding ("Cmd+Alt+B"). */
function glyphToAuthored(glyphs: string): string {
  const key = glyphs.replace(/[⌘⌥⇧⌃]/g, "");
  const mods = [
    glyphs.includes("⌘") && "Cmd",
    glyphs.includes("⌃") && "Ctrl",
    glyphs.includes("⇧") && "Shift",
    glyphs.includes("⌥") && "Alt",
  ].filter(Boolean);
  return [...mods, key].join("+");
}

/** The first chord of every binding and alias in the registry, on a Mac, both surfaces. */
const REGISTRY_CHORDS = new Set(
  Object.values(defaultShortcuts).flatMap((shortcut) =>
    [shortcut.defaultBinding, shortcut.defaultWebBinding, ...(shortcut.desktopAliases ?? [])]
      .filter(Boolean)
      .map((binding) => parseBinding(binding, true).split(" ")[0]),
  ),
);

describe("component source", () => {
  it("never spells a shortcut's chord by hand", () => {
    const offenders: string[] = [];
    for (const file of componentFiles(COMPONENTS)) {
      const rel = relative(SRC, file);
      if (ALLOWED[rel]) continue;

      const text = withoutComments(readFileSync(file, "utf8"));
      const literals = [
        ...[...text.matchAll(WORD_CHORD)].map((m) => m[0]),
        ...[...text.matchAll(GLYPH_CHORD)].map((m) => glyphToAuthored(m[0])),
      ];
      for (const literal of literals) {
        if (REGISTRY_CHORDS.has(parseBinding(literal, true))) {
          offenders.push(`${rel}: ${literal}`);
        }
      }
    }
    expect(offenders).toEqual([]);
  });

  it("would catch the labels it was written for", () => {
    // Guards the scanner itself: these are the literals it replaced.
    for (const literal of ['"Close tab (Cmd+W)"', '"New terminal (Cmd+Shift+J)"', "'⌘L'", '"Files (Cmd+K F)"']) {
      const hits = [
        ...[...literal.matchAll(WORD_CHORD)].map((m) => m[0]),
        ...[...literal.matchAll(GLYPH_CHORD)].map((m) => glyphToAuthored(m[0])),
      ];
      expect(hits.some((hit) => REGISTRY_CHORDS.has(parseBinding(hit, true)))).toBe(true);
    }
  });
});
