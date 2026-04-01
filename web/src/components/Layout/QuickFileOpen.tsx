// QuickFileOpen - Fuzzy file name search (Cmd+P)
import { useState, useRef, useEffect, forwardRef, useImperativeHandle, useCallback } from "react";
import { createPortal } from "react-dom";
import { Search, FileText, Loader2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useViewerStore } from "../../store/viewerStore";
import { useChatStore } from "../../store/chatStore";
import { getFileTree } from "../../api/fileSystem";
import type { FileNode } from "../FileBrowser";
import { focusChatInput } from "../../hooks/useFocusManager";

export interface QuickFileOpenRef {
  open: () => void;
  close: () => void;
  isOpen: () => boolean;
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

  const inputRef = useRef<HTMLInputElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  const currentProject = useProjectStore((state) => state.currentProject);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  
  // Get worktree from active chat, falling back to main worktree
  const activeChatId = useChatStore((state) => state.activeChatId);
  const activeChat = useChatStore((state) => activeChatId ? state.chats.get(activeChatId) : undefined);
  const mainWorktree = worktrees.find((w) => w.is_main && w.project_id === currentProject?.id);
  const effectiveWorktreeId = activeChat?.worktreeId || mainWorktree?.id;

  // Flatten file tree helper (stable - no deps)
  const flattenFileTree = useCallback((nodes: FileNode[], accumulator: FileNode[] = []): FileNode[] => {
    for (const node of nodes) {
      if (node.type === "file") {
        accumulator.push(node);
      }
      if (node.children) {
        flattenFileTree(node.children, accumulator);
      }
    }
    return accumulator;
  }, []);

  // Load file tree when opened
  const loadFileTree = useCallback(async () => {
    if (!currentProject) return;
    
    setIsLoading(true);
    try {
      const tree = await getFileTree("/", false, effectiveWorktreeId);
      const files = flattenFileTree(tree);
      setAllFiles(files);
      setResults(files.slice(0, 20)); // Show first 20 by default
    } catch (error) {
      console.error("Failed to load file tree:", error);
    } finally {
      setIsLoading(false);
    }
  }, [currentProject, effectiveWorktreeId, flattenFileTree]);

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
      setResults(allFiles.slice(0, 20));
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
      .slice(0, 20);

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
