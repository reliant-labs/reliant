// QuickFileOpen - Fuzzy file name search (Cmd+P)
import { useState, useRef, useEffect, forwardRef, useImperativeHandle, useCallback } from "react";
import { createPortal } from "react-dom";
import { Search, FileText, Loader2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useProjectStore } from "../../store/projectStore";
import { useActiveWorktreeId } from "../../store/worktreeStore";
import { useViewerStore } from "../../store/viewerStore";
import { getFileTree, FILE_TREE_DEPTH_MAX } from "../../api/fileSystem";
import type { FileNode } from "../FileBrowser";
import { focusChatInput } from "../../hooks/useFocusManager";

export interface QuickFileOpenRef {
  open: () => void;
  close: () => void;
  isOpen: () => boolean;
}

/**
 * The picker asks for the deepest walk the server will give it.
 *
 * A fixed depth is the wrong bound for a fuzzy file picker: a file's usefulness
 * has nothing to do with how many directories it sits under, so any cutoff
 * silently makes deep source files unfindable (this repo's own components live
 * at level 5). The bound that actually matters is the server's, and it is now a
 * real one — gitignore exclusion plus the canonical skip set plus a 50k node
 * budget. In practice a 106k-file Unity project walks to ~8.3k nodes and this
 * repo to ~3.8k, because the gitignored bulk is never entered.
 *
 * This is still a tree walk serving a flat-list need. The picker stays honest
 * about what it did not see (`isTruncated`) until a flat file-list RPC replaces
 * it.
 */
const QUICK_OPEN_TREE_DEPTH = FILE_TREE_DEPTH_MAX;

/**
 * Hard cap on how many candidate files the picker holds in renderer memory.
 *
 * The server already bounds the walk, so this is a second, renderer-side bound
 * for a different reason: every candidate is fuzzy-scored twice on every
 * keystroke with an object allocated per file, and that scan is neither
 * debounced nor virtualized. 20k keeps a keystroke cheap while sitting well
 * above what real projects produce (see the node counts above), so a normal
 * project is never truncated here.
 *
 * Deliberately NOT raised to the server's 50k budget: that would trade a
 * user-visible jank on every keystroke for completeness the flat-list RPC
 * should deliver properly instead.
 */
const QUICK_OPEN_MAX_CANDIDATES = 20000;

/** How many matches the dropdown renders at once. */
const QUICK_OPEN_MAX_RESULTS = 20;

/**
 * Collects files out of a (possibly depth-limited) tree into a flat, bounded
 * list.
 *
 * Iterative on purpose: this walks whatever the API hands back, and a recursive
 * version can blow the stack on a pathologically deep tree. Stops as soon as
 * `limit` files have been collected.
 *
 * `truncated` is derived purely from what we received — no backend flag — and is
 * true when either the cap was hit or the walk ran into a directory the
 * depth-limited request never descended into (`hasChildren` with no `children`).
 */
export function collectFiles(
  nodes: FileNode[],
  limit: number = QUICK_OPEN_MAX_CANDIDATES
): { files: FileNode[]; truncated: boolean } {
  const files: FileNode[] = [];
  let truncated = false;

  // Reverse-push so popping yields the original (already sorted) order.
  const stack: FileNode[] = [];
  for (let i = nodes.length - 1; i >= 0; i--) stack.push(nodes[i]);

  while (stack.length > 0) {
    const node = stack.pop()!;

    if (node.type === "file") {
      if (files.length >= limit) {
        truncated = true;
        break;
      }
      files.push(node);
      continue;
    }

    if (node.children && node.children.length > 0) {
      for (let i = node.children.length - 1; i >= 0; i--) stack.push(node.children[i]);
    } else if (node.hasChildren) {
      // A directory at the depth boundary: its contents are not candidates.
      truncated = true;
    }
  }

  return { files, truncated };
}

interface QuickFileOpenProps {
  isOpen?: boolean;
  onClose?: () => void;
}

