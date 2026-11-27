// Copyright (c) 2026 Reliant Labs

/**
 * The nav's Find box, under the org row (U6c, owner item 7: Vercel's "Find  F").
 * It opens the existing command palette (the keyboard host's, so it searches
 * the same commands and settings everywhere). F opens it from anywhere you are
 * not typing; the palette's own chord still works.
 *
 * A host with no keyboard host (the phone's drawer, on `/m/*`) hands it
 * `onOpen` instead: there, Find opens the phone's own Search screen.
 */

import { useEffect } from "react";
import { Search } from "lucide-react";

import { isInputFocused } from "@/hooks/useFocusManager";
import { useShortcutHost } from "@/hooks/useShortcutHost";
import { useShortcutLabel } from "@/store/shortcutsStore";

function typingIn(target: EventTarget | null): boolean {
  const element = target as HTMLElement | null;
  return !!element?.closest?.("input, textarea, select, [contenteditable=''], [contenteditable='true'], .monaco-editor");
}

export function FindBox({ onOpen }: { onOpen?: () => void } = {}) {
  const host = useShortcutHost();
  const paletteShortcut = useShortcutLabel("commandPalette");
  const open = () => (onOpen ? onOpen() : void host?.run("onCommandPalette"));

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.defaultPrevented || event.repeat || event.metaKey || event.ctrlKey || event.altKey) return;
      if (event.key !== "f" && event.key !== "F") return;
      if (typingIn(event.target) || isInputFocused() || document.querySelector('[aria-modal="true"], [role="dialog"]')) return;
      event.preventDefault();
      if (onOpen) onOpen();
      else void host?.run("onCommandPalette");
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [host, onOpen]);

  return (
    <div className="shrink-0 px-2 py-1">
      <button
        type="button"
        onClick={open}
        data-testid="nav-find"
        aria-label="Find"
        aria-keyshortcuts={paletteShortcut ? `F ${paletteShortcut}` : "F"}
        title={paletteShortcut ? `Find (F, or ${paletteShortcut})` : "Find (F)"}
        className="flex h-[var(--row-h)] w-full items-center gap-2 rounded-md border border-border bg-background px-2 text-left text-ui text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <Search className="size-4 shrink-0" aria-hidden="true" />
        <span className="min-w-0 flex-1 truncate">Find…</span>
        <kbd className="rounded border border-border px-1.5 font-sans text-meta text-muted-foreground">F</kbd>
      </button>
    </div>
  );
}
