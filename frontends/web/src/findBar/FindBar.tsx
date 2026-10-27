/**
 * The desktop app's find-in-page bar: the whole page of the small view the
 * Electron main process lays over a window (see ./protocol.ts for why it is
 * not part of the page it searches).
 *
 * Chrome's find bar, in the app's own tokens: type to search, Enter and
 * Shift+Enter (or Cmd+G and Cmd+Shift+G) for the next and previous match,
 * Escape to close, which also clears the highlights. Cmd+F while it is open
 * selects the query again, the way Cmd+F does on a page whose bar is already
 * up.
 */

import { useCallback, useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { ChevronDown, ChevronUp, X } from "lucide-react";
import { cn } from "../lib/utils";
import { detectIsMac } from "../lib/keyboard/platform";
import { applyTheme, type FindBarAPI, type FindResult } from "./protocol";

export interface FindBarProps {
  api: FindBarAPI;
  /** Whether Cmd means Meta (macOS) or Control. */
  isMac?: boolean;
}

export function FindBar({ api, isMac = detectIsMac() }: FindBarProps) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");
  // The latest search's standing; null while one is on its way.
  const [result, setResult] = useState<FindResult | null>(null);
  // Read by the main process's callbacks, which outlive any one render.
  const queryRef = useRef(query);
  queryRef.current = query;
  const composing = useRef(false);

  /** A new search for `text` — or, for no text, the end of the last one. */
  const search = useCallback(
    (text: string) => {
      setResult(null);
      api.find({ text, forward: true, findNext: true, matchCase: false });
    },
    [api],
  );

  const step = useCallback(
    (forward: boolean) => {
      const text = queryRef.current;
      if (!text) return;
      api.find({ text, forward, findNext: false, matchCase: false });
    },
    [api],
  );

  useEffect(() => {
    const offOpened = api.onOpened(({ theme }) => {
      applyTheme(theme);
      inputRef.current?.focus();
      inputRef.current?.select();
      // Closing cleared the highlights; reopening on the same query shows
      // them again, as Chrome does.
      if (queryRef.current) search(queryRef.current);
    });
    const offResult = api.onResult(setResult);
    // Only now: an open request sent before this page listened would be lost.
    api.ready();
    return () => {
      offOpened();
      offResult();
    };
  }, [api, search]);

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    // Enter confirms an IME composition; it is not a search step.
    if (event.nativeEvent.isComposing) return;
    const mod = isMac ? event.metaKey : event.ctrlKey;
    const key = event.key.toLowerCase();

    if (event.key === "Enter" || (mod && key === "g")) {
      event.preventDefault();
      step(!event.shiftKey);
    } else if (event.key === "Escape") {
      event.preventDefault();
      api.close();
    } else if (mod && key === "f" && !event.shiftKey) {
      event.preventDefault();
      inputRef.current?.select();
    }
  };

  const hasMatches = (result?.matches ?? 0) > 0;
  const noMatches = query !== "" && result !== null && result.matches === 0;

  return (
    // The view is a little larger than the bar, and transparent around it,
    // so the shadow has room to fall.
    <div className="flex h-full w-full items-start p-2">
      <div
        role="search"
        className="flex h-10 w-full items-center gap-1 rounded-lg border border-border bg-popover pl-2 pr-1 text-popover-foreground shadow-lg"
      >
        <input
          ref={inputRef}
          type="text"
          aria-label="Find in page"
          aria-invalid={noMatches || undefined}
          placeholder="Find in page"
          autoComplete="off"
          spellCheck={false}
          value={query}
          onChange={(event) => {
            const text = event.target.value;
            setQuery(text);
            if (!composing.current) search(text);
          }}
          onCompositionStart={() => {
            composing.current = true;
          }}
          onCompositionEnd={(event) => {
            composing.current = false;
            search(event.currentTarget.value);
          }}
          onKeyDown={onKeyDown}
          className="h-7 min-w-0 flex-1 rounded-md border border-border/60 bg-background px-2 text-sm text-foreground placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        />
        <span
          aria-live="polite"
          data-testid="find-count"
          className={cn(
            "w-14 shrink-0 text-right text-xs tabular-nums text-muted-foreground",
            noMatches && "text-destructive",
          )}
        >
          {query && result ? `${result.activeMatchOrdinal}/${result.matches}` : ""}
        </span>
        <BarButton label="Previous match" disabled={!hasMatches} onClick={() => step(false)}>
          <ChevronUp className="h-4 w-4" />
        </BarButton>
        <BarButton label="Next match" disabled={!hasMatches} onClick={() => step(true)}>
          <ChevronDown className="h-4 w-4" />
        </BarButton>
        <BarButton label="Close" onClick={api.close}>
          <X className="h-4 w-4" />
        </BarButton>
      </div>
    </div>
  );
}

function BarButton({
  label,
  disabled,
  onClick,
  children,
}: {
  label: string;
  disabled?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-label={label}
      title={label}
      disabled={disabled}
      // Keep focus in the query, so typing and Enter carry on after a click.
      onMouseDown={(event) => event.preventDefault()}
      onClick={onClick}
      className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:pointer-events-none disabled:opacity-40"
    >
      {children}
    </button>
  );
}