export const QuickFileOpen = forwardRef<QuickFileOpenRef, QuickFileOpenProps>(({ isOpen: externalIsOpen, onClose }, ref) => {
  const [internalIsOpen, setInternalIsOpen] = useState(false);
  
  // Use external control if provided, otherwise use internal state
  const isOpen = externalIsOpen ?? internalIsOpen;
  const setIsOpen = useCallback((value: boolean) => {
    setInternalIsOpen(value);
    if (!value && onClose) {
      onClose();
    }
  }, [onClose]);
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<FileNode[]>([]);
  const [highlightedIndex, setHighlightedIndex] = useState(0);
  const [isLoading, setIsLoading] = useState(false);
  const [allFiles, setAllFiles] = useState<FileNode[]>([]);
  // True when the candidate set is only part of the project — either the tree
  // request stopped at QUICK_OPEN_TREE_DEPTH or the candidate cap was hit.
  const [isTruncated, setIsTruncated] = useState(false);

  const inputRef = useRef<HTMLInputElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  const currentProject = useProjectStore((state) => state.currentProject);
  
  // Use the global active worktree (from worktreeStore) as the single source of truth
  const effectiveWorktreeId = useActiveWorktreeId();

  // Load file tree when opened. Bounded on both ends: an explicit depth so the
  // backend never walks the whole project, and a candidate cap so a large
  // response still can't balloon renderer memory.
  const loadFileTree = useCallback(async () => {
    if (!currentProject) return;

    setIsLoading(true);
    try {
      const tree = await getFileTree("/", false, effectiveWorktreeId, QUICK_OPEN_TREE_DEPTH);
      const { files, truncated } = collectFiles(tree, QUICK_OPEN_MAX_CANDIDATES);
      setAllFiles(files);
      setIsTruncated(truncated);
      setResults(files.slice(0, QUICK_OPEN_MAX_RESULTS));
    } catch (error) {
      console.error("Failed to load file tree:", error);
      setIsTruncated(false);
    } finally {
      setIsLoading(false);
    }
  }, [currentProject, effectiveWorktreeId]);

  // Expose methods via ref
  useImperativeHandle(ref, () => ({
    open: () => {
      setIsOpen(true);
      setQuery("");
      setHighlightedIndex(0);
      loadFileTree();
      setTimeout(() => inputRef.current?.focus(), 50);
    },
    close: () => {
      setIsOpen(false);
      setQuery("");
      setResults([]);
    },
    isOpen: () => isOpen,
  }), [isOpen, loadFileTree, setIsOpen]);
  
  // Sync with external isOpen state
  useEffect(() => {
    if (externalIsOpen) {
      setQuery("");
      setHighlightedIndex(0);
      loadFileTree();
      setTimeout(() => inputRef.current?.focus(), 50);
    } else if (externalIsOpen === false) {
      setQuery("");
      setResults([]);
    }
  }, [externalIsOpen, loadFileTree]);

  // Fuzzy match scoring
  const fuzzyMatch = (str: string, query: string): { matches: boolean; score: number } => {
    const lowerStr = str.toLowerCase();
    const lowerQuery = query.toLowerCase();
    
    // Direct substring match gets highest score
    if (lowerStr.includes(lowerQuery)) {
      const index = lowerStr.indexOf(lowerQuery);
      // Prefer matches at start of filename
      return { matches: true, score: 1000 - index };
    }
    
    // Fuzzy match - all chars must appear in order
    let strIndex = 0;
    let queryIndex = 0;
    let score = 0;
    
    while (strIndex < lowerStr.length && queryIndex < lowerQuery.length) {
      if (lowerStr[strIndex] === lowerQuery[queryIndex]) {
        score += 1;
        queryIndex++;
      }
      strIndex++;
    }
    
    if (queryIndex === lowerQuery.length) {
      return { matches: true, score };
    }
    
    return { matches: false, score: 0 };
  };

  // Filter and sort files based on query
  useEffect(() => {
    if (!query.trim()) {
      setResults(allFiles.slice(0, QUICK_OPEN_MAX_RESULTS));
      setHighlightedIndex(0);
      return;
    }

    const scored = allFiles
      .map((file) => {
        const nameMatch = fuzzyMatch(file.name, query);
        const pathMatch = fuzzyMatch(file.path, query);
        
        if (nameMatch.matches || pathMatch.matches) {
          const score = nameMatch.matches ? nameMatch.score + 1000 : pathMatch.score;
          return { file, score };
        }
        return null;
      })
      .filter((item): item is { file: FileNode; score: number } => item !== null)
      .sort((a, b) => b.score - a.score)
      .slice(0, QUICK_OPEN_MAX_RESULTS);

    setResults(scored.map(({ file }) => file));
    setHighlightedIndex(0);
  }, [query, allFiles]);

  // Close on click outside
  useEffect(() => {
    if (!isOpen) return;

    const handleClickOutside = (e: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(e.target as Node)) {
        setIsOpen(false);
        setQuery("");
        focusChatInput();
      }
    };

    setTimeout(() => {
      document.addEventListener("mousedown", handleClickOutside);
    }, 100);

    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
    };
  }, [isOpen, setIsOpen]);

  // Handle keyboard navigation
  const handleKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        setHighlightedIndex((prev) => 
          prev < results.length - 1 ? prev + 1 : 0
        );
        break;
      case "ArrowUp":
        e.preventDefault();
        setHighlightedIndex((prev) => 
          prev > 0 ? prev - 1 : results.length - 1
        );
        break;
      case "Enter":
        e.preventDefault();
        if (results[highlightedIndex]) {
          openFile(results[highlightedIndex], false); // Don't auto-focus on Enter
        }
        break;
      case "Escape":
        e.preventDefault();
        setIsOpen(false);
        setQuery("");
        focusChatInput();
        break;
    }
  };

  // Open file in viewer
  const openFile = (file: FileNode, shouldFocus: boolean = false) => {
    if (currentProject?.id) {
      useViewerStore.getState().openFileViewer(file, currentProject.id, effectiveWorktreeId, shouldFocus);
    }
    setIsOpen(false);
    setQuery("");
  };

  // Auto-scroll highlighted item into view
  useEffect(() => {
    if (highlightedIndex >= 0 && dropdownRef.current) {
      const element = dropdownRef.current.querySelector(`[data-index="${highlightedIndex}"]`);
      element?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  }, [highlightedIndex]);

  if (!isOpen) return null;

  return createPortal(
    <div 
      className="fixed inset-0 z-50 flex items-start justify-center pt-[10vh]"
      data-modal-open="true"
      onClick={(e) => {
        if (e.target === e.currentTarget) {
          setIsOpen(false);
          setQuery("");
          focusChatInput();
        }
      }}
    >
      {/* Backdrop */}
      <div className="absolute inset-0 bg-black/50" />
      
      {/* Modal */}
      <div 
        ref={dropdownRef}
        className="relative w-full max-w-2xl bg-background border border-border rounded-lg shadow-2xl overflow-hidden"
      >
        {/* Search input */}
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border">
          <Search className="w-4 h-4 text-muted-foreground flex-shrink-0" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Search files by name..."
            className="flex-1 bg-transparent text-sm font-mono outline-none placeholder:text-muted-foreground"
            autoFocus
          />
          {isLoading && <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />}
        </div>

        {/* Results */}
        <div className="max-h-[60vh] overflow-y-auto">
          {results.length === 0 ? (
            <div className="px-4 py-8 text-center">
              <p className="text-sm text-muted-foreground font-mono">
                {query ? "No files found" : "Type to search files..."}
              </p>
            </div>
          ) : (
            <div className="py-1">
              {results.map((file, index) => (
                <button
                  key={file.path}
                  data-index={index}
                  onClick={() => openFile(file, false)} // Don't auto-focus on click
                  onMouseEnter={() => setHighlightedIndex(index)}
                  className={cn(
                    "w-full flex items-center gap-3 px-4 py-2 text-left transition-colors",
                    highlightedIndex === index 
                      ? "bg-accent border-2 border-primary rounded-md text-foreground" 
                      : "hover:bg-muted/50 border-2 border-transparent rounded-md"
                  )}
                >
                  <FileText className={cn(
                    "w-4 h-4 flex-shrink-0",
                    highlightedIndex === index ? "text-primary" : "text-muted-foreground"
                  )} />
                  <div className="flex-1 min-w-0">
                    <div className={cn(
                      "text-sm font-mono truncate",
                      highlightedIndex === index ? "text-foreground font-semibold" : ""
                    )}>{file.name}</div>
                    <div className={cn(
                      "text-xs truncate",
                      highlightedIndex === index ? "text-foreground/70" : "text-muted-foreground"
                    )}>{file.path}</div>
                  </div>
                </button>
              ))}
            </div>
          )}

          {/* Partial-index notice. Without this a file that exists but sits
              outside the bounded walk reads as a bug in the picker. */}
          {isTruncated && !isLoading && (
            <div className="px-4 py-2 border-t border-border/50">
              <p className="text-xs text-muted-foreground font-mono" data-testid="quick-open-truncated">
                Searching {allFiles.length.toLocaleString()} files — this project is large enough that some are not listed.
              </p>
            </div>
          )}
        </div>

        {/* Footer */}
        <div className="px-4 py-2 border-t border-border bg-muted/30 flex items-center gap-3 text-xs text-muted-foreground">
          <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">↑↓</kbd> Navigate</span>
          <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Enter</kbd> Open</span>
          <span><kbd className="px-1 py-0.5 bg-muted rounded border border-border/50">Esc</kbd> Close</span>
        </div>
      </div>
    </div>,
    document.body
  );
});

QuickFileOpen.displayName = "QuickFileOpen";