import { useState, useMemo, memo, useEffect, useRef } from "react";
import {
  ChevronRight,
  ChevronDown,
  Folder,
  FileText,
  Copy,
  Scissors,
  ClipboardPaste,
  AlertCircle,
  AlertTriangle,
  FilePlus,
  FolderPlus,
  Trash2,
} from "lucide-react";
import { FileIcon } from "../ui/FileIcon";
import { ContextMenu } from "../ui/ContextMenu";
import type { ContextMenuItem } from "../ui/ContextMenu";
import type { FileNode } from "./index";
import { cn } from "../../lib/utils";
import { Tooltip } from "../ui/Tooltip";
import { createFile, createFolder, deleteFileOrFolder, copyFile, moveFile, copyDirectoryRecursive } from "../../api/fileSystem";
import { useProjectStore } from "../../store/projectStore";
import { useViewerStore } from "../../store/viewerStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useFileClipboardStore } from "../../store/fileClipboardStore";
import { queryClient } from "../../lib/query-client";
import { settingsKeys, type UserPreferences } from "../../hooks/settings-queries";
import { toast } from "../../lib/toast-manager";
import { buildFullPath, getRelativePath } from "../../lib/fileUtils";

export type FileOperationType = "copy" | "delete";

interface FileTreeItemProps {
  node: FileNode;
  level: number;
  onSelect: (node: FileNode) => void;
  onPathChange: (path: string) => void;
  selectedFile: FileNode | null;
  searchQuery?: string;
  onRefresh: () => void;
  collapseKey?: number;
  worktreeId?: string;
  // Controlled expansion/focus from parent
  focusedPath?: string | null;
  expandedPaths?: Set<string>;
  onExpand?: (path: string) => void;
  onCollapse?: (path: string) => void;
  // Hoisted modal callback
  onFileOperation?: (operation: FileOperationType, node: FileNode, skipModal?: boolean) => void;
}

// Helper function to check if a node or any of its descendants match the search
function nodeMatchesSearch(node: FileNode, query: string): boolean {
  if (!query) return true;

  const lowerQuery = query.toLowerCase();

  // Check if this node matches
  if (
    node.name.toLowerCase().includes(lowerQuery) ||
    node.path.toLowerCase().includes(lowerQuery)
  ) {
    return true;
  }

  // Recursively check children
  if (node.children) {
    return node.children.some((child) => nodeMatchesSearch(child, query));
  }

  return false;
}

// Helper function to sort file nodes: directories first (alphabetically), then files (alphabetically)
function sortFileNodes(nodes: FileNode[]): FileNode[] {
  return [...nodes].sort((a, b) => {
    // Directories come before files
    if (a.type === "directory" && b.type === "file") return -1;
    if (a.type === "file" && b.type === "directory") return 1;

    // Within same type, sort alphabetically (case-insensitive)
    return a.name.toLowerCase().localeCompare(b.name.toLowerCase());
  });
}

// Helper function to aggregate diagnostics from children
function aggregateDiagnostics(node: FileNode): {
  errors: number;
  warnings: number;
} {
  let errors = node.errorCount || 0;
  let warnings = node.warningCount || 0;

  if (node.children) {
    for (const child of node.children) {
      const childCounts = aggregateDiagnostics(child);
      errors += childCounts.errors;
      warnings += childCounts.warnings;
    }
  }

  return { errors, warnings };
}

