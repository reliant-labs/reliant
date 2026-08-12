/**
 * `/m/search` — search entry point for the two desktop search features that
 * were keyboard-shortcut-only (Cmd+Shift+C / Cmd+Shift+F) and therefore
 * invisible on a phone despite both being responsive.
 *
 * One search box, a segmented control for "Chats" / "Files" swapping which
 * tab consumes the query — not two separate screens, since a user typing a
 * search term rarely knows in advance which corpus has the answer.
 */

import { useState } from "react";
import { Link } from "@tanstack/react-router";
import { ChevronLeft, Search, X } from "lucide-react";
import { cn } from "../../lib/utils";
import { useCapability } from "../../lib/surfaceContext";
import { MobileChatSearchTab } from "./MobileChatSearchTab";
import { MobileFileSearchTab } from "./MobileFileSearchTab";

type SearchMode = "chats" | "files";

const SEARCH_MODES: { value: SearchMode; label: string }[] = [
  { value: "chats", label: "Chats" },
  { value: "files", label: "Files" },
];

export function MobileSearchScreen() {
  const [mode, setMode] = useState<SearchMode>("chats");
  const [query, setQuery] = useState("");
  const chatsEnabled = useCapability("searchChats");
  const filesEnabled = useCapability("searchFiles");

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header className="flex min-h-[56px] shrink-0 items-center gap-2 border-b border-border bg-background px-2">
        <Link
          to="/m/chats"
          className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
          aria-label="Back"
        >
          <ChevronLeft className="h-5 w-5" />
        </Link>
        {/* `bg-muted` and no border: a filled field reads as an input on a
            phone, where a hairline outline on a same-colored background is
            nearly invisible. Full `rounded-full` matches the platform idiom
            for a search field specifically. */}
        <div className="flex min-h-[44px] flex-1 items-center gap-2 rounded-full bg-muted px-3.5">
          <Search className="h-4 w-4 shrink-0 text-muted-foreground" />
          <input
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={mode === "chats" ? "Search chats…" : "Search file contents…"}
            autoFocus
            className="min-h-[44px] flex-1 bg-transparent text-sm outline-none placeholder:text-muted-foreground"
          />
          {query && (
            <button
              type="button"
              onClick={() => setQuery("")}
              className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-foreground/10 text-muted-foreground active:bg-foreground/20"
              aria-label="Clear search"
            >
              <X className="h-3.5 w-3.5" />
            </button>
          )}
        </div>
      </header>

      {chatsEnabled && filesEnabled && (
        // A pill segmented control rather than two underlined tabs: underline
        // tabs read as a document's section nav, and this is a mode switch on
        // one query. The track is the page's own muted surface with the
        // selected segment raised out of it.
        <div className="shrink-0 px-4 pt-4">
          <div className="flex gap-1 rounded-lg bg-muted p-1">
            {SEARCH_MODES.map(({ value, label }) => (
              <button
                key={value}
                type="button"
                onClick={() => setMode(value)}
                aria-pressed={mode === value}
                className={cn(
                  "flex min-h-[44px] flex-1 items-center justify-center rounded-md text-sm font-medium",
                  // Filled primary for the selection rather than a raised
                  // surface: `--surface-raised` resolves to `--muted` in dark
                  // mode, which is this track's own color, so a raised segment
                  // would be invisible in exactly half the themes.
                  mode === value
                    ? "bg-primary text-primary-foreground"
                    : "text-muted-foreground active:bg-foreground/5",
                )}
              >
                {label}
              </button>
            ))}
          </div>
        </div>
      )}

      {/*
        No overflow-y-auto here: MobileFileSearchTab owns its own scroll
        region so its options toggle stays pinned above the result list.
        MobileChatSearchTab is a plain list and scrolls fine inside this
        min-h-0 flex child either way.
      */}
      <div className="min-h-0 flex-1">
        {mode === "chats" && chatsEnabled && (
          <div className="h-full overflow-y-auto">
            <MobileChatSearchTab query={query} />
          </div>
        )}
        {mode === "files" && filesEnabled && <MobileFileSearchTab query={query} />}
      </div>
    </div>
  );
}
