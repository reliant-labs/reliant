import { useEffect, useState, useRef, useCallback, forwardRef, useImperativeHandle } from "react";
import { FileTreeItem } from "./FileTreeItem";
import type { FileOperationType } from "./FileTreeItem";
import { FileOperationsModal } from "./FileOperationsModal";
import { Loader2, AlertCircle, FilePlus, FolderPlus } from "lucide-react";
import type { FileNode } from "./index";
import { getFileTree, createFile, createFolder, deleteFileOrFolder, copyFile, getFileContent, getFilePreviewInfo } from "../../api/fileSystem";
import { cn } from "../../lib/utils";
import {
  isDaemonConnectingError,
  DAEMON_CONNECT_TIMEOUT_MS,
} from "../../lib/daemon-errors";
import { DaemonConnectingState } from "../DaemonConnectingState";
import { useProjectStore } from "../../store/projectStore";
import { useViewerStore } from "../../store/viewerStore";
import { useFileDeletionStore } from "../../store/fileDeletionStore";
import { toast } from "../../lib/toast-manager";

interface FileTreeProps {
  searchQuery: string;
  onFileSelect: (file: FileNode) => void;
  onPathChange: (path: string) => void;
  selectedFile: FileNode | null;
  showHidden: boolean;
  onRefresh: () => void;
  collapseKey: number;
  creatingType?: "file" | "folder" | null;
  onCreatingComplete?: () => void;
  worktreeId?: string;
  // Controlled state for persistence across tab switches
  focusedPath?: string | null;
  onFocusedPathChange?: (path: string | null) => void;
  expandedPaths?: Set<string>;
  onExpandedPathsChange?: (paths: Set<string>) => void;
  // Auto-focus when the tree loads
  autoFocus?: boolean;
}

export interface FileTreeHandle {
  focus: () => void;
  syncToActiveFile: () => void;
  refresh: () => Promise<void>;
}

// Helper to sort nodes: directories first (alphabetically), then files (alphabetically)
function sortFileNodes(nodes: FileNode[]): FileNode[] {
  return [...nodes].sort((a, b) => {
    if (a.type === "directory" && b.type === "file") return -1;
    if (a.type === "file" && b.type === "directory") return 1;
    return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
  });
}

// Build a flat list of visible paths for keyboard navigation
function buildVisiblePaths(
  nodes: FileNode[],
  expandedPaths: Set<string>,
  searchQuery: string
): string[] {
  const result: string[] = [];
  
  function traverse(nodeList: FileNode[]) {
    const sorted = sortFileNodes(nodeList);
    for (const node of sorted) {
      // Skip nodes that don't match search
      if (searchQuery && !nodeMatchesSearch(node, searchQuery)) {
        continue;
      }
      
      result.push(node.path);
      
      if (node.type === "directory" && node.children) {
        // Check if expanded or auto-expanded due to search
        const shouldAutoExpand = searchQuery && node.children.some(c => nodeMatchesSearch(c, searchQuery));
        if (expandedPaths.has(node.path) || shouldAutoExpand) {
          traverse(node.children);
        }
      }
    }
  }
  
  traverse(nodes);
  return result;
}

// Check if node matches search query
function nodeMatchesSearch(node: FileNode, query: string): boolean {
  if (!query) return true;
  const lowerQuery = query.toLowerCase();
  if (node.name.toLowerCase().includes(lowerQuery) || node.path.toLowerCase().includes(lowerQuery)) {
    return true;
  }
  if (node.children) {
    return node.children.some(child => nodeMatchesSearch(child, query));
  }
  return false;
}

// Find a node by path in the tree
function findNodeByPath(nodes: FileNode[], path: string): FileNode | null {
  for (const node of nodes) {
    if (node.path === path) return node;
    if (node.children) {
      const found = findNodeByPath(node.children, path);
      if (found) return found;
    }
  }
  return null;
}

// reconcileChildren merges a freshly-fetched child list against the previous one
// so that still-expanded subdirectories keep their already-loaded grandchildren
// (avoids a collapse-flash while those subtrees refetch). A directory that is no
// longer expanded drops back to "not loaded" (undefined) so re-expanding it
// fetches fresh data instead of showing a stale subtree.
function reconcileChildren(
  next: FileNode[],
  prev: FileNode[] | undefined,
  expanded: Set<string>
): FileNode[] {
  if (!prev || prev.length === 0) return next;
  const prevByPath = new Map(prev.map((c) => [c.path, c] as const));
  return next.map((child) => {
    if (child.type === "directory" && child.children === undefined && expanded.has(child.path)) {
      const old = prevByPath.get(child.path);
      if (old && old.children !== undefined) {
        return { ...child, children: old.children };
      }
    }
    return child;
  });
}