export const FileTreeItem = memo(function FileTreeItem({
  node,
  level,
  onSelect,
  onPathChange,
  selectedFile,
  searchQuery = "",
  onRefresh,
  collapseKey = 0,
  worktreeId,
  focusedPath,
  expandedPaths,
  onExpand,
  onCollapse,
  onFileOperation,
}: FileTreeItemProps) {
  // Use controlled expansion if provided, otherwise fall back to internal state
  const [internalIsExpanded, setInternalIsExpanded] = useState(false);
  const isControlledExpansion = expandedPaths !== undefined;
  const isExpanded = isControlledExpansion 
    ? expandedPaths.has(node.path) 
    : internalIsExpanded;
  
  const [contextMenu, setContextMenu] = useState<{
    x: number;
    y: number;
  } | null>(null);
  const [creatingInline, setCreatingInline] = useState<"file" | "folder" | null>(null);
  const [newItemName, setNewItemName] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const itemRef = useRef<HTMLButtonElement>(null);
  const currentProject = useProjectStore((state) => state.currentProject);
  const viewerStore = useViewerStore();
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const { setClipboard, operation, filePath, fileName, isDirectory: clipboardIsDirectory, worktreeId: clipboardWorktreeId, projectId: clipboardProjectId, clearClipboard } = useFileClipboardStore();
  // Get the worktree for building full paths
  const effectiveWorktree = worktreeId 
    ? worktrees.find(w => w.id === worktreeId) || currentWorktree
    : currentWorktree;
  const isDirectory = node.type === "directory";
  const isSelected = selectedFile?.path === node.path;
  const isFocused = focusedPath === node.path;

  // Collapse all folders when collapseKey changes (for internal state only)
  useEffect(() => {
    if (collapseKey > 0 && !isControlledExpansion) {
      setInternalIsExpanded(false);
    }
  }, [collapseKey, isControlledExpansion]);

  // Focus input when inline creation starts
  useEffect(() => {
    if (creatingInline && inputRef.current) {
      setTimeout(() => inputRef.current?.focus(), 0);
    }
  }, [creatingInline]);

  // Note: Scroll is handled by FileTree during keyboard navigation only
  // We don't auto-scroll here to preserve scroll position when switching tabs

  // Handle cut/copy/paste keyboard events
  useEffect(() => {
    const handleCut = () => {
      if (isSelected || isFocused) {
        if (node.path) {
          setClipboard("cut", node.path, node.name, isDirectory, worktreeId, currentProject?.id);
          toast.notify(`${isDirectory ? "Directory" : "File"} cut`, { description: node.name });
        }
      }
    };

    const handleCopy = () => {
      if (isSelected || isFocused) {
        if (node.path) {
          setClipboard("copy", node.path, node.name, isDirectory, worktreeId, currentProject?.id);
          toast.notify(`${isDirectory ? "Directory" : "File"} copied`, { description: node.name });
        }
      }
    };

    // Note: Paste is handled globally in RightSidebar to prevent multiple pastes
    // FileTreeItem only handles cut/copy for individual items

    window.addEventListener("file-tree-cut", handleCut);
    window.addEventListener("file-tree-copy", handleCopy);
    // Removed file-tree-paste listener - handled globally in RightSidebar

    return () => {
      window.removeEventListener("file-tree-cut", handleCut);
      window.removeEventListener("file-tree-copy", handleCopy);
      // Removed file-tree-paste listener cleanup
    };
  }, [isSelected, isFocused, isDirectory, node.path, node.name, worktreeId, currentProject?.id, setClipboard]);

  // Get aggregate diagnostic counts for directories
  const diagnosticCounts = useMemo(
    () => (isDirectory ? aggregateDiagnostics(node) : null),
    [isDirectory, node]
  );

  // Memoize search matching to avoid recalculation on every render
  const shouldShow = useMemo(
    () => nodeMatchesSearch(node, searchQuery),
    [node, searchQuery]
  );

  // Filter children based on search query (deep search) and sort them
  const filteredChildren = useMemo(
    () =>
      node.children
        ? sortFileNodes(
            node.children.filter((child) =>
              nodeMatchesSearch(child, searchQuery)
            )
          )
        : undefined,
    [node.children, searchQuery]
  );

  if (!shouldShow) return null;

  // Auto-expand directories when searching and they contain matches
  const shouldAutoExpand =
    searchQuery && filteredChildren && filteredChildren.length > 0;

  const handleClick = () => {
    onSelect(node);
  };

  const handleContextMenu = (e: React.MouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setContextMenu({ x: e.clientX, y: e.clientY });
  };

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text).catch((err) => {
      console.error("Failed to copy to clipboard:", err);
    });
  };

  const handleInlineCreate = async () => {
    if (!newItemName.trim() || !creatingInline) return;

    try {
      const basePath = node.path;
      const fullPath = `${basePath}/${newItemName.trim()}`;
      
      if (creatingInline === "file") {
        await createFile(fullPath);
        
        // Open the newly created file in viewer tab
        if (currentProject?.id) {
          const newFileNode: FileNode = {
            name: newItemName.trim(),
            path: fullPath,
            type: "file",
          };
          // Open file and focus it
          viewerStore.openFileViewer(newFileNode, currentProject.id, worktreeId);
          // Focus the editor after a short delay
          setTimeout(() => {
            const activeViewer = viewerStore.getActiveViewer();
            if (activeViewer && activeViewer.type === "file") {
              window.dispatchEvent(new CustomEvent('file-viewer-focus', { 
                detail: { viewerId: activeViewer.id } 
              }));
            }
          }, 100);
        }
      } else {
        await createFolder(fullPath);
      }
      
      // Reset inline creation state
      setCreatingInline(null);
      setNewItemName("");
      
      // Expand this folder to show new item
      if (isControlledExpansion && onExpand) {
        onExpand(node.path);
      } else {
        setInternalIsExpanded(true);
      }
      
      // Refresh tree
      onRefresh();
    } catch (error) {
      console.error("Failed to create:", error);
      alert(`Failed to create: ${error instanceof Error ? error.message : "Unknown error"}`);
    }
  };

  const handleInlineCancel = () => {
    setCreatingInline(null);
    setNewItemName("");
  };

  const handleInlineKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === "Enter") {
      e.preventDefault();
      e.stopPropagation();
      handleInlineCreate();
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      handleInlineCancel();
    }
  };

  const getContextMenuItems = (): ContextMenuItem[] => {
    const items: ContextMenuItem[] = [];

    if (isDirectory) {
      items.push({
        label: isExpanded ? "Collapse" : "Expand",
        icon: isExpanded ? (
          <ChevronDown className="w-4 h-4" />
        ) : (
          <ChevronRight className="w-4 h-4" />
        ),
        onClick: () => {
          if (isControlledExpansion) {
            if (isExpanded && onCollapse) {
              onCollapse(node.path);
            } else if (!isExpanded && onExpand) {
              onExpand(node.path);
            }
          } else {
            setInternalIsExpanded(!isExpanded);
          }
        },
      });
    } else {
      items.push({
        label: "Open",
        icon: <FileText className="w-4 h-4" />,
        onClick: () => onSelect(node),
      });
    }

    // Add file operations
    if (isDirectory) {
      items.push(
        { label: "", onClick: () => {}, separator: true },
        {
          label: "New File",
          icon: <FilePlus className="w-4 h-4" />,
          onClick: () => {
            if (isControlledExpansion && onExpand) {
              onExpand(node.path);
            } else {
              setInternalIsExpanded(true);
            }
            setCreatingInline("file");
            setNewItemName("");
          },
        },
        {
          label: "New Folder",
          icon: <FolderPlus className="w-4 h-4" />,
          onClick: () => {
            if (isControlledExpansion && onExpand) {
              onExpand(node.path);
            } else {
              setInternalIsExpanded(true);
            }
            setCreatingInline("folder");
            setNewItemName("");
          },
        }
      );
    }

    // Copy operations
    const fullPath = buildFullPath(effectiveWorktree?.path, node.path);
    items.push(
      { label: "", onClick: () => {}, separator: true },
      {
        label: "Copy Path",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => copyToClipboard(fullPath),
      },
      {
        label: "Copy Relative Path",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => {
          // Copy path relative to workspace root
          copyToClipboard(getRelativePath(node.path, effectiveWorktree?.path));
        },
      },
      {
        label: "Copy Name",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => copyToClipboard(node.name),
      }
    );

    // Cut/Copy operations (for both files and directories)
    items.push(
      { label: "", onClick: () => {}, separator: true },
      {
        label: "Cut",
        icon: <Scissors className="w-4 h-4" />,
        onClick: () => {
          setClipboard("cut", node.path, node.name, isDirectory, worktreeId, currentProject?.id);
          toast.notify(`${isDirectory ? "Directory" : "File"} cut`, { description: node.name });
        },
      },
      {
        label: "Copy",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => {
          setClipboard("copy", node.path, node.name, isDirectory, worktreeId, currentProject?.id);
          toast.notify(`${isDirectory ? "Directory" : "File"} copied`, { description: node.name });
        },
      }
    );
    
    // Copy File... option (only for files)
    if (!isDirectory) {
      items.push({
        label: "Copy File...",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => onFileOperation?.("copy", node),
      });
    }

    // Paste operation (only for directories)
    if (isDirectory && operation && filePath && fileName) {
      items.push(
        { label: "", onClick: () => {}, separator: true },
        {
          label: `Paste ${operation === "cut" ? "(Move)" : "(Copy)"}`,
          icon: <ClipboardPaste className="w-4 h-4" />,
          onClick: async () => {
            // Check if same project/worktree
            if (clipboardProjectId !== currentProject?.id) {
              toast.notify("Cannot paste", { description: "File is from a different project" });
              return;
            }
            
            const destinationPath = `${node.path}/${fileName}`;
            
            // Don't paste to same location
            if (filePath === destinationPath) {
              toast.notify("Cannot paste", { description: "File is already in this location" });
              return;
            }

            try {
              if (operation === "cut") {
                if (clipboardIsDirectory) {
                  await createFolder(destinationPath, clipboardWorktreeId || worktreeId);
                  await copyDirectoryRecursive(filePath, destinationPath, clipboardWorktreeId || worktreeId);
                  await deleteFileOrFolder(filePath, clipboardWorktreeId || worktreeId);
                } else {
                  await moveFile(filePath, destinationPath, clipboardWorktreeId || worktreeId);
                }
                toast.notify(`${clipboardIsDirectory ? "Directory" : "File"} moved`, { description: fileName });
              } else {
                if (clipboardIsDirectory) {
                  await createFolder(destinationPath, clipboardWorktreeId || worktreeId);
                  await copyDirectoryRecursive(filePath, destinationPath, clipboardWorktreeId || worktreeId);
                } else {
                  await copyFile(filePath, destinationPath, clipboardWorktreeId || worktreeId);
                }
                toast.notify(`${clipboardIsDirectory ? "Directory" : "File"} copied`, { description: fileName });
              }
              clearClipboard();
              onRefresh();
            } catch (error) {
              console.error("Failed to paste:", error);
              const errorMessage = error instanceof Error ? error.message : "Unknown error";
              toast.notify("Failed to paste", { description: errorMessage });
            }
          },
        }
      );
    }

    // Delete
    items.push(
      { label: "", onClick: () => {}, separator: true },
      {
        label: "Delete",
        icon: <Trash2 className="w-4 h-4" />,
        onClick: () => {
          const skipConfirmation = queryClient.getQueryData<UserPreferences>(settingsKeys.preferences())?.skipDeleteConfirmation ?? false;
          onFileOperation?.("delete", node, skipConfirmation);
        },
      }
    );

    return items;
  };

  // Use auto-expand when searching
  const effectiveIsExpanded = shouldAutoExpand || isExpanded;

  const indentRem = 0.75;
  const paddingLeft = `${level * indentRem}rem`;

  return (
    <div className="relative group/row">
      {/* Vertical indent guide lines for opened directories */}
      {level > 0 &&
        Array.from({ length: level }, (_, i) => (
          <div
            key={i}
            className="absolute top-0 bottom-0 w-px opacity-40 group-hover/row:opacity-70 transition-opacity pointer-events-none"
            style={{
              left: `${i * indentRem}rem`,
              backgroundColor: "hsl(var(--border))",
            }}
            aria-hidden
          />
        ))}
      <Tooltip content={buildFullPath(effectiveWorktree?.path, node.path)} delay={300} placement="bottom">
        <button
          ref={itemRef}
          data-path={node.path}
          onClick={handleClick}
          onContextMenu={handleContextMenu}
          tabIndex={-1}
          className={cn(
            "file-tree-item w-full flex items-center gap-2 px-2 py-1.5 text-sm font-mono transition-all duration-200 text-left group relative"
          )}
          style={{
            paddingLeft,
            backgroundColor: isSelected 
              ? "hsl(var(--tab-active) / 0.2)" 
              : isFocused 
                ? "hsl(var(--foreground) / 0.1)" 
                : undefined,
          }}
          onMouseEnter={(e) => {
            if (!isSelected && !isFocused) {
              e.currentTarget.style.backgroundColor = "var(--transparent-button-hover)";
            }
          }}
          onMouseLeave={(e) => {
            if (!isSelected && !isFocused) {
              e.currentTarget.style.backgroundColor = "";
            }
          }}
        >
        {/* Expand/collapse icon for directories */}
        {isDirectory && (
          <span className="w-4 h-4 flex items-center justify-center text-muted-foreground">
            {effectiveIsExpanded ? (
              <ChevronDown className="w-3.5 h-3.5" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5" />
            )}
          </span>
        )}
        {!isDirectory && <span className="w-4" />}

        {/* File/folder icon */}
        <span className="w-4 h-4 flex items-center justify-center flex-shrink-0">
          {isDirectory ? (
            <Folder
              className={cn(
                "w-4 h-4 transition-colors",
                effectiveIsExpanded ? "text-primary" : "text-muted-foreground"
              )}
            />
          ) : (
            <FileIcon fileName={node.name} className="w-4 h-4" />
          )}
        </span>

        {/* File/folder name */}
        <span
          className={cn(
            "flex-1 truncate transition-colors",
            isSelected && "text-primary font-semibold",
            !isSelected && "group-hover:text-foreground"
          )}
        >
          {node.name}
        </span>

        {/* Diagnostic indicators for files and directories */}
        {((isDirectory &&
          diagnosticCounts &&
          (diagnosticCounts.errors > 0 || diagnosticCounts.warnings > 0)) ||
          (!isDirectory && (node.errorCount || node.warningCount))) && (
          <div className="flex items-center gap-1 flex-shrink-0">
            {(() => {
              const errorCount =
                isDirectory && diagnosticCounts
                  ? diagnosticCounts.errors
                  : node.errorCount || 0;
              const warningCount =
                isDirectory && diagnosticCounts
                  ? diagnosticCounts.warnings
                  : node.warningCount || 0;

              return (
                <>
                  {errorCount > 0 && (
                    <Tooltip
                      content={`${errorCount} error${
                        errorCount > 1 ? "s" : ""
                      }${isDirectory ? " in this folder" : ""}`}
                      placement="top"
                    >
                      <div className="flex items-center gap-0.5 text-destructive">
                        <AlertCircle className="w-3 h-3" />
                        <span className="text-xs font-medium">
                          {errorCount}
                        </span>
                      </div>
                    </Tooltip>
                  )}
                  {warningCount > 0 && (
                    <Tooltip
                      content={`${warningCount} warning${
                        warningCount > 1 ? "s" : ""
                      }${isDirectory ? " in this folder" : ""}`}
                      placement="top"
                    >
                      <div className="flex items-center gap-0.5 text-amber-500">
                        <AlertTriangle className="w-3 h-3" />
                        <span className="text-xs font-medium">
                          {warningCount}
                        </span>
                      </div>
                    </Tooltip>
                  )}
                </>
              );
            })()}
          </div>
        )}

        {/* File size removed per user request */}
        </button>
      </Tooltip>

      {/* Inline creation UI within folder */}
      {isDirectory && effectiveIsExpanded && creatingInline && (
        <div
          className="flex items-center gap-2 px-2 py-1.5 bg-muted/30 rounded mx-2 my-1"
          style={{ paddingLeft: `${(level + 1) * 0.75}rem` }}
        >
          <span className="w-4 h-4 flex-shrink-0">
            {creatingInline === "file" ? (
              <FilePlus className="w-4 h-4 text-primary" />
            ) : (
              <FolderPlus className="w-4 h-4 text-primary" />
            )}
          </span>
          <input
            ref={inputRef}
            type="text"
            value={newItemName}
            onChange={(e) => setNewItemName(e.target.value)}
            onKeyDown={handleInlineKeyDown}
            onBlur={handleInlineCancel}
            placeholder={creatingInline === "file" ? "filename.ext" : "folder-name"}
            className="flex-1 bg-transparent border-b border-primary text-sm font-mono focus:outline-none text-foreground placeholder:text-muted-foreground/60"
          />
        </div>
      )}

      {/* Render children for expanded directories */}
      {isDirectory && effectiveIsExpanded && filteredChildren && (
        <div>
          {filteredChildren.map((child) => (
            <FileTreeItem
              key={child.path}
              node={child}
              level={level + 1}
              onSelect={onSelect}
              onPathChange={onPathChange}
              selectedFile={selectedFile}
              searchQuery={searchQuery}
              onRefresh={onRefresh}
              collapseKey={collapseKey}
              worktreeId={worktreeId}
              focusedPath={focusedPath}
              expandedPaths={expandedPaths}
              onExpand={onExpand}
              onCollapse={onCollapse}
              onFileOperation={onFileOperation}
            />
          ))}
        </div>
      )}

      {/* Context Menu */}
      {contextMenu && (
        <ContextMenu
          items={getContextMenuItems()}
          position={contextMenu}
          onClose={() => setContextMenu(null)}
        />
      )}


    </div>
  );
})

// Helper component to highlight search matches - kept for potential future use
// function HighlightText({
//   text,
//   highlight,
// }: {
//   text: string;
//   highlight: string;
// }) {
//   if (!highlight.trim()) {
//     return <>{text}</>;
//   }
//   const regex = new RegExp(`(${highlight})`, "gi");
//   const parts = text.split(regex);
//   return (
//     <>
//       {parts.map((part, index) =>
//         regex.test(part) ? (
//           <mark key={index} className="bg-yellow-400/30 text-foreground font-semibold">{part}</mark>
//         ) : (
//           <span key={index}>{part}</span>
//         )
//       )}
//     </>
//   );
// }

// Helper function to format file size - kept for potential future use
// function formatFileSize(bytes: number): string {
//   if (bytes === 0) return "0 B";
//   const k = 1024;
//   const sizes = ["B", "KB", "MB", "GB"];
//   const i = Math.floor(Math.log(bytes) / Math.log(k));
//   return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`;
// }