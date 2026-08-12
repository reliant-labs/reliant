/**
 * Files tab of `/m/search` — content search with regex/glob filters, tapping
 * through to a read-only preview.
 *
 * The desktop `GlobalSearch.tsx` modal has a keyboard-nav footer and an
 * expand/collapse-per-file result tree built for a mouse; this keeps the
 * same `searchFiles` call (regex, case-sensitivity, file-pattern glob — all
 * server-side, same RPC) but flattens results into one scrollable list of
 * matches, since collapsing/expanding per file is a desktop density
 * optimization that doesn't help on a narrow screen. Tapping a match opens
 * `MobileFilePreview` — the same Prism-based, non-Monaco viewer the Files
 * workspace drill-in uses (see that component's module comment) — rather
 * than the desktop `FileViewerTab`.
 */

import { useEffect, useRef, useState } from "react";
import {
  ChevronLeft,
  FileSearch,
  FileText,
  Loader2,
  SearchX,
  Settings2,
} from "lucide-react";
import { searchFiles, type SearchResult } from "../../api/fileSystem";
import { useProjectStore } from "../../store/projectStore";
import { useActiveWorktreeId } from "../../store/worktreeStore";
import { cn } from "../../lib/utils";
import { MobileFilePreview } from "./MobileFilePreview";
import { MobileEmptyState } from "./MobileChrome";

interface FlatMatch {
  path: string;
  lineNumber: number;
  lineContent: string;
  matchStart: number;
  matchEnd: number;
}

function highlightMatch(content: string, start: number, end: number) {
  return (
    <>
      <span className="text-muted-foreground">{content.slice(0, start)}</span>
      <span className="bg-yellow-500/30 font-medium text-foreground">
        {content.slice(start, end)}
      </span>
      <span className="text-muted-foreground">{content.slice(end)}</span>
    </>
  );
}

export function MobileFileSearchTab({ query }: { query: string }) {
  const currentProject = useProjectStore((s) => s.currentProject);
  const worktreeId = useActiveWorktreeId();

  const [results, setResults] = useState<SearchResult[]>([]);
  const [totalMatches, setTotalMatches] = useState(0);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showOptions, setShowOptions] = useState(false);
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [filePattern, setFilePattern] = useState("");
  const [preview, setPreview] = useState<{ path: string } | null>(null);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);

    const trimmed = query.trim();
    if (!trimmed || !currentProject) {
      setResults([]);
      setTotalMatches(0);
      return;
    }

    debounceRef.current = setTimeout(async () => {
      setIsLoading(true);
      setError(null);
      try {
        const response = await searchFiles(trimmed, {
          worktreeId,
          caseSensitive,
          filePattern: filePattern || undefined,
          maxResults: 50,
          contextLines: 0,
        });
        setResults(response.results);
        setTotalMatches(response.totalMatches);
      } catch (err) {
        console.error("File search failed:", err);
        setError(err instanceof Error ? err.message : "Search failed");
      } finally {
        setIsLoading(false);
      }
    }, 300);

    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, [query, currentProject, worktreeId, caseSensitive, filePattern]);

  const flatMatches: FlatMatch[] = results.flatMap((r) =>
    r.matches.map((m) => ({
      path: r.path,
      lineNumber: m.lineNumber,
      lineContent: m.lineContent,
      matchStart: m.matchStart,
      matchEnd: m.matchEnd,
    })),
  );

  if (preview) {
    return (
      <div className="flex h-full min-h-0 flex-col">
        <div className="flex min-h-[44px] items-center gap-2 border-b border-border px-2">
          <button
            type="button"
            onClick={() => setPreview(null)}
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
            aria-label="Back to results"
          >
            <ChevronLeft className="h-5 w-5" />
          </button>
          <FileText className="h-4 w-4 shrink-0 text-muted-foreground" />
          <span className="truncate text-sm font-medium">{preview.path}</span>
        </div>
        <MobileFilePreview path={preview.path} worktreeId={worktreeId} />
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex shrink-0 items-center justify-between border-b border-border px-4 py-2">
        <span className="text-xs text-muted-foreground">
          {query.trim()
            ? `${totalMatches} match${totalMatches !== 1 ? "es" : ""} in ${results.length} file${results.length !== 1 ? "s" : ""}`
            : "Type to search file contents"}
        </span>
        <button
          type="button"
          onClick={() => setShowOptions((v) => !v)}
          className={cn(
            "flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md",
            showOptions && "bg-muted",
          )}
          aria-label="Search options"
        >
          <Settings2 className="h-4 w-4 text-muted-foreground" />
        </button>
      </div>

      {showOptions && (
        <div className="shrink-0 space-y-2 border-b border-border bg-muted/30 px-4 py-3">
          <label className="flex min-h-[44px] items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={caseSensitive}
              onChange={(e) => setCaseSensitive(e.target.checked)}
              className="h-4 w-4 rounded border-border"
            />
            <span className="text-muted-foreground">Case sensitive</span>
          </label>
          <div className="flex items-center gap-2">
            <label className="text-xs text-muted-foreground">Files:</label>
            <input
              type="text"
              value={filePattern}
              onChange={(e) => setFilePattern(e.target.value)}
              placeholder="*.ts, *.go"
              className="min-h-[44px] flex-1 rounded-md border border-border bg-background px-2 py-1 font-mono text-xs outline-none focus:ring-1 focus:ring-primary/50"
            />
          </div>
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto">
        {isLoading && flatMatches.length === 0 ? (
          <div className="flex items-center justify-center py-10">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          </div>
        ) : error ? (
          <div className="px-4 py-8 text-center">
            <p className="text-sm text-destructive">{error}</p>
          </div>
        ) : flatMatches.length === 0 ? (
          query.trim() ? (
            <MobileEmptyState
              icon={SearchX}
              title="No matches"
              description={`Nothing in this workspace matches "${query.trim()}". Check the file filter, or try a shorter term.`}
            />
          ) : (
            <MobileEmptyState
              icon={FileSearch}
              title="Search your workspace"
              description="Enter a term above to find it across every file in this workspace."
            />
          )
        ) : (
          <div className="space-y-2 px-4 py-4">
            {flatMatches.map((match, i) => (
              <button
                key={`${match.path}-${match.lineNumber}-${i}`}
                type="button"
                onClick={() => setPreview({ path: match.path })}
                // Card per match, not a hairline-separated stack: each match is
                // a path line plus a wrapped code line, and without a container
                // there was no visual boundary telling you which code line
                // belonged to which path.
                className="flex w-full min-h-[44px] flex-col items-start gap-1 rounded-lg px-4 py-3 text-left elevation-1 active:bg-foreground/5"
              >
                <span className="flex w-full items-center gap-1.5 text-xs text-muted-foreground">
                  <FileText className="h-3 w-3 shrink-0 text-primary" />
                  <span className="truncate">{match.path}</span>
                  <span className="shrink-0">:{match.lineNumber}</span>
                </span>
                <span className="w-full truncate whitespace-pre font-mono text-xs">
                  {highlightMatch(match.lineContent, match.matchStart, match.matchEnd)}
                </span>
              </button>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
