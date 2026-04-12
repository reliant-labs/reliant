// FindReplace - Find and replace text across files in the workspace
import { useState, useEffect, useRef, useCallback } from "react";
import { createPortal } from "react-dom";
import {
  Search,
  X,
  Loader2,
  FileText,
  ChevronDown,
  ChevronRight,
  Settings2,
  Replace,
  Check,
  AlertCircle,
} from "lucide-react";
import { cn } from "../../lib/utils";
import {
  searchFiles,
  replaceInFiles,
  type SearchResult,
  type SearchMatch,
  type ReplaceInFilesResult,
} from "../../api/fileSystem";
import { useProjectStore } from "../../store/projectStore";
import { useActiveWorktreeId } from "../../store/worktreeStore";
import { useViewerStore } from "../../store/viewerStore";

interface FindReplaceProps {
  isOpen: boolean;
  onClose: () => void;
}

export function FindReplace({ isOpen, onClose }: FindReplaceProps) {
  const [searchText, setSearchText] = useState("");
  const [replaceText, setReplaceText] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [totalMatches, setTotalMatches] = useState(0);
  const [isSearching, setIsSearching] = useState(false);
  const [isReplacing, setIsReplacing] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [replaceResult, setReplaceResult] = useState<ReplaceInFilesResult | null>(null);
  const [expandedFiles, setExpandedFiles] = useState<Set<string>>(new Set());
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const [showOptions, setShowOptions] = useState(false);
  const [selectedFiles, setSelectedFiles] = useState<Set<string>>(new Set());

  // Search options
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [filePattern, setFilePattern] = useState("");

  const searchInputRef = useRef<HTMLInputElement>(null);
  const replaceInputRef = useRef<HTMLInputElement>(null);
  const resultsRef = useRef<HTMLDivElement>(null);
  const searchTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  const currentProject = useProjectStore((state) => state.currentProject);
  
  // Use the global active worktree (from worktreeStore) as the single source of truth
  const effectiveWorktreeId = useActiveWorktreeId();

  // Focus search input when opened
  useEffect(() => {
    if (isOpen) {
      setTimeout(() => searchInputRef.current?.focus(), 50);
    } else {
      // Clear state when closed
      setSearchText("");
      setReplaceText("");
      setResults([]);
      setTotalMatches(0);
      setError(null);
      setReplaceResult(null);
      setExpandedFiles(new Set());
      setHighlightedIndex(0);
      setSelectedFiles(new Set());
    }
  }, [isOpen]);

  // Debounced search
  const performSearch = useCallback(
    async (query: string) => {
      if (!query.trim() || !currentProject) {
        setResults([]);
        setTotalMatches(0);
        setSelectedFiles(new Set());
        return;
      }

      setIsSearching(true);
      setError(null);
      setReplaceResult(null);

      try {
        const response = await searchFiles(query, {
          worktreeId: effectiveWorktreeId,
          caseSensitive,
          filePattern: filePattern || undefined,
          maxResults: 100,
          contextLines: 1,
        });

        setResults(response.results);
        setTotalMatches(response.totalMatches);

        // Auto-expand first result and select all files by default
        if (response.results.length > 0) {
          setExpandedFiles(new Set([response.results[0].path]));
          setSelectedFiles(new Set(response.results.map((r) => r.path)));
        }
      } catch (err) {
        console.error("Search failed:", err);
        setError(err instanceof Error ? err.message : "Search failed");
      } finally {
        setIsSearching(false);
      }
    },
    [currentProject, effectiveWorktreeId, caseSensitive, filePattern]
  );

  // Handle search input change with debounce
  const handleSearchChange = (value: string) => {
    setSearchText(value);

    if (searchTimeoutRef.current) {
      clearTimeout(searchTimeoutRef.current);
    }

    searchTimeoutRef.current = setTimeout(() => {
      performSearch(value);
    }, 300);
  };

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current);
      }
    };
  }, []);

  // Handle replace all
  const handleReplaceAll = async () => {
    if (!searchText.trim() || !currentProject || selectedFiles.size === 0) return;

    setIsReplacing(true);
    setError(null);

    try {
      const result = await replaceInFiles(searchText, replaceText, {
        worktreeId: effectiveWorktreeId,
        caseSensitive,
        filePattern: filePattern || undefined,
        filePaths: Array.from(selectedFiles),
      });

      setReplaceResult(result);

      // Clear search results after successful replace
      if (result.filesModified > 0) {
        setResults([]);
        setTotalMatches(0);
        setSelectedFiles(new Set());
      }
    } catch (err) {
      console.error("Replace failed:", err);
      setError(err instanceof Error ? err.message : "Replace failed");
    } finally {
      setIsReplacing(false);
    }
  };

  // Handle keyboard navigation
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      onClose();
      return;
    }

    // Tab between search and replace inputs
    if (e.key === "Tab" && !e.shiftKey && document.activeElement === searchInputRef.current) {
      e.preventDefault();
      replaceInputRef.current?.focus();
      return;
    }
    if (e.key === "Tab" && e.shiftKey && document.activeElement === replaceInputRef.current) {
      e.preventDefault();
      searchInputRef.current?.focus();
      return;
    }

    if (e.key === "ArrowDown") {
      e.preventDefault();
      setHighlightedIndex((prev) => Math.min(prev + 1, flattenedResults.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setHighlightedIndex((prev) => Math.max(prev - 1, 0));
    } else if (e.key === "Enter" && e.metaKey) {
      e.preventDefault();
      handleReplaceAll();
    } else if (e.key === "Enter") {
      e.preventDefault();
      const item = flattenedResults[highlightedIndex];
      if (item) {
        handleResultClick(item.path, item.match?.lineNumber, false); // Enter - don't auto-focus
      }
    }
  };

  // Flatten results for keyboard navigation
  const flattenedResults: Array<{ path: string; match?: SearchMatch }> = [];
  results.forEach((result) => {
    flattenedResults.push({ path: result.path });
    if (expandedFiles.has(result.path)) {
      result.matches.forEach((match) => {
        flattenedResults.push({ path: result.path, match });
      });
    }
  });

  // Handle clicking on a result
  const handleResultClick = (path: string, lineNumber?: number, shouldFocus: boolean = false) => {
    if (!currentProject) return;

    const file = {
      name: path.split("/").pop() || path,
      path: path,
      type: "file" as const,
      line: lineNumber,
    };

    useViewerStore.getState().openFileViewer(file, currentProject.id, effectiveWorktreeId, shouldFocus);
    onClose();
  };

  // Toggle file expansion
  const toggleFileExpansion = (path: string, e: React.MouseEvent) => {
    e.stopPropagation();
    setExpandedFiles((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  // Toggle file selection
  const toggleFileSelection = (path: string, e: React.MouseEvent) => {
    e.stopPropagation();
    setSelectedFiles((prev) => {
      const next = new Set(prev);
      if (next.has(path)) {
        next.delete(path);
      } else {
        next.add(path);
      }
      return next;
    });
  };

  // Toggle select all files
  const toggleSelectAll = () => {
    if (selectedFiles.size === results.length) {
      setSelectedFiles(new Set());
    } else {
      setSelectedFiles(new Set(results.map((r) => r.path)));
    }
  };

  // Highlight match in line content
  const highlightMatch = (content: string, matchStart: number, matchEnd: number) => {
    const before = content.slice(0, matchStart);
    const match = content.slice(matchStart, matchEnd);
    const after = content.slice(matchEnd);

    return (
      <>
        <span className="text-muted-foreground">{before}</span>
        <span className="bg-yellow-500/30 text-foreground font-medium">{match}</span>
        <span className="text-muted-foreground">{after}</span>
      </>
    );
  };

  if (!isOpen) return null;

  return createPortal(
    <div
      className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]"
      data-modal-open="true"
      onClick={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/50" />

      {/* Modal */}
      <div
        className="relative w-full max-w-2xl bg-background border border-border rounded-lg shadow-2xl overflow-hidden"
        onKeyDown={handleKeyDown}
      >
        {/* Search input */}
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border">
          <Search className="w-4 h-4 text-muted-foreground flex-shrink-0" />
          <input
            ref={searchInputRef}
            type="text"
            value={searchText}
            onChange={(e) => handleSearchChange(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Search for..."
            className="flex-1 bg-transparent text-sm font-mono outline-none placeholder:text-muted-foreground"
          />
          {isSearching && <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />}
          <button
            onClick={() => setShowOptions(!showOptions)}
            className={cn(
              "p-1 rounded hover:bg-muted transition-colors",
              showOptions && "bg-muted"
            )}
            title="Search options"
          >
            <Settings2 className="w-4 h-4 text-muted-foreground" />
          </button>
          <button onClick={onClose} className="p-1 rounded hover:bg-muted transition-colors">
            <X className="w-4 h-4 text-muted-foreground" />
          </button>
        </div>

        {/* Replace input */}
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border bg-muted/20">
          <Replace className="w-4 h-4 text-muted-foreground flex-shrink-0" />
          <input
            ref={replaceInputRef}
            type="text"
            value={replaceText}
            onChange={(e) => setReplaceText(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Replace with..."
            className="flex-1 bg-transparent text-sm font-mono outline-none placeholder:text-muted-foreground"
          />
          <button
            onClick={handleReplaceAll}
            disabled={
              isReplacing || !searchText.trim() || results.length === 0 || selectedFiles.size === 0
            }
            className={cn(
              "flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded transition-colors",
              "bg-primary text-primary-foreground hover:bg-primary/90",
              "disabled:opacity-50 disabled:cursor-not-allowed"
            )}
          >
            {isReplacing ? (
              <Loader2 className="w-3 h-3 animate-spin" />
            ) : (
              <Replace className="w-3 h-3" />
            )}
            Replace All
          </button>
        </div>

        {/* Search options */}
        {showOptions && (
          <div className="px-4 py-2 border-b border-border bg-muted/30 space-y-2">
            <div className="flex items-center gap-4 text-xs">
              <label className="flex items-center gap-1.5 cursor-pointer">
                <input
                  type="checkbox"
                  checked={caseSensitive}
                  onChange={(e) => setCaseSensitive(e.target.checked)}
                  className="w-3 h-3 rounded border-border"
                />
                <span className="text-muted-foreground">Case sensitive</span>
              </label>
            </div>
            <div className="flex items-center gap-2">
              <label className="text-xs text-muted-foreground whitespace-nowrap">
                File pattern:
              </label>
              <input
                type="text"
                value={filePattern}
                onChange={(e) => setFilePattern(e.target.value)}
                placeholder="e.g., *.ts, *.go"
                className="flex-1 px-2 py-1 text-xs font-mono bg-background border border-border rounded outline-none focus:ring-1 focus:ring-primary/50"
              />
            </div>
          </div>
        )}

        {/* Replace result notification */}
        {replaceResult && (
          <div className="px-4 py-3 border-b border-border bg-green-500/10 flex items-center gap-2">
            <Check className="w-4 h-4 text-green-500" />
            <span className="text-sm text-green-700 dark:text-green-400">
              Replaced {replaceResult.totalReplacements} occurrence
              {replaceResult.totalReplacements !== 1 ? "s" : ""} in {replaceResult.filesModified}{" "}
              file{replaceResult.filesModified !== 1 ? "s" : ""}
            </span>
          </div>
        )}

        {/* Results */}
        <div ref={resultsRef} className="max-h-[50vh] overflow-y-auto">
          {error ? (
            <div className="px-4 py-8 text-center">
              <AlertCircle className="w-8 h-8 text-destructive mx-auto mb-2" />
              <p className="text-sm text-destructive font-mono">{error}</p>
            </div>
          ) : results.length === 0 ? (
            <div className="px-4 py-8 text-center">
              {searchText ? (
                <p className="text-sm text-muted-foreground font-mono">
                  No results found for &quot;{searchText}&quot;
                </p>
              ) : (
                <p className="text-sm text-muted-foreground font-mono">
                  Enter search text to find and replace across files
                </p>
              )}
            </div>
          ) : (
            <div>
              {/* Select all header */}
              <div className="flex items-center gap-2 px-4 py-2 border-b border-border bg-muted/30">
                <input
                  type="checkbox"
                  checked={selectedFiles.size === results.length && results.length > 0}
                  onChange={toggleSelectAll}
                  className="w-3.5 h-3.5 rounded border-border"
                />
                <span className="text-xs text-muted-foreground">
                  {selectedFiles.size} of {results.length} files selected ({totalMatches} matches)
                </span>
              </div>

              {results.map((result, _fileIndex) => {
                const isExpanded = expandedFiles.has(result.path);
                const isSelected = selectedFiles.has(result.path);
                const fileIsHighlighted =
                  flattenedResults[highlightedIndex]?.path === result.path &&
                  !flattenedResults[highlightedIndex]?.match;

                return (
                  <div key={result.path}>
                    {/* File header */}
                    <div
                      className={cn(
                        "w-full flex items-center gap-2 px-4 py-2 hover:bg-muted/50 transition-colors text-left cursor-pointer",
                        fileIsHighlighted && "bg-muted"
                      )}
                    >
                      <input
                        type="checkbox"
                        checked={isSelected}
                        onChange={(e) => toggleFileSelection(result.path, e as any)}
                        className="w-3.5 h-3.5 rounded border-border"
                        onClick={(e) => e.stopPropagation()}
                      />
                      <button
                        onClick={(e) => toggleFileExpansion(result.path, e)}
                        className="flex-shrink-0"
                      >
                        {isExpanded ? (
                          <ChevronDown className="w-3 h-3 text-muted-foreground" />
                        ) : (
                          <ChevronRight className="w-3 h-3 text-muted-foreground" />
                        )}
                      </button>
                      <FileText className="w-4 h-4 text-primary flex-shrink-0" />
                      <span
                        className="text-sm font-mono truncate flex-1"
                        onClick={() => handleResultClick(result.path, undefined, false)} // Click - don't auto-focus
                      >
                        {result.path}
                      </span>
                      <span className="text-xs text-muted-foreground">
                        {result.matches.length} match{result.matches.length !== 1 ? "es" : ""}
                      </span>
                    </div>

                    {/* Matches */}
                    {isExpanded && (
                      <div className="bg-muted/20">
                        {result.matches.map((match, matchIndex) => {
                          const isMatchHighlighted =
                            flattenedResults[highlightedIndex]?.path === result.path &&
                            flattenedResults[highlightedIndex]?.match?.lineNumber ===
                              match.lineNumber;

                          return (
                            <button
                              key={`${result.path}-${match.lineNumber}-${matchIndex}`}
                              onClick={() => handleResultClick(result.path, match.lineNumber, false)} // Click - don't auto-focus
                              className={cn(
                                "w-full flex items-start gap-3 px-4 py-1.5 hover:bg-muted/50 transition-colors text-left pl-12",
                                isMatchHighlighted && "bg-muted"
                              )}
                            >
                              <span className="text-xs text-muted-foreground font-mono w-8 flex-shrink-0 text-right">
                                {match.lineNumber}
                              </span>
                              <span className="text-xs font-mono truncate flex-1 whitespace-pre">
                                {highlightMatch(
                                  match.lineContent,
                                  match.matchStart,
                                  match.matchEnd
                                )}
                              </span>
                            </button>
                          );
                        })}
                      </div>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </div>

        {/* Footer with shortcuts */}
        <div className="px-4 py-2 border-t border-border bg-muted/30 flex items-center justify-between text-xs text-muted-foreground">
          <div className="flex items-center gap-3">
            <span>
              <kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Tab</kbd> Switch
              fields
            </span>
            <span>
              <kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">⌘+Enter</kbd>{" "}
              Replace all
            </span>
            <span>
              <kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Esc</kbd> Close
            </span>
          </div>
        </div>
      </div>
    </div>,
    document.body
  );
}