// setChildrenAtPath immutably replaces the children of the node at `path` (or the
// whole tree when `path` is the root), reconciling against the existing subtree.
function setChildrenAtPath(
  tree: FileNode[],
  path: string,
  newChildren: FileNode[],
  expanded: Set<string>
): FileNode[] {
  if (path === "/" || path === "") {
    return reconcileChildren(newChildren, tree, expanded);
  }
  return tree.map((node) => {
    if (node.path === path) {
      return { ...node, children: reconcileChildren(newChildren, node.children, expanded) };
    }
    // Only descend into the subtree that contains `path`.
    if (node.children && node.children.length > 0 && path.startsWith(node.path + "/")) {
      return { ...node, children: setChildrenAtPath(node.children, path, newChildren, expanded) };
    }
    return node;
  });
}

// Get parent path from a path
function getParentPath(path: string): string | null {
  const lastSlash = path.lastIndexOf('/');
  if (lastSlash <= 0) return null;
  return path.substring(0, lastSlash);
}

// Expand all parent directories of a path
function expandPathToNode(path: string, currentExpanded: Set<string>): Set<string> {
  const newExpanded = new Set(currentExpanded);
  let parentPath = getParentPath(path);
  while (parentPath) {
    newExpanded.add(parentPath);
    parentPath = getParentPath(parentPath);
  }
  return newExpanded;
}

