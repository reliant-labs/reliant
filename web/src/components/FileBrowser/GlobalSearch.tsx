// GlobalSearch - Search across all files in the workspace
import { useState, useEffect, useRef, useCallback, useMemo } from "react";
import { createPortal } from "react-dom";
import { Search, X, Loader2, FileText, ChevronDown, ChevronRight, Settings2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { searchFiles, type SearchResult, type SearchMatch } from "../../api/fileSystem";
import { isDaemonConnectingError } from "../../lib/daemon-errors";
import { useProjectStore } from "../../store/projectStore";
import { useActiveWorktreeId } from "../../store/worktreeStore";
import { useViewerStore } from "../../store/viewerStore";

interface GlobalSearchProps {
  isOpen: boolean;
  onClose: () => void;
}

export function GlobalSearch({ isOpen, onClose }: GlobalSearchProps) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<SearchResult[]>([]);
  const [totalMatches, setTotalMatches] = useState(0);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [expandedFiles, setExpandedFiles] = useState<Set<string>>(new Set());
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const [hoveredIndex, setHoveredIndex] = useState<number | null>(null);
  const [showOptions, setShowOptions] = useState(false);
  
  // Search options
  const [caseSensitive, setCaseSensitive] = useState(false);
  const [filePattern, setFilePattern] = useState("");
  const [useRegex, setUseRegex] = useState(false);
  
  const inputRef = useRef<HTMLInputElement>(null);
  const resultsRef = useRef<HTMLDivElement>(null);
  const modalContentRef = useRef<HTMLDivElement>(null);
  const searchTimeoutRef = useRef<NodeJS.Timeout | null>(null);
  
  const currentProject = useProjectStore((state) => state.currentProject);
  
  // Use the global active worktree (from worktreeStore) as the single source of truth
  const effectiveWorktreeId = useActiveWorktreeId();
  
  // Focus input when opened
  useEffect(() => {
    if (isOpen) {
      setTimeout(() => inputRef.current?.focus(), 50);
    } else {
      // Clear state when closed
      setQuery("");
      setResults([]);
      setTotalMatches(0);
      setError(null);
      setExpandedFiles(new Set());
      setHighlightedIndex(0);
      setHoveredIndex(null);
    }
  }, [isOpen]);
  
  // Debounced search
  const performSearch = useCallback(async (searchQuery: string) => {
    if (!searchQuery.trim() || !currentProject) {
      setResults([]);
      setTotalMatches(0);
      return;
    }
    
    setIsLoading(true);
    setError(null);
    
    try {
      const response = await searchFiles(searchQuery, {
        worktreeId: effectiveWorktreeId,
        caseSensitive,
        filePattern: filePattern || undefined,
        maxResults: 100,
        contextLines: 2,
      });
      
      setResults(response.results);
      setTotalMatches(response.totalMatches);
      
      // Auto-expand all results for better visibility
      if (response.results.length > 0) {
        setExpandedFiles(new Set(response.results.map(r => r.path)));
      }
      
      // Reset highlighted index to first item
      setHighlightedIndex(0);
    } catch (err) {
      // Search runs on the machine, so with none connected the raw error is
      // `[internal] unavailable: no daemon connected` — meaningless in a
      // search box. Say what's actually true instead.
      if (isDaemonConnectingError(err)) {
        setError("Your machine is starting — search will work once it's online.");
        return;
      }
      console.error("Search failed:", err);
      setError(err instanceof Error ? err.message : "Search failed");
    } finally {
      setIsLoading(false);
    }
  }, [currentProject, effectiveWorktreeId, caseSensitive, filePattern]);
  
  // Handle search input change with debounce
  const handleQueryChange = (value: string) => {
    setQuery(value);
    
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
  
  // Handle keyboard navigation - works from input or modal
  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      onClose();
      return;
    }
    
    // Only handle arrow keys if there are results
    if (flattenedResults.length === 0) {
      return;
    }
    
    if (e.key === "ArrowDown") {
      e.preventDefault();
      e.stopPropagation();
      setHighlightedIndex((prev) => Math.min(prev + 1, flattenedResults.length - 1));
      setHoveredIndex(null); // Clear hover when using keyboard
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      e.stopPropagation();
      setHighlightedIndex((prev) => Math.max(prev - 1, 0));
      setHoveredIndex(null); // Clear hover when using keyboard
    } else if (e.key === "Enter") {
      e.preventDefault();
      e.stopPropagation();
      const item = flattenedResults[highlightedIndex];
      if (item) {
        handleResultClick(item.path, item.match?.lineNumber, true); // Enter - focus the editor
      }
    }
  };

  // Global Escape key handler - works even when modal loses focus
  useEffect(() => {
    if (!isOpen) return;

    const handleGlobalKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        e.stopPropagation();
        onClose();
      }
    };

    // Use capture phase to catch Escape before other handlers
    document.addEventListener("keydown", handleGlobalKeyDown, true);
    return () => {
      document.removeEventListener("keydown", handleGlobalKeyDown, true);
    };
  }, [isOpen, onClose]);

  // Click outside handler - works when clicking anywhere outside the modal
  useEffect(() => {
    if (!isOpen) return;

    const handleClickOutside = (e: MouseEvent) => {
      const target = e.target as Node;
      // Close if click is outside the modal content (but allow clicking the backdrop)
      if (modalContentRef.current && !modalContentRef.current.contains(target)) {
        onClose();
      }
    };

    // Use a small delay to avoid closing immediately when opening
    const timeoutId = setTimeout(() => {
      document.addEventListener("mousedown", handleClickOutside);
    }, 100);

    return () => {
      clearTimeout(timeoutId);
      document.removeEventListener("mousedown", handleClickOutside);
    };
  }, [isOpen, onClose]);
  
  // Flatten results for keyboard navigation
  const flattenedResults = useMemo(() => {
    const flattened: Array<{ path: string; match?: SearchMatch }> = [];
    results.forEach((result) => {
      flattened.push({ path: result.path });
      if (expandedFiles.has(result.path)) {
        result.matches.forEach((match) => {
          flattened.push({ path: result.path, match });
        });
      }
    });
    return flattened;
  }, [results, expandedFiles]);
  
  // Auto-scroll highlighted item into view
  useEffect(() => {
    if (highlightedIndex >= 0 && resultsRef.current) {
      // Find the highlighted element
      const highlightedItem = flattenedResults[highlightedIndex];
      if (highlightedItem) {
        // Find the corresponding DOM element
        const element = resultsRef.current.querySelector(
          highlightedItem.match
            ? `[data-match-path="${CSS.escape(highlightedItem.path)}"][data-match-line="${highlightedItem.match.lineNumber}"]`
            : `[data-file-path="${CSS.escape(highlightedItem.path)}"]`
        );
        if (element) {
          element.scrollIntoView({ block: "nearest", behavior: "smooth" });
        }
      }
    }
  }, [highlightedIndex, flattenedResults]);
  
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
  const toggleFileExpansion = (path: string) => {
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
    >
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/50" />
      
      {/* Modal */}
      <div 
        ref={modalContentRef}
        className="relative w-full max-w-2xl bg-background border border-border rounded-lg shadow-2xl overflow-hidden"
        onKeyDown={handleKeyDown}
      >
        {/* Search input */}
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border">
          <Search className="w-4 h-4 text-muted-foreground flex-shrink-0" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => handleQueryChange(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Search in files..."
            className="flex-1 bg-transparent text-sm font-mono outline-none placeholder:text-muted-foreground"
          />
          {isLoading && <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />}
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
          <button
            onClick={onClose}
            className="p-1 rounded hover:bg-muted transition-colors"
          >
            <X className="w-4 h-4 text-muted-foreground" />
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
              <label className="flex items-center gap-1.5 cursor-pointer">
                <input
                  type="checkbox"
                  checked={useRegex}
                  onChange={(e) => setUseRegex(e.target.checked)}
                  className="w-3 h-3 rounded border-border"
                />
                <span className="text-muted-foreground">Regex</span>
              </label>
            </div>
            <div className="flex items-center gap-2">
              <label className="text-xs text-muted-foreground whitespace-nowrap">File pattern:</label>
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
        
        {/* Results */}
        <div 
          ref={resultsRef}
          className="max-h-[60vh] overflow-y-auto"
        >
          {error ? (
            <div className="px-4 py-8 text-center">
              <p className="text-sm text-destructive font-mono">{error}</p>
            </div>
          ) : results.length === 0 ? (
            <div className="px-4 py-8 text-center">
              {query ? (
                <p className="text-sm text-muted-foreground font-mono">
                  No results found
                </p>
              ) : (
                <p className="text-sm text-muted-foreground font-mono">
                  Type to search across files in your workspace
                </p>
              )}
            </div>
          ) : (
            <div className="py-1">
              {/* Results summary */}
              <div className="px-4 py-2 text-xs text-muted-foreground border-b border-border">
                {totalMatches} match{totalMatches !== 1 ? "es" : ""} in {results.length} file{results.length !== 1 ? "s" : ""}
              </div>
              
              {/* Result items */}
              {results.map((result, _fileIndex) => {
                const isExpanded = expandedFiles.has(result.path);
                // Find the index of this file header in flattenedResults
                // We know the order matches because flattenedResults is built in the same order as results
                let fileHeaderIndex = -1;
                for (let i = 0; i < flattenedResults.length; i++) {
                  if (flattenedResults[i].path === result.path && !flattenedResults[i].match) {
                    fileHeaderIndex = i;
                    break;
                  }
                }
                const fileIsHighlighted = highlightedIndex === fileHeaderIndex && fileHeaderIndex >= 0;
                const fileIsHovered = hoveredIndex === fileHeaderIndex && fileHeaderIndex >= 0;
                
                return (
                  <div key={result.path} className="border-b border-border/50 last:border-b-0">
                    {/* File header */}
                    <button
                      onClick={() => toggleFileExpansion(result.path)}
                      onMouseEnter={() => {
                        if (fileHeaderIndex >= 0) {
                          setHoveredIndex(fileHeaderIndex);
                          setHighlightedIndex(fileHeaderIndex);
                        }
                      }}
                      onMouseLeave={() => setHoveredIndex(null)}
                      data-file-path={result.path}
                      className={cn(
                        "w-full flex items-center gap-2 px-4 py-2 transition-colors text-left rounded-md mx-2 my-1",
                        (fileIsHighlighted || fileIsHovered) 
                          ? "bg-accent border-2 border-primary text-foreground" 
                          : "hover:bg-muted/50 border-2 border-transparent"
                      )}
                    >
                      {isExpanded ? (
                        <ChevronDown className="w-3 h-3 text-muted-foreground flex-shrink-0" />
                      ) : (
                        <ChevronRight className="w-3 h-3 text-muted-foreground flex-shrink-0" />
                      )}
                      <FileText className="w-4 h-4 text-primary flex-shrink-0" />
                      <span className="text-sm font-mono truncate flex-1">{result.path}</span>
                      <span className="text-xs text-muted-foreground">
                        {result.matches.length} match{result.matches.length !== 1 ? "es" : ""}
                      </span>
                    </button>
                    
                    {/* Matches */}
                    {isExpanded && (
                      <div className="bg-muted/20">
                        {result.matches.map((match, matchIndex) => {
                          // Find the index of this match in flattenedResults
                          const matchIndexInFlattened = flattenedResults.findIndex(
                            item => item.path === result.path && 
                            item.match?.lineNumber === match.lineNumber
                          );
                          const isMatchHighlighted = highlightedIndex === matchIndexInFlattened && matchIndexInFlattened >= 0;
                          const isMatchHovered = hoveredIndex === matchIndexInFlattened && matchIndexInFlattened >= 0;
                          
                          return (
                            <button
                              key={`${result.path}-${match.lineNumber}-${matchIndex}`}
                              onClick={() => handleResultClick(result.path, match.lineNumber, true)} // Click - focus the editor
                              onMouseEnter={() => {
                                if (matchIndexInFlattened >= 0) {
                                  setHoveredIndex(matchIndexInFlattened);
                                  setHighlightedIndex(matchIndexInFlattened);
                                }
                              }}
                              onMouseLeave={() => setHoveredIndex(null)}
                              data-match-path={result.path}
                              data-match-line={match.lineNumber}
                              className={cn(
                                "w-full flex items-start gap-3 px-4 py-1.5 transition-colors text-left pl-12 rounded-md mx-2 my-0.5",
                                (isMatchHighlighted || isMatchHovered) 
                                  ? "bg-accent border-2 border-primary text-foreground" 
                                  : "hover:bg-muted/50 border-2 border-transparent"
                              )}
                            >
                              <span className="text-xs text-muted-foreground font-mono w-8 flex-shrink-0 text-right">
                                {match.lineNumber}
                              </span>
                              <span className="text-xs font-mono truncate flex-1 whitespace-pre">
                                {highlightMatch(match.lineContent, match.matchStart, match.matchEnd)}
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
              <kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">↑↓</kbd> Navigate
            </span>
            <span>
              <kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Enter</kbd> Open
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