// Copyright (c) 2025 Reliant Labs

/**
 * Open the project selector from anywhere (the Switch Project shortcut, the
 * header, ⌘K's "Switch project…", the tray). The keyboard host
 * (components/Shortcuts/ShortcutHost) listens for the event and floats the
 * selector over the page (Projects/SwitchProjectDialog), so it opens on every
 * desktop route without the sidebar having to be there.
 */

export const OPEN_PROJECT_SWITCHER_EVENT = "open-project-switcher";

export function openProjectSwitcher(): void {
  window.dispatchEvent(new CustomEvent(OPEN_PROJECT_SWITCHER_EVENT));
}