export const FileTree = forwardRef<FileTreeHandle, FileTreeProps>(function FileTree({
  searchQuery,
  onFileSelect,
  onPathChange,
  selectedFile,
  showHidden,
  onRefresh,
  collapseKey,
  creatingType = null,
  onCreatingComplete,
  worktreeId,
  focusedPath: controlledFocusedPath,
  onFocusedPathChange,
  expandedPaths: controlledExpandedPaths,
  onExpandedPathsChange,
  autoFocus = false,
}, ref) {
  const [tree, setTree] = useState<FileNode[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  // Transient "cloud daemon still connecting" state: show a spinner + auto-retry
  // instead of a red error while the daemon comes online (up to 60s).
  const [connecting, setConnecting] = useState(false);
  const connectingSinceRef = useRef<number | null>(null);
  // Lazy directory loading: which directories are currently fetching children
  // (for per-node spinners), and an in-flight guard to dedupe concurrent loads.
  const [loadingPaths, setLoadingPaths] = useState<Set<string>>(new Set());
  const inflightRef = useRef<Set<string>>(new Set());
  // Mirror of expandedPaths readable from callbacks/refresh without stale closures.
  const expandedPathsRef = useRef<Set<string>>(new Set());
  const [newItemName, setNewItemName] = useState("");
  const [isCreating, setIsCreating] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const containerRef = useRef<HTMLDivElement>(null);
  const currentProject = useProjectStore((state) => state.currentProject);
  const openFileViewer = useViewerStore((state) => state.openFileViewer);
  const getActiveViewer = useViewerStore((state) => state.getActiveViewer);
  const addDeletedFile = useFileDeletionStore((state) => state.addDeletedFile);

  // Hoisted FileOperationsModal state
  const [modalState, setModalState] = useState<{
    isOpen: boolean;
    operation: FileOperationType | null;
    node: FileNode | null;
  }>({ isOpen: false, operation: null, node: null });

  // Internal state (used when not controlled)
  const [internalFocusedPath, setInternalFocusedPath] = useState<string | null>(null);
  const [internalExpandedPaths, setInternalExpandedPaths] = useState<Set<string>>(new Set());

  // Use controlled or internal state
  const focusedPath = controlledFocusedPath !== undefined ? controlledFocusedPath : internalFocusedPath;
  const expandedPaths = controlledExpandedPaths !== undefined ? controlledExpandedPaths : internalExpandedPaths;
  // Keep a ref mirror so loadChildren/refresh read the latest expansion without
  // being re-created on every expand.
  expandedPathsRef.current = expandedPaths;

  const setFocusedPath = useCallback((path: string | null) => {
    if (onFocusedPathChange) {
      onFocusedPathChange(path);
    } else {
      setInternalFocusedPath(path);
    }
  }, [onFocusedPathChange]);

  const setExpandedPaths = useCallback((paths: Set<string>) => {
    if (onExpandedPathsChange) {
      onExpandedPathsChange(paths);
    } else {
      setInternalExpandedPaths(paths);
    }
  }, [onExpandedPathsChange]);

  // Fetch a single directory's immediate children (depth-limited) and merge them
  // into the tree. Used for lazy expansion (depth 1) and, from refresh, to
  // re-read the currently-visible directories. Deduped per-path via inflightRef.
  const loadChildren = useCallback(async (path: string, depth: number) => {
    if (inflightRef.current.has(path)) return;
    inflightRef.current.add(path);
    setLoadingPaths((prev) => {
      const next = new Set(prev);
      next.add(path);
      return next;
    });
    try {
      const children = await getFileTree(path, showHidden, worktreeId, depth);
      setTree((prev) => setChildrenAtPath(prev, path, children, expandedPathsRef.current));
    } catch (err) {
      // A directory removed between expand and fetch: its parent refetch already
      // drops it from the tree, so swallow the error here.
      console.debug("[FileTree] failed to load children for", path, err);
    } finally {
      inflightRef.current.delete(path);
      setLoadingPaths((prev) => {
        const next = new Set(prev);
        next.delete(path);
        return next;
      });
    }
  }, [showHidden, worktreeId]);

  // Define loadFileTree early so it can be used in useImperativeHandle
  const loadFileTree = useCallback(async (showLoading = true) => {
    if (showLoading) {
      setLoading(true);
    }
    setError(null);

    try {
      // Initial/root load fetches two levels (VS Code style): the top level plus
      // one level of preloaded children so the first expand is instant. Deeper
      // directories load lazily on expand. Reconcile against the existing tree so
      // a refresh doesn't collapse already-expanded subtrees.
      const rootChildren = await getFileTree("/", showHidden, worktreeId, 2);
      setTree((prev) => reconcileChildren(rootChildren, prev, expandedPathsRef.current));
      setConnecting(false);
      connectingSinceRef.current = null;
    } catch (err) {
      if (isDaemonConnectingError(err)) {
        // Cloud daemon is still coming online — show the connecting state and
        // keep auto-retrying, but give up after the provisioning window so we
        // don't spin forever on a genuinely stuck daemon.
        const since = connectingSinceRef.current ?? Date.now();
        connectingSinceRef.current = since;
        if (Date.now() - since >= DAEMON_CONNECT_TIMEOUT_MS) {
          setConnecting(false);
          connectingSinceRef.current = null;
          setError("Couldn't connect to your environment. Please try again.");
        } else {
          setConnecting(true);
        }
      } else {
        setConnecting(false);
        connectingSinceRef.current = null;
        setError(err instanceof Error ? err.message : "Failed to load file tree");
      }
    } finally {
      if (showLoading) {
        setLoading(false);
      }
    }
  }, [showHidden, worktreeId]);

  // While connecting, silently retry every 2s so the tree loads automatically
  // once the daemon flips to ACTIVE. The 60s cap lives in loadFileTree.
  useEffect(() => {
    if (!connecting) return;
    const id = setInterval(() => {
      void loadFileTree(false);
    }, 2_000);
    return () => clearInterval(id);
  }, [connecting, loadFileTree]);

  // Lazily fetch children for any expanded directory that hasn't loaded them yet
  // (children === undefined). Covers every expansion path — click, keyboard,
  // context menu, restored-across-tab-switch — with one uniform trigger.
  useEffect(() => {
    for (const path of expandedPaths) {
      const node = findNodeByPath(tree, path);
      if (
        node &&
        node.type === "directory" &&
        node.children === undefined &&
        (node.hasChildren ?? true) &&
        !inflightRef.current.has(path)
      ) {
        void loadChildren(path, 1);
      }
    }
  }, [expandedPaths, tree, loadChildren]);

  // Refresh only the currently-visible directories (root + expanded), not the
  // whole recursive tree. Each directory is refetched so files removed on disk
  // disappear; expanded subtrees are preserved (and themselves refreshed) so the
  // tree doesn't collapse under the user.
  const refresh = useCallback(async () => {
    await loadChildren("/", 2);
    const expanded = Array.from(expandedPathsRef.current);
    await Promise.all(expanded.map((p) => loadChildren(p, 1)));
  }, [loadChildren]);

  // Build visible paths for navigation
  const visiblePaths = buildVisiblePaths(tree, expandedPaths, searchQuery);

  // Sync focus to currently active file viewer
  const syncToActiveFile = useCallback(() => {
    const activeViewer = getActiveViewer();
    if (activeViewer?.type === "file") {
      const filePath = activeViewer.file.path;
      // Expand path to make sure file is visible
      const newExpanded = expandPathToNode(filePath, expandedPaths);
      setExpandedPaths(newExpanded);
      setFocusedPath(filePath);
      return true;
    }
    return false;
  }, [getActiveViewer, expandedPaths, setExpandedPaths, setFocusedPath]);

  // Helper to set focus and scroll into view (only for keyboard nav)
  const setFocusedPathAndScroll = useCallback((path: string) => {
    setFocusedPath(path);
    // Scroll after React renders the focused item
    setTimeout(() => {
      const element = containerRef.current?.querySelector(`[data-path="${CSS.escape(path)}"]`);
      element?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }, 0);
  }, [setFocusedPath]);

  // Expose methods via ref
  useImperativeHandle(ref, () => ({
    refresh: async () => {
      // Silent refresh — refetch only the visible/expanded directories.
      await refresh();
    },
    focus: () => {
      containerRef.current?.focus();
    },
    syncToActiveFile,
  }), [syncToActiveFile, refresh]);

  // Handle PageUp/PageDown globally when file tree is visible
  // This catches Fn+Up/Down even when container doesn't have focus
  useEffect(() => {
    const handleGlobalPageKeys = (e: KeyboardEvent) => {
      // Only handle if file tree container exists and we're not in an input/textarea
      if (!containerRef.current) return;
      const target = e.target as HTMLElement;
      if (target instanceof HTMLInputElement || 
          target instanceof HTMLTextAreaElement ||
          target.closest('.monaco-editor') !== null) {
        return;
      }
      
      const isPageUp = e.key === "PageUp" || e.keyCode === 33;
      const isPageDown = e.key === "PageDown" || e.keyCode === 34;
      
      if (isPageUp || isPageDown) {
        const container = containerRef.current;
        // Check if container is visible (not hidden by CSS)
        const rect = container.getBoundingClientRect();
        if (rect.width === 0 || rect.height === 0) return;
        
        // Find the scrollable parent (the one with overflow-y-auto)
        const scrollableParent = container.closest('.overflow-y-auto') as HTMLElement;
        if (!scrollableParent) return;
        
        e.preventDefault();
        e.stopPropagation();
        
        // Ensure container has focus
        container.focus();
        
        const viewportHeight = scrollableParent.clientHeight;
        const currentScrollTop = scrollableParent.scrollTop;
        
        if (isPageDown) {
          const maxScrollTop = scrollableParent.scrollHeight - viewportHeight;
          const newScrollTop = Math.min(maxScrollTop, currentScrollTop + viewportHeight);
          
          // Only scroll if we can actually scroll
          if (maxScrollTop > 0 && newScrollTop > currentScrollTop) {
            scrollableParent.scrollTop = newScrollTop;
          } else {
            return;
          }
        } else {
          const newScrollTop = Math.max(0, currentScrollTop - viewportHeight);
          
          // Only scroll if we can actually scroll
          if (currentScrollTop > 0 && newScrollTop < currentScrollTop) {
            scrollableParent.scrollTop = newScrollTop;
          } else {
            return;
          }
        }
        
        // Find the item that's now visible at the top of the viewport after scrolling
        setTimeout(() => {
          const elements = Array.from(container.querySelectorAll('[data-path]'));
          const containerRect = scrollableParent.getBoundingClientRect();
          
          // Find all elements that are visible in the viewport and calculate their Y position
          const visibleElements = elements
            .map(el => {
              const elRect = el.getBoundingClientRect();
              if (elRect.bottom < containerRect.top || elRect.top > containerRect.bottom) {
                return null;
              }
              const topY = elRect.top - containerRect.top;
              const bottomY = elRect.bottom - containerRect.top;
              return { el, topY, bottomY };
            })
            .filter((item): item is { el: Element; topY: number; bottomY: number } => item !== null);
          
          if (visibleElements.length === 0) return;
          
          // For PageDown, find element toward the bottom of the viewport
          const viewportHeight = scrollableParent.clientHeight;
          const targetY = viewportHeight * 0.75; // Target 75% down the viewport
          
          // Find the element closest to the target position (toward bottom)
          visibleElements.sort((a, b) => {
            const aDistance = Math.abs(a.topY - targetY);
            const bDistance = Math.abs(b.topY - targetY);
            return aDistance - bDistance;
          });
          
          // Get the element closest to the target position
          const targetElement = visibleElements[0]?.el;
          if (targetElement) {
            const path = targetElement.getAttribute('data-path');
            if (path) {
              setFocusedPath(path);
            }
          }
        }, 10);
      }
    };
    
    // Use capture phase to catch before other handlers
    window.addEventListener('keydown', handleGlobalPageKeys, true);
    return () => {
      window.removeEventListener('keydown', handleGlobalPageKeys, true);
    };
  }, [visiblePaths, setFocusedPath]);

  // Handle keyboard navigation from file viewer (arrow keys when Monaco is focused)
  useEffect(() => {
    const handleKeyboardNavigate = (event: CustomEvent<{ direction: 'up' | 'down'; currentPath: string }>) => {
      const { direction, currentPath } = event.detail;
      
      // Find current index in visible paths
      const currentIndex = currentPath ? visiblePaths.indexOf(currentPath) : -1;
      
      if (currentIndex === -1) {
        // If current path not found, try to find the file in the tree
        const fileNode = findNodeByPath(tree, currentPath);
        if (fileNode) {
          // Expand parents and set focus
          const newExpanded = new Set(expandedPaths);
          let parentPath = currentPath.substring(0, currentPath.lastIndexOf('/'));
          while (parentPath) {
            newExpanded.add(parentPath);
            const lastSlash = parentPath.lastIndexOf('/');
            parentPath = lastSlash > 0 ? parentPath.substring(0, lastSlash) : '';
          }
          setExpandedPaths(newExpanded);
          // Rebuild visible paths and find new index
          const newVisiblePaths = buildVisiblePaths(tree, newExpanded, searchQuery);
          const newIndex = newVisiblePaths.indexOf(currentPath);
          if (newIndex !== -1) {
            const targetIndex = direction === 'up' ? Math.max(0, newIndex - 1) : Math.min(newVisiblePaths.length - 1, newIndex + 1);
            const targetPath = newVisiblePaths[targetIndex];
            setFocusedPath(targetPath);
            // Scroll after React renders
            setTimeout(() => {
              const element = containerRef.current?.querySelector(`[data-path="${CSS.escape(targetPath)}"]`);
              element?.scrollIntoView({ block: "nearest", behavior: "smooth" });
            }, 0);
          }
        }
        return;
      }

      // Navigate up or down
      if (direction === 'up' && currentIndex > 0) {
        const targetPath = visiblePaths[currentIndex - 1];
        setFocusedPath(targetPath);
        setTimeout(() => {
          const element = containerRef.current?.querySelector(`[data-path="${CSS.escape(targetPath)}"]`);
          element?.scrollIntoView({ block: "nearest", behavior: "smooth" });
        }, 0);
      } else if (direction === 'down' && currentIndex < visiblePaths.length - 1) {
        const targetPath = visiblePaths[currentIndex + 1];
        setFocusedPath(targetPath);
        setTimeout(() => {
          const element = containerRef.current?.querySelector(`[data-path="${CSS.escape(targetPath)}"]`);
          element?.scrollIntoView({ block: "nearest", behavior: "smooth" });
        }, 0);
      }
    };

    window.addEventListener('file-tree-keyboard-navigate', handleKeyboardNavigate as EventListener);
    return () => {
      window.removeEventListener('file-tree-keyboard-navigate', handleKeyboardNavigate as EventListener);
    };
  }, [visiblePaths, tree, expandedPaths, searchQuery, setExpandedPaths, setFocusedPath]);

  useEffect(() => {
    loadFileTree();
  }, [showHidden, worktreeId, loadFileTree]);

  // Auto-focus and sync to active file when tree loads
  useEffect(() => {
    if (!loading && tree.length > 0 && autoFocus) {
      // Try to sync to active file first
      const synced = syncToActiveFile();
      if (!synced && visiblePaths.length > 0 && !focusedPath) {
        // If no active file, focus first item
        setFocusedPath(visiblePaths[0]);
      }
      // Focus the container
      setTimeout(() => containerRef.current?.focus(), 0);
    }
  }, [loading, tree.length, autoFocus, focusedPath, setFocusedPath, syncToActiveFile, visiblePaths]);

  // Focus input when creating mode starts
  useEffect(() => {
    if (creatingType && !isCreating) {
      setIsCreating(true);
      setNewItemName("");
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [creatingType, isCreating]);

  // Handle collapse all
  useEffect(() => {
    if (collapseKey > 0) {
      setExpandedPaths(new Set());
    }
  }, [collapseKey, setExpandedPaths]);

  const handleCreateSubmit = async () => {
    if (!newItemName.trim() || !creatingType) return;

    try {
      const fullPath = `/${newItemName.trim()}`;
      const fileName = newItemName.trim();
      
      if (creatingType === "file") {
        await createFile(fullPath, "", worktreeId);
        
        if (currentProject?.id) {
          const newFileNode: FileNode = {
            name: fileName,
            path: fullPath,
            type: "file",
          };
          openFileViewer(newFileNode, currentProject.id, undefined, true); // New file - auto-focus
        }
      } else {
        await createFolder(fullPath, worktreeId);
      }
      
      setNewItemName("");
      setIsCreating(false);
      onCreatingComplete?.();
      
      await loadFileTree();
    } catch (error) {
      console.error("Failed to create:", error);
      alert(`Failed to create: ${error instanceof Error ? error.message : "Unknown error"}`);
    }
  };

  const handleCreateCancel = () => {
    setNewItemName("");
    setIsCreating(false);
    onCreatingComplete?.();
  };

  const handleInputKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      handleCreateSubmit();
    } else if (e.key === "Escape") {
      e.preventDefault();
      handleCreateCancel();
    }
  };

  // Handle keyboard navigation
  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    // Don't handle if target is the input
    if (e.target instanceof HTMLInputElement) return;

    const currentIndex = focusedPath ? visiblePaths.indexOf(focusedPath) : -1;
    const currentNode = focusedPath ? findNodeByPath(tree, focusedPath) : null;
    const isCmd = e.metaKey || e.ctrlKey;
    
    // On Mac, Fn+Up/Down maps to PageUp/PageDown keys
    const isPageUp = e.key === "PageUp" || 
                     e.keyCode === 33 || 
                     (e.key === "ArrowUp" && e.getModifierState("Fn"));
    const isPageDown = e.key === "PageDown" || 
                       e.keyCode === 34 || 
                       (e.key === "ArrowDown" && e.getModifierState("Fn"));

    // Handle PageUp/PageDown (Fn+Up/Down) - scroll by viewport height
    if (isPageDown) {
      e.preventDefault();
      e.stopPropagation();
      if (containerRef.current) {
        const container = containerRef.current;
        // Find the scrollable parent (the one with overflow-y-auto)
        const scrollableParent = container.closest('.overflow-y-auto') as HTMLElement;
        if (!scrollableParent) return;
        
        const viewportHeight = scrollableParent.clientHeight;
        const currentScrollTop = scrollableParent.scrollTop;
        const maxScrollTop = scrollableParent.scrollHeight - viewportHeight;
        const newScrollTop = Math.min(maxScrollTop, currentScrollTop + viewportHeight);
        
        // Only scroll if we can actually scroll
        if (maxScrollTop > 0 && newScrollTop > currentScrollTop) {
          scrollableParent.scrollTop = newScrollTop;
          
          // Find the item that's now visible toward the bottom of the viewport after scrolling
          setTimeout(() => {
            const elements = Array.from(container.querySelectorAll('[data-path]'));
            const containerRect = scrollableParent.getBoundingClientRect();
            
            // Find all elements that are visible in the viewport and calculate their Y position
            const visibleElements = elements
              .map(el => {
                const elRect = el.getBoundingClientRect();
                if (elRect.bottom < containerRect.top || elRect.top > containerRect.bottom) {
                  return null;
                }
                const topY = elRect.top - containerRect.top;
                const bottomY = elRect.bottom - containerRect.top;
                return { el, topY, bottomY };
              })
              .filter((item): item is { el: Element; topY: number; bottomY: number } => item !== null);
            
            if (visibleElements.length === 0) return;
            
            // For PageDown, find element toward the bottom of the viewport
            const targetY = viewportHeight * 0.75; // Target 75% down the viewport
            
            // Find the element closest to the target position (toward bottom)
            visibleElements.sort((a, b) => {
              const aDistance = Math.abs(a.topY - targetY);
              const bDistance = Math.abs(b.topY - targetY);
              return aDistance - bDistance;
            });
            
            // Get the element closest to the target position
            const targetElement = visibleElements[0]?.el;
            if (targetElement) {
              const path = targetElement.getAttribute('data-path');
              if (path) {
                setFocusedPath(path);
              }
            }
          }, 10);
        }
      }
      return;
    }
    
    if (isPageUp) {
      e.preventDefault();
      e.stopPropagation();
      if (containerRef.current) {
        const container = containerRef.current;
        // Find the scrollable parent (the one with overflow-y-auto)
        const scrollableParent = container.closest('.overflow-y-auto') as HTMLElement;
        if (!scrollableParent) return;
        
        const viewportHeight = scrollableParent.clientHeight;
        const currentScrollTop = scrollableParent.scrollTop;
        const newScrollTop = Math.max(0, currentScrollTop - viewportHeight);
        
        // Only scroll if we can actually scroll
        if (currentScrollTop > 0 && newScrollTop < currentScrollTop) {
          scrollableParent.scrollTop = newScrollTop;
          
          // Find the item that's now visible at the top of the viewport after scrolling
          setTimeout(() => {
            const elements = Array.from(container.querySelectorAll('[data-path]'));
            const containerRect = scrollableParent.getBoundingClientRect();
            
            // Find all elements that are visible in the viewport and calculate their Y position
            const visibleElements = elements
              .map(el => {
                const elRect = el.getBoundingClientRect();
                if (elRect.bottom < containerRect.top || elRect.top > containerRect.bottom) {
                  return null;
                }
                const topY = elRect.top - containerRect.top;
                return { el, topY };
              })
              .filter((item): item is { el: Element; topY: number } => item !== null);
            
            if (visibleElements.length === 0) return;
            
            // Sort by Y position (smallest = closest to top)
            visibleElements.sort((a, b) => a.topY - b.topY);
            
            // Get the element closest to the top
            const targetElement = visibleElements[0]?.el;
            if (targetElement) {
              const path = targetElement.getAttribute('data-path');
              if (path) {
                setFocusedPath(path);
              }
            }
          }, 10);
        }
      }
      return;
    }

    switch (e.key) {
      case "ArrowDown": {
        e.preventDefault();
        if (isCmd) {
          // Cmd+Down: Jump to bottom
          if (visiblePaths.length > 0) {
            setFocusedPathAndScroll(visiblePaths[visiblePaths.length - 1]);
          }
        } else {
          // Regular Down: Move to next item
          if (currentIndex < visiblePaths.length - 1) {
            setFocusedPathAndScroll(visiblePaths[currentIndex + 1]);
          }
        }
        break;
      }
      case "ArrowUp": {
        e.preventDefault();
        if (isCmd) {
          // Cmd+Up: Jump to top
          if (visiblePaths.length > 0) {
            setFocusedPathAndScroll(visiblePaths[0]);
          }
        } else {
          // Regular Up: Move to previous item
          if (currentIndex > 0) {
            setFocusedPathAndScroll(visiblePaths[currentIndex - 1]);
          }
        }
        break;
      }
      case "ArrowRight": {
        e.preventDefault();
        if (currentNode?.type === "directory") {
          if (!expandedPaths.has(currentNode.path)) {
            // Expand the directory
            const newExpanded = new Set(expandedPaths);
            newExpanded.add(currentNode.path);
            setExpandedPaths(newExpanded);
            // Scroll into view when expanding
            setTimeout(() => {
              const element = containerRef.current?.querySelector(`[data-path="${CSS.escape(currentNode.path)}"]`);
              if (element) {
                const rect = element.getBoundingClientRect();
                const containerRect = containerRef.current?.getBoundingClientRect();
                if (containerRect) {
                  // Check if element is out of view
                  if (rect.top < containerRect.top || rect.bottom > containerRect.bottom) {
                    element.scrollIntoView({ block: "nearest", behavior: "smooth" });
                  }
                }
              }
            }, 0);
          } else if (currentNode.children?.length) {
            // Already expanded, move to first child
            const nextIndex = currentIndex + 1;
            if (nextIndex < visiblePaths.length) {
              setFocusedPathAndScroll(visiblePaths[nextIndex]);
            }
          }
        }
        break;
      }
      case "ArrowLeft": {
        e.preventDefault();
        if (currentNode?.type === "directory" && expandedPaths.has(currentNode.path)) {
          // Collapse the directory
          const newExpanded = new Set(expandedPaths);
          newExpanded.delete(currentNode.path);
          setExpandedPaths(newExpanded);
        } else {
          // Move to parent directory
          const parentPath = getParentPath(focusedPath || "");
          if (parentPath && visiblePaths.includes(parentPath)) {
            setFocusedPathAndScroll(parentPath);
          }
        }
        break;
      }
      case "Enter":
      case " ": {
        e.preventDefault();
        if (currentNode) {
          if (currentNode.type === "directory") {
            // Toggle expand/collapse
            const newExpanded = new Set(expandedPaths);
            const wasExpanded = expandedPaths.has(currentNode.path);
            if (wasExpanded) {
              newExpanded.delete(currentNode.path);
            } else {
              newExpanded.add(currentNode.path);
              // Scroll into view when expanding
              setTimeout(() => {
                const element = containerRef.current?.querySelector(`[data-path="${CSS.escape(currentNode.path)}"]`);
                if (element) {
                  const rect = element.getBoundingClientRect();
                  const containerRect = containerRef.current?.getBoundingClientRect();
                  if (containerRect) {
                    // Check if element is out of view
                    if (rect.top < containerRect.top || rect.bottom > containerRect.bottom) {
                      element.scrollIntoView({ block: "nearest", behavior: "smooth" });
                    }
                  }
                }
              }, 0);
            }
            setExpandedPaths(newExpanded);
          } else {
            // Open the file (Enter key - don't auto-focus)
            onFileSelect(currentNode);
          }
        }
        break;
      }
      case "Tab": {
        // Let tab navigation work naturally - search input is the only other focusable element
        break;
      }
    }
  }, [focusedPath, visiblePaths, tree, expandedPaths, setFocusedPath, setExpandedPaths, onFileSelect, setFocusedPathAndScroll]);

  // Handle expand/collapse from FileTreeItem
  const handleExpand = useCallback((path: string) => {
    const newExpanded = new Set(expandedPaths);
    newExpanded.add(path);
    setExpandedPaths(newExpanded);
    onPathChange(path);
    
    // Scroll the expanded directory into view if it's out of frame
    setTimeout(() => {
      const element = containerRef.current?.querySelector(`[data-path="${CSS.escape(path)}"]`);
      if (element) {
        const rect = element.getBoundingClientRect();
        const containerRect = containerRef.current?.getBoundingClientRect();
        if (containerRect) {
          // Check if element is out of view
          if (rect.top < containerRect.top || rect.bottom > containerRect.bottom) {
            element.scrollIntoView({ block: "nearest", behavior: "smooth" });
          }
        }
      }
    }, 0);
  }, [expandedPaths, setExpandedPaths, onPathChange]);

  const handleCollapse = useCallback((path: string) => {
    const newExpanded = new Set(expandedPaths);
    newExpanded.delete(path);
    setExpandedPaths(newExpanded);
  }, [expandedPaths, setExpandedPaths]);

  // Handle item click - also sets focus
  const handleItemClick = useCallback((node: FileNode) => {
    setFocusedPath(node.path);
    if (node.type === "file") {
      onFileSelect(node); // Click - don't auto-focus
    } else {
      // Toggle directory
      if (expandedPaths.has(node.path)) {
        handleCollapse(node.path);
      } else {
        handleExpand(node.path);
      }
    }
  }, [setFocusedPath, onFileSelect, expandedPaths, handleExpand, handleCollapse]);

  // Execute the actual file operation (called by modal onConfirm or directly for skip-modal deletes)
  const executeFileOperation = useCallback(async (operation: FileOperationType, value: string, targetNode: FileNode) => {
    const isDir = targetNode.type === "directory";
    try {
      switch (operation) {
        case "copy":
          await copyFile(targetNode.path, value, worktreeId);
          break;
        case "delete": {
          let fileContent: string | undefined;
          let canUndo = true;
          let undoReason: string | undefined;

          if (!isDir && currentProject?.id) {
            try {
              const previewInfo = await getFilePreviewInfo(targetNode.path, worktreeId);
              if (previewInfo.viewerKind === "text") {
                fileContent = await getFileContent(targetNode.path, worktreeId);
              } else {
                canUndo = false;
                undoReason = `Undo is unavailable for ${previewInfo.viewerKind} files.`;
              }
            } catch (error) {
              canUndo = false;
              undoReason = "Undo is unavailable because Reliant could not read the file before deletion.";
              console.warn("Failed to read file content before deletion:", error);
            }
          }
          
          if (currentProject?.id) {
            addDeletedFile({
              path: targetNode.path,
              type: isDir ? "directory" : "file",
              content: fileContent,
              canUndo: isDir ? true : canUndo,
              undoReason,
              worktreeId,
              projectId: currentProject.id,
              deletedAt: Date.now(),
            });
          }
          
          await deleteFileOrFolder(targetNode.path, worktreeId);
          
          if (typeof window !== 'undefined') {
            window.focus();
            const focusTarget = document.querySelector('main, [role="main"], #root') as HTMLElement;
            if (focusTarget) {
              focusTarget.setAttribute('tabindex', '-1');
              focusTarget.focus();
              focusTarget.removeAttribute('tabindex');
            }
          }
          
          toast.notify(
            `${isDir ? "Folder" : "File"} deleted`,
            {
              description: canUndo || isDir
                ? targetNode.name
                : `${targetNode.name} • ${undoReason || "Undo unavailable"}`,
              duration: 5000,
              action: canUndo || isDir
                ? {
                    label: "Undo (Cmd+Z)",
                    onClick: () => {
                      window.dispatchEvent(new CustomEvent("file-deletion-undo"));
                    },
                  }
                : undefined,
            }
          );
          break;
        }
      }
      onRefresh();
    } catch (error) {
      console.error(`Failed to ${operation}:`, error);
      alert(`Failed to ${operation}: ${error instanceof Error ? error.message : "Unknown error"}`);
    }
  }, [worktreeId, currentProject?.id, addDeletedFile, onRefresh]);
  // Hoisted file operation handler - called by FileTreeItem context menus
  const handleFileOperation = useCallback((operation: FileOperationType, node: FileNode, skipModal?: boolean) => {
    if (operation === "delete" && skipModal) {
      // Execute delete directly without modal
      executeFileOperation("delete", "", node);
    } else {
      // Open modal for confirmation/input
      setModalState({ isOpen: true, operation, node });
    }
  }, [executeFileOperation]);


  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center space-y-2">
          <Loader2 className="w-8 h-8 animate-spin text-muted-foreground mx-auto" />
          <p className="text-sm text-muted-foreground font-mono">Loading files...</p>
        </div>
      </div>
    );
  }

  if (connecting) {
    return <DaemonConnectingState />;
  }

  if (error) {
    return (
      <div className="flex items-center justify-center h-full p-4">
        <div className="text-center space-y-2">
          <AlertCircle className="w-8 h-8 text-destructive mx-auto" />
          <p className="text-sm text-destructive font-mono">{error}</p>
          <button
            onClick={() => loadFileTree()}
            className="text-xs text-primary hover:underline font-mono"
          >
            Try again
          </button>
        </div>
      </div>
    );
  }

  if (tree.length === 0) {
    return (
      <div className="flex items-center justify-center h-full">
        <p className="text-sm text-muted-foreground font-mono">No files found</p>
      </div>
    );
  }

  return (
    <div 
      ref={containerRef}
      className="py-2 px-2 pb-12 file-tree-container"
      tabIndex={0}
      onKeyDown={handleKeyDown}
    >
      {/* Inline creation UI */}
      {isCreating && creatingType && (
        <div className="flex items-center gap-2 px-2 py-1.5 mb-1 bg-muted/30 rounded">
          <span className="w-4 h-4 flex items-center justify-center flex-shrink-0 text-muted-foreground">
            {creatingType === "file" ? (
              <FilePlus className="w-4 h-4" />
            ) : (
              <FolderPlus className="w-4 h-4" />
            )}
          </span>
          <input
            ref={inputRef}
            type="text"
            value={newItemName}
            onChange={(e) => setNewItemName(e.target.value)}
            onKeyDown={handleInputKeyDown}
            onBlur={handleCreateCancel}
            placeholder={creatingType === "file" ? "filename.ext" : "folder-name"}
            className={cn(
              "flex-1 bg-transparent border-b border-primary",
              "text-sm font-mono text-foreground",
              "outline-none focus:border-primary",
              "placeholder:text-muted-foreground/50"
            )}
          />
        </div>
      )}
      
      {sortFileNodes(tree).map((node) => (
        <FileTreeItem
          key={node.path}
          node={node}
          level={0}
          onSelect={handleItemClick}
          onPathChange={onPathChange}
          selectedFile={selectedFile}
          searchQuery={searchQuery}
          onRefresh={onRefresh}
          collapseKey={collapseKey}
          worktreeId={worktreeId}
          focusedPath={focusedPath}
          expandedPaths={expandedPaths}
          loadingPaths={loadingPaths}
          onExpand={handleExpand}
          onCollapse={handleCollapse}
          onFileOperation={handleFileOperation}
        />
      ))}

      {/* Single hoisted FileOperationsModal */}
      <FileOperationsModal
        isOpen={modalState.isOpen}
        onClose={() => setModalState({ isOpen: false, operation: null, node: null })}
        operation={modalState.operation}
        currentPath={
          modalState.node
            ? modalState.node.type === "directory"
              ? modalState.node.path
              : modalState.node.path.substring(0, modalState.node.path.lastIndexOf("/"))
            : ""
        }
        fileName={modalState.node?.name}
        onConfirm={(value) => {
          if (modalState.operation && modalState.node) {
            executeFileOperation(modalState.operation, value, modalState.node);
          }
        }}
      />
    </div>
  );
});
