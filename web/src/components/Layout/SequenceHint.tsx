/**
 * Sequence hint.
 *
 * When a sequence prefix is armed (Cmd+K), this shows what the next keystroke
 * can be. Without it, 24 of the app's shortcuts are effectively invisible:
 * pressing Cmd+K appears to do nothing, and there is no way to discover the
 * second key short of reading the settings page.
 *
 * This is the same affordance Vim's which-key and VS Code's chord hint provide,
 * and it is what makes a two-key sequence learnable rather than a memory test.
 *
 * It renders only while a prefix is pending, so it costs nothing the rest of
 * the time and never needs dismissing — the dispatcher's own timeout clears it.
 */

import { useMemo } from "react";
import { createPortal } from "react-dom";
import { useShortcutsStore } from "../../store/shortcutsStore";
import { parseBinding, sequencePrefix, isSequence } from "../../lib/keyboard/chord";
import { detectPlatform, formatBinding } from "../../lib/keyboard/platform";

interface SequenceHintProps {
  /** Canonical armed prefix ("meta+K"), or "" when idle. */
  pending: string;
}

interface HintEntry {
  /** The completing key, formatted for display ("T"). */
  key: string;
  name: string;
  category: string;
}

export function SequenceHint({ pending }: SequenceHintProps) {
  const shortcuts = useShortcutsStore((state) => state.shortcuts);

  const entries = useMemo<HintEntry[]>(() => {
    if (!pending) return [];
    const { isMac, isDesktop } = detectPlatform();

    const matches: HintEntry[] = [];
    for (const shortcut of Object.values(shortcuts)) {
      const authored =
        shortcut.currentBinding ??
        (isDesktop ? shortcut.defaultBinding : shortcut.defaultWebBinding);
      if (!authored) continue;

      const binding = parseBinding(authored, isMac);
      if (!isSequence(binding)) continue;
      if (sequencePrefix(binding) !== pending) continue;

      // Everything after the prefix is what the user still has to press.
      const completion = binding.slice(pending.length + 1);
      matches.push({
        key: formatBinding(completion, isMac),
        name: shortcut.name,
        category: shortcut.category,
      });
    }

    return matches.sort(
      (a, b) =>
        a.category.localeCompare(b.category) || a.key.localeCompare(b.key),
    );
  }, [pending, shortcuts]);

  if (!pending || entries.length === 0) return null;

  const { isMac } = detectPlatform();
  const grouped = entries.reduce<Record<string, HintEntry[]>>((acc, entry) => {
    (acc[entry.category] ??= []).push(entry);
    return acc;
  }, {});

  return createPortal(
    <div
      // Non-interactive: it must never steal the keystroke it is describing.
      className="pointer-events-none fixed bottom-4 left-1/2 z-50 w-[min(90vw,44rem)] -translate-x-1/2"
      role="status"
      aria-live="polite"
    >
      <div className="rounded-lg border border-border bg-popover/95 shadow-lg backdrop-blur">
        <div className="flex items-center gap-2 border-b border-border px-3 py-1.5">
          <kbd className="rounded border border-border/60 bg-muted px-1.5 py-0.5 font-mono text-[11px]">
            {formatBinding(pending, isMac)}
          </kbd>
          <span className="text-xs text-muted-foreground">
            waiting for the next key…
          </span>
        </div>

        <div className="grid max-h-56 grid-cols-2 gap-x-6 gap-y-0.5 overflow-y-auto px-3 py-2 sm:grid-cols-3">
          {Object.entries(grouped).map(([category, items]) => (
            <div key={category} className="min-w-0">
              <div className="truncate pb-0.5 text-[10px] font-medium uppercase tracking-wide text-muted-foreground/70">
                {category}
              </div>
              {items.map((entry) => (
                <div
                  key={`${category}-${entry.key}`}
                  className="flex items-baseline gap-2 py-px"
                >
                  <kbd className="min-w-[1.25rem] shrink-0 rounded border border-border/60 bg-muted px-1 text-center font-mono text-[11px]">
                    {entry.key}
                  </kbd>
                  <span className="truncate text-xs text-foreground/90">
                    {entry.name}
                  </span>
                </div>
              ))}
            </div>
          ))}
        </div>
      </div>
    </div>,
    document.body,
  );
}
