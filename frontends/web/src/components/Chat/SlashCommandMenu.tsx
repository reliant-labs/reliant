/**
 * Slash command menu.
 *
 * Typing "/" at the start of an empty composer opens a small menu of common
 * actions anchored above the input. It is a discovery surface, not a second
 * command system: entries come from the shared command registry, so an action
 * added once is reachable from the palette, a keyboard shortcut, and here.
 *
 * Two ways in:
 *   - Cmd+/ opens it explicitly from anywhere in the composer. This is the
 *     discoverable path and works even mid-sentence.
 *   - Typing "/" as the FIRST character opens it too, matching what people
 *     expect from Slack and Linear. A slash inside a path or URL types
 *     normally, so "src/lib" never summons a menu.
 *
 * Interaction rules:
 *   - A space closes it — the user is writing prose, not naming a command.
 *   - Typing filters; Arrow keys move; Enter or Tab runs; Escape dismisses.
 *   - Dismissing never eats the text; whatever was typed stays put.
 *
 * The menu is imperative on purpose: the composer owns the text and the caret,
 * and needs to ask "did you take this key?" before applying its own Enter and
 * Escape behavior.
 */

import {
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import { cn } from "../../lib/utils";

export interface SlashCommand {
  id: string;
  title: string;
  description: string;
  icon?: React.ReactNode;
  keywords?: string[];
  /**
   * The command's keyboard shortcut, already formatted for display ("⌘E").
   * Shown beside the entry so the menu teaches the shortcut rather than
   * competing with it.
   */
  shortcut?: string;
  /** Runs when chosen. The composer is cleared first. */
  action: () => void;
}

export interface SlashCommandMenuHandle {
  isOpen: () => boolean;
  /** Returns true when the menu consumed the key. */
  handleKeyDown: (e: React.KeyboardEvent) => boolean;
  /** Open explicitly (Cmd+/), independent of what the composer contains. */
  open: () => void;
  /** Close without running anything. */
  dismiss: () => void;
}

interface SlashCommandMenuProps {
  /** Current composer text — the menu derives its query from it. */
  value: string;
  commands: SlashCommand[];
  /** Element to anchor against, normally the composer. */
  anchorRef: React.RefObject<HTMLElement | null>;
  /** Clear the composer; called before an action runs. */
  onConsume: () => void;
}

const MENU_MAX_HEIGHT = 320;
const ROW_HEIGHT = 56;
const VIEWPORT_PADDING = 8;

/** Whether the text currently constitutes a slash-command query. */
function parseQuery(value: string): string | null {
  if (!value.startsWith("/")) return null;
  // A space means the user moved on to prose.
  if (/\s/.test(value)) return null;
  return value.slice(1).toLowerCase();
}

export const SlashCommandMenu = forwardRef<
  SlashCommandMenuHandle,
  SlashCommandMenuProps
>(function SlashCommandMenu({ value, commands, anchorRef, onConsume }, ref) {
  const [highlighted, setHighlighted] = useState(0);
  const [dismissed, setDismissed] = useState(false);
  const [position, setPosition] = useState<{ left: number; top: number } | null>(
    null,
  );
  const listRef = useRef<HTMLDivElement>(null);

  // The imperative handler runs several key events between renders (ArrowDown
  // then Enter arrive back to back), so it must read the CURRENT selection
  // rather than the value captured when the handle was last built.
  const highlightedRef = useRef(0);
  highlightedRef.current = highlighted;

  // Opened explicitly via Cmd+/ rather than by typing a slash. In that mode the
  // composer text is a plain message, so the whole list shows and typing does
  // not filter — the user asked for the menu, not for a query.
  const [explicitlyOpen, setExplicitlyOpen] = useState(false);

  const typedQuery = parseQuery(value);
  const query = explicitlyOpen ? "" : typedQuery;

  const matches = useMemo(() => {
    if (query === null) return [];
    if (!query) return commands;
    return commands.filter((command) => {
      const haystack = [
        command.id,
        command.title,
        command.description,
        ...(command.keywords ?? []),
      ]
        .join(" ")
        .toLowerCase();
      return haystack.includes(query);
    });
  }, [commands, query]);

  // An empty result set is not an open menu — never block the composer with an
  // empty popup.
  const visible = query !== null && matches.length > 0 && !dismissed;

  // Re-arm once the user clears the slash, so dismissing does not disable the
  // feature for the rest of the message.
  useEffect(() => {
    if (typedQuery === null && dismissed) setDismissed(false);
  }, [typedQuery, dismissed]);

  // Keep the highlight inside the (possibly narrowed) list.
  useEffect(() => {
    if (highlightedRef.current >= matches.length) {
      highlightedRef.current = 0;
      setHighlighted(0);
    }
  }, [matches.length]);

  // Anchor above the composer. Layout effect so it never paints misplaced.
  useLayoutEffect(() => {
    if (!visible) {
      setPosition(null);
      return;
    }
    const anchor = anchorRef.current;
    if (!anchor) return;

    const rect = anchor.getBoundingClientRect();
    const height = Math.min(MENU_MAX_HEIGHT, matches.length * ROW_HEIGHT + 16);

    // Prefer opening upward: the composer sits at the bottom of the window.
    let top = rect.top - height - 8;
    if (top < VIEWPORT_PADDING) top = rect.bottom + 8;

    setPosition({ left: Math.max(VIEWPORT_PADDING, rect.left), top });
  }, [visible, matches.length, anchorRef]);

  // Keep the highlighted row scrolled into view.
  useEffect(() => {
    if (!visible || !listRef.current) return;
    listRef.current
      .querySelector(`[data-index="${highlighted}"]`)
      ?.scrollIntoView({ block: "nearest" });
  }, [highlighted, visible]);

  const run = useCallback(
    (command: SlashCommand) => {
      // Clear the "/..." query so the composer is not left holding a command
      // string the user never intended to send. When the menu was opened with
      // Cmd+/ the text is a real message in progress, so leave it alone.
      if (!explicitlyOpen) onConsume();
      setDismissed(false);
      setExplicitlyOpen(false);
      command.action();
    },
    [onConsume, explicitlyOpen],
  );

  useImperativeHandle(
    ref,
    () => ({
      isOpen: () => visible,
      open: () => {
        setExplicitlyOpen(true);
        setDismissed(false);
        highlightedRef.current = 0;
        setHighlighted(0);
      },
      dismiss: () => {
        setDismissed(true);
        setExplicitlyOpen(false);
      },
      handleKeyDown: (e: React.KeyboardEvent) => {
        if (!visible) return false;

        switch (e.key) {
          case "ArrowDown":
            e.preventDefault();
            highlightedRef.current =
              (highlightedRef.current + 1) % matches.length;
            setHighlighted(highlightedRef.current);
            return true;
          case "ArrowUp":
            e.preventDefault();
            highlightedRef.current =
              (highlightedRef.current - 1 + matches.length) % matches.length;
            setHighlighted(highlightedRef.current);
            return true;
          case "Enter":
          case "Tab":
            e.preventDefault();
            run(matches[highlightedRef.current]);
            return true;
          case "Escape":
            e.preventDefault();
            setDismissed(true);
            setExplicitlyOpen(false);
            return true;
          default:
            return false;
        }
      },
    }),
    [visible, matches, run],
  );

  if (!visible || !position) return null;

  return createPortal(
    <div
      data-slash-menu-open="true"
      role="listbox"
      aria-label="Slash commands"
      className="fixed z-50 w-80 overflow-hidden rounded-lg border border-border bg-popover shadow-lg"
      style={{ left: position.left, top: position.top }}
    >
      <div ref={listRef} className="max-h-80 overflow-y-auto py-1">
        {matches.map((command, index) => (
          <button
            key={command.id}
            type="button"
            data-index={index}
            role="option"
            aria-selected={index === highlighted}
            className={cn(
              "flex w-full items-start gap-3 px-3 py-2 text-left transition-colors",
              index === highlighted ? "bg-accent" : "hover:bg-accent/50",
            )}
            // Keep focus in the composer: losing it moves the caret and closes
            // the menu before the click can land.
            onMouseDown={(e) => e.preventDefault()}
            onMouseEnter={() => setHighlighted(index)}
            onClick={() => run(command)}
          >
            {command.icon && (
              <span className="mt-0.5 shrink-0 text-muted-foreground">
                {command.icon}
              </span>
            )}
            <span className="min-w-0 flex-1">
              <span className="block truncate text-sm font-medium">
                {command.title}
              </span>
              <span className="block truncate text-xs text-muted-foreground">
                {command.description}
              </span>
            </span>
            {/* Show the key that does the same thing, so the menu teaches the
                shortcut instead of becoming a substitute for learning it. */}
            {command.shortcut && (
              <kbd className="mt-0.5 shrink-0 rounded border border-border/60 bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
                {command.shortcut}
              </kbd>
            )}
          </button>
        ))}
      </div>
      <div className="border-t border-border px-3 py-1.5 text-xs text-muted-foreground">
        ↑↓ navigate · ↵ run · esc dismiss
      </div>
    </div>,
    document.body,
  );
});
