/**
 * Every shortcut declares what it does off the workspace.
 *
 * The keyboard host runs on every authenticated route, but most handlers live
 * in ModernApp. A handler missing from lib/keyboard/scope would be silently
 * absent off the workspace — and a desktop menu item delivering it would do
 * nothing at all. These pin the table to config/shortcuts.yaml.
 */

import { describe, expect, it } from "vitest";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { OFF_WORKSPACE, goesToWorkspace } from "../scope";

const HANDLERS = new Set(
  Object.values(defaultShortcuts).map((shortcut) => shortcut.handler),
);

/** The ids the desktop menu delivers on `menu-shortcut` (electron/src/main.js). */
const MENU_DELIVERED = [
  "newChat",
  "closeTab",
  "reopenLastClosedFile",
  "toggleTerminal",
  "newTerminal",
  "clearTerminal",
  "nextChat",
  "prevChat",
  "nextRightSidebarTab",
  "prevRightSidebarTab",
  "findInPage",
];

describe("off-workspace reach", () => {
  it.each([...HANDLERS])("declares a reach for %s", (handler) => {
    expect(OFF_WORKSPACE[handler]).toBeDefined();
  });

  it("declares nothing for a handler no shortcut has", () => {
    const stale = Object.keys(OFF_WORKSPACE).filter((name) => !HANDLERS.has(name));
    expect(stale).toEqual([]);
  });

  it("reveals through a handler the workspace itself runs", () => {
    for (const [name, reach] of Object.entries(OFF_WORKSPACE)) {
      if (typeof reach !== "object") continue;
      expect({ name, via: OFF_WORKSPACE[reach.reveal] }).toEqual({
        name,
        via: "workspace",
      });
    }
  });

  it.each(MENU_DELIVERED)(
    "acts on %s from any route, because the menu has no key to fall back to",
    (id) => {
      const handler = defaultShortcuts[id]?.handler;
      expect(handler).toBeDefined();
      const reach = OFF_WORKSPACE[handler!];
      expect(reach === "here" || reach === "leave" || goesToWorkspace(reach)).toBe(
        true,
      );
    },
  );

  it("never takes a key off the workspace that it then does nothing with", () => {
    // A sequence completion the host does not run would leak its second key
    // into the page after Cmd+K was already swallowed.
    for (const shortcut of Object.values(defaultShortcuts)) {
      const sequence = [shortcut.defaultBinding, shortcut.defaultWebBinding].some(
        (binding) => binding?.includes(" "),
      );
      if (!sequence) continue;
      expect({ id: shortcut.id, reach: OFF_WORKSPACE[shortcut.handler] }).not.toMatchObject({
        reach: "native",
      });
    }
  });
});
