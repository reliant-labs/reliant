import { useEffect, useState, useRef } from "react";
import { Loader2, AlertCircle } from "lucide-react";
import { getMonacoLanguage, configureMonacoTheme, getCurrentMonacoTheme } from "../../lib/monacoTheme";
import { useEditorStore } from "../../store/editorStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { getFilePreviewInfo, type FilePreviewInfo } from "../../api/fileSystem";
import type { FileChange } from "../Chat/RecentChanges";
import { monacoManager } from "../../lib/monacoManager";
import { getRelativePath } from "../../lib/fileUtils";
import { FileChangeStatus } from "../../gen/reliant/v1/common_pb";
import { FileViewerTab } from "./FileViewerTab";
import type { FileNode } from "./index";

interface MonacoDiffViewerProps {
  file: FileChange;
}

export function MonacoDiffViewer({ file }: MonacoDiffViewerProps) {
  const settings = useEditorStore((state) => state.settings);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const currentWorktreeId = currentWorktree?.id;
  // Display path relative to workspace
  const displayPath = getRelativePath(file.path, currentWorktree?.path);
  const previewFile: FileNode = {
    name: file.path.split("/").pop() || file.path,
    path: file.path,
    type: "file",
  };
  const [previewInfo, setPreviewInfo] = useState<FilePreviewInfo | null>(null);
  const [originalContent, setOriginalContent] = useState<string>("");
  const [modifiedContent, setModifiedContent] = useState<string>("");
  const [loading, setLoading] = useState(true);
  const [showLoadingSpinner, setShowLoadingSpinner] = useState(false); // Only show after 5 seconds
  const [error, setError] = useState<string | null>(null);
  const [isReady, setIsReady] = useState(false); // Track if editor is scrolled to first diff
  const containerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<any>(null);
  const monacoRef = useRef<any>(null);
  const hasScrolledRef = useRef<boolean>(false);
  const loadingTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  // Load diff content
  useEffect(() => {
    loadDiffContent();
    
    // Cleanup timeout on unmount or when file changes
    return () => {
      if (loadingTimeoutRef.current) {
        clearTimeout(loadingTimeoutRef.current);
        loadingTimeoutRef.current = null;
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file.path, file.status, currentWorktreeId]);

  // Reset scroll state when file changes
  useEffect(() => {
    hasScrolledRef.current = false;
    setIsReady(false);
  }, [file.path, file.status]);

  // Create and mount the diff editor
  useEffect(() => {
    if (!containerRef.current || loading || error) {
      return;
    }

    let cancelled = false;
    let diffEditor: any = null;
    let originalModel: any = null;
    let modifiedModel: any = null;
    let resizeObserver: ResizeObserver | null = null;

    // Initialize Monaco using manager
    monacoManager.getMonaco().then((monaco) => {
      if (cancelled || !containerRef.current) return;

      monacoRef.current = monaco;

      // Determine if this file should be editable (working tree files are editable, staged/index files are read-only)
      const isEditable = file.status === FileChangeStatus.MODIFIED;
      
      // Create the diff editor
      diffEditor = monaco.editor.createDiffEditor(containerRef.current, {
        readOnly: !isEditable,
        automaticLayout: true,
        minimap: { enabled: settings.minimap },
        fontSize: settings.fontSize,
        wordWrap: settings.wordWrap ? "on" : "off",
        scrollBeyondLastLine: false,
        renderWhitespace: settings.renderWhitespace ? "all" : "none",
        bracketPairColorization: { enabled: settings.bracketPairColorization },
        guides: {
          bracketPairs: settings.guides,
          indentation: settings.guides,
        },
        renderLineHighlight: settings.renderLineHighlight,
        tabSize: settings.tabSize,
        renderSideBySide: settings.diffSideBySide,
        ignoreTrimWhitespace: false,
        renderIndicators: true,
        originalEditable: false,
        modifiedEditable: isEditable,
        enableSplitViewResizing: true,
        renderOverviewRuler: true,
        lineNumbers: settings.lineNumbers ? "on" : "off",
        scrollbar: {
          alwaysConsumeMouseWheel: false,
        },
      });

      // Create models
      const language = getMonacoLanguage(file.path);
      originalModel = monaco.editor.createModel(originalContent, language);
      modifiedModel = monaco.editor.createModel(modifiedContent, language);

      // Set models
      diffEditor.setModel({
        original: originalModel,
        modified: modifiedModel,
      });

      editorRef.current = diffEditor;

      // Apply theme
      const themeName = getCurrentMonacoTheme();
      monaco.editor.setTheme(themeName);
      
      // Scroll to first diff immediately after editor is created
      // Do this before the editor is visible to avoid showing scroll animation
      const scrollToFirstDiffImmediate = () => {
        try {
          const modifiedEditor = diffEditor.getModifiedEditor();
          if (!modifiedEditor) {
            // Retry if not ready
            setTimeout(scrollToFirstDiffImmediate, 50);
            return;
          }
          
          // Compute first diff line
          const findFirstDiffLine = (original: string, modified: string): number | null => {
            if (!original && modified) return 1;
            const originalLines = original.split("\n");
            const modifiedLines = modified.split("\n");
            const maxLines = Math.max(originalLines.length, modifiedLines.length);
            for (let i = 0; i < maxLines; i++) {
              if (originalLines[i] !== modifiedLines[i]) {
                return i + 1;
              }
            }
            return null;
          };
          
          const firstDiffLine = findFirstDiffLine(originalContent, modifiedContent);
          if (firstDiffLine !== null) {
            // Set cursor position
            modifiedEditor.setPosition({ lineNumber: firstDiffLine, column: 1 });
            
            // Force layout
            diffEditor.layout();
            
            // Wait for editor to be ready, then scroll
            requestAnimationFrame(() => {
              diffEditor.layout();
              
              // Use revealLineInCenter - this is the most reliable method
              modifiedEditor.revealLineInCenter(firstDiffLine);
              
              // Also set scroll directly as backup
              requestAnimationFrame(() => {
                const scrollableElement = modifiedEditor.getScrollableElement();
                if (scrollableElement) {
                  const lineTop = modifiedEditor.getTopForLineNumber(firstDiffLine);
                  if (lineTop !== -1 && lineTop >= 0) {
                    const editorHeight = modifiedEditor.getLayoutInfo().height;
                    const lineHeight = modifiedEditor.getOption(monaco.editor.EditorOption.lineHeight);
                    const targetScrollTop = lineTop - (editorHeight / 2) + (lineHeight / 2);
                    scrollableElement.scrollTop = Math.max(0, targetScrollTop);
                  }
                }
                
                // Show editor after scroll is set
                requestAnimationFrame(() => {
                  setIsReady(true);
                  hasScrolledRef.current = true;
                });
              });
            });
          } else {
            // No diffs found, show editor immediately
            setIsReady(true);
            hasScrolledRef.current = true;
          }
        } catch (err) {
          console.debug("[MonacoDiffViewer] Error in immediate scroll:", err);
          setIsReady(true);
          hasScrolledRef.current = true;
        }
      };
      
      // Start scrolling after editor is initialized
      setTimeout(scrollToFirstDiffImmediate, 150);
      
      // Set up ResizeObserver after editor is created
      if (containerRef.current) {
        resizeObserver = new ResizeObserver(() => {
          if (diffEditor) {
            diffEditor.layout();
          }
        });
        
        resizeObserver.observe(containerRef.current);
      }
    });

    // Cleanup function
    return () => {
      cancelled = true;

      // Disconnect ResizeObserver
      if (resizeObserver) {
        resizeObserver.disconnect();
        resizeObserver = null;
      }

      // IMPORTANT: Reset the diff editor's model BEFORE disposing anything
      // This prevents "TextModel got disposed before DiffEditorWidget model got reset" errors
      if (diffEditor) {
        try {
          diffEditor.setModel(null);
        } catch (e) {
          // Ignore errors if editor is already disposed
        }
        diffEditor.dispose();
      }

      // Now it's safe to dispose the models
      originalModel?.dispose();
      modifiedModel?.dispose();

      editorRef.current = null;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [file.path, file.status, originalContent, modifiedContent, loading, error]);

  // Update editable state when file status changes
  useEffect(() => {
    if (!editorRef.current) return;

    const isEditable = file.status === FileChangeStatus.MODIFIED;
    editorRef.current.updateOptions({
      readOnly: !isEditable,
      modifiedEditable: isEditable,
    });
  }, [file.status]);

  // Update editor options when settings change
  useEffect(() => {
    if (!editorRef.current) return;

    const modifiedEditor = editorRef.current.getModifiedEditor();
    const originalEditor = editorRef.current.getOriginalEditor();

    if (!modifiedEditor || !originalEditor) return;

    const commonOptions = {
      minimap: { enabled: settings.minimap },
      fontSize: settings.fontSize,
      wordWrap: settings.wordWrap ? "on" : "off",
      renderWhitespace: settings.renderWhitespace ? "all" : "none",
      bracketPairColorization: { enabled: settings.bracketPairColorization },
      guides: {
        bracketPairs: settings.guides,
        indentation: settings.guides,
      },
      renderLineHighlight: settings.renderLineHighlight,
      tabSize: settings.tabSize,
    };

    // In inline mode, hide line numbers on original editor to avoid double column
    if (!settings.diffSideBySide) {
      modifiedEditor.updateOptions({
        ...commonOptions,
        lineNumbers: settings.lineNumbers ? "on" : "off",
      });
      originalEditor.updateOptions({
        ...commonOptions,
        lineNumbers: "off",
      });
    } else {
      // In side-by-side mode, show line numbers on both
      modifiedEditor.updateOptions({
        ...commonOptions,
        lineNumbers: settings.lineNumbers ? "on" : "off",
      });
      originalEditor.updateOptions({
        ...commonOptions,
        lineNumbers: settings.lineNumbers ? "on" : "off",
      });
    }

    // Update diff-specific options
    editorRef.current.updateOptions({
      renderSideBySide: settings.diffSideBySide,
    });
  }, [settings.bracketPairColorization, settings.diffSideBySide, settings.fontSize, settings.guides, settings.lineNumbers, settings.minimap, settings.renderLineHighlight, settings.renderWhitespace, settings.tabSize, settings.wordWrap]);

  // Listen for theme changes
  useEffect(() => {
    const handleThemeChange = () => {
      if (monacoRef.current) {
        // Reconfigure theme to pick up light/dark mode changes
        configureMonacoTheme(monacoRef.current);
        const themeName = getCurrentMonacoTheme();
        monacoRef.current.editor.setTheme(themeName);
      }
    };

    window.addEventListener("theme-applied", handleThemeChange);
    window.addEventListener("appearance-updated", handleThemeChange);

    return () => {
      window.removeEventListener("theme-applied", handleThemeChange);
      window.removeEventListener("appearance-updated", handleThemeChange);
    };
  }, []);

  // Scroll to first diff when editor is ready (backup/retry mechanism)
  // This only runs if the immediate scroll in editor creation didn't work
  useEffect(() => {
    if (loading || error || !originalContent || !modifiedContent) {
      return;
    }
    
    // If already scrolled from the immediate scroll in editor creation, skip
    if (hasScrolledRef.current) {
      return;
    }
    
    // Reset ready state only if we haven't scrolled yet
    if (!hasScrolledRef.current) {
      setIsReady(false);
    }

    // Compute the first diff by comparing content line by line
    const findFirstDiffLine = (original: string, modified: string): number | null => {
      if (!original && modified) {
        // New file - first line is the diff
        return 1;
      }
      
      const originalLines = original.split("\n");
      const modifiedLines = modified.split("\n");
      const maxLines = Math.max(originalLines.length, modifiedLines.length);
      
      for (let i = 0; i < maxLines; i++) {
        const origLine = originalLines[i];
        const modLine = modifiedLines[i];
        
        // If lines differ, this is the first diff
        if (origLine !== modLine) {
          return i + 1; // Line numbers are 1-based
        }
      }
      
      return null; // No differences found
    };

    const scrollToFirstDiff = () => {
      // Only scroll once per file
      if (hasScrolledRef.current) {
        return;
      }

      try {
        const diffEditor = editorRef.current;
        if (!diffEditor) {
          // Retry if editor not ready
          setTimeout(scrollToFirstDiff, 100);
          return;
        }

        const modifiedEditor = diffEditor.getModifiedEditor();
        if (!modifiedEditor) {
          // Retry if editor not ready
          setTimeout(scrollToFirstDiff, 100);
          return;
        }

        // Ensure editor is laid out
        diffEditor.layout();

        // Find first diff line
        const firstDiffLine = findFirstDiffLine(originalContent, modifiedContent);
        
        if (firstDiffLine !== null) {
          // Set cursor position first
          modifiedEditor.setPosition({
            lineNumber: firstDiffLine,
            column: 1,
          });

          // Force layout to ensure everything is rendered
          diffEditor.layout();
          
          // Use multiple animation frames to ensure scroll happens before showing
          requestAnimationFrame(() => {
            // Force another layout
            diffEditor.layout();
            
            requestAnimationFrame(() => {
              try {
                // Try to get scrollable element and set scroll directly
                const scrollableElement = modifiedEditor.getScrollableElement();
                if (scrollableElement) {
                  const lineTop = modifiedEditor.getTopForLineNumber(firstDiffLine);
                  if (lineTop !== -1 && lineTop >= 0) {
                    const editorHeight = modifiedEditor.getLayoutInfo().height;
                    const lineHeight = modifiedEditor.getOption(monacoRef.current?.editor.EditorOption.lineHeight || 19);
                    const targetScrollTop = lineTop - (editorHeight / 2) + (lineHeight / 2);
                    
                    // Set scroll position
                    scrollableElement.scrollTop = Math.max(0, targetScrollTop);
                  }
                }
                
                // Also use revealLineInCenter as backup
                modifiedEditor.revealLineInCenter(firstDiffLine);
                
                // One more frame to ensure scroll is set, then show
                requestAnimationFrame(() => {
                  setIsReady(true);
                  hasScrolledRef.current = true;
                });
              } catch (err) {
                console.debug("[MonacoDiffViewer] Error scrolling:", err);
                setIsReady(true);
                hasScrolledRef.current = true;
              }
            });
          });
        } else {
          // No diffs found, show editor immediately
          setIsReady(true);
          hasScrolledRef.current = true;
        }
      } catch (err) {
        console.debug("[MonacoDiffViewer] Failed to scroll to first diff:", err);
        // Show editor even if scrolling failed
        setIsReady(true);
        hasScrolledRef.current = true;
      }
    };

    // Wait for editor to be fully ready, with multiple attempts
    let attempts = 0;
    const maxAttempts = 15;
    
    const attemptScroll = () => {
      attempts++;
      if (attempts > maxAttempts) {
        // Fallback: show editor even if we couldn't scroll
        setIsReady(true);
        return;
      }
      
      if (editorRef.current) {
        scrollToFirstDiff();
      } else {
        setTimeout(attemptScroll, 100);
      }
    };

    // Start attempting after a short delay
    const timeoutId = setTimeout(attemptScroll, 300);
    
    // Safety fallback: show editor after 2 seconds even if scrolling failed
    const safetyTimeoutId = setTimeout(() => {
      if (!hasScrolledRef.current) {
        setIsReady(true);
      }
    }, 2000);

    return () => {
      clearTimeout(timeoutId);
      clearTimeout(safetyTimeoutId);
    };
  }, [loading, error, originalContent, modifiedContent, file.path]);

  const loadDiffContent = async () => {
    setLoading(true);
    setShowLoadingSpinner(false);
    setError(null);
    setPreviewInfo(null);

    // Only show loading spinner if loading takes more than 5 seconds
    loadingTimeoutRef.current = setTimeout(() => {
      if (loading) {
        setShowLoadingSpinner(true);
      }
    }, 5000);

    try {
      try {
        const info = await getFilePreviewInfo(file.path, currentWorktreeId);
        setPreviewInfo(info);

        if (info.viewerKind !== "text") {
          return;
        }
      } catch (previewError) {
        console.warn("[MonacoDiffViewer] Failed to load preview metadata, falling back to diff rendering:", previewError);
      }

      // For new files, show empty original and file content as modified
      if (file.is_new && file.content) {
        setOriginalContent("");
        setModifiedContent(file.content);
      }
      // For modified files, use the full file contents if available
      else if (file.content && file.original_content) {
        setOriginalContent(file.original_content);
        setModifiedContent(file.content);
      }
      // Fallback: if we only have content, show it as modified
      else if (file.content) {
        setOriginalContent("");
        setModifiedContent(file.content);
      }
      // Last resort: try parsing the diff (for backwards compatibility)
      else if (file.diff) {
        const { original, modified } = parseGitDiff(file.diff);
        setOriginalContent(original);
        setModifiedContent(modified);
      } else {
        setError("No diff or content available");
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load diff");
    } finally {
      // Clear the timeout if loading finishes before 5 seconds
      if (loadingTimeoutRef.current) {
        clearTimeout(loadingTimeoutRef.current);
        loadingTimeoutRef.current = null;
      }
      setLoading(false);
      setShowLoadingSpinner(false);
    }
  };

  if (loading && showLoadingSpinner) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center space-y-2">
          <Loader2 className="w-8 h-8 animate-spin text-muted-foreground mx-auto" />
          <p className="text-sm text-muted-foreground font-mono">Loading diff...</p>
        </div>
      </div>
    );
  }
  
  // If loading but spinner not shown yet, show empty div (editor will appear when ready)
  if (loading && !showLoadingSpinner) {
    return <div className="h-full" />;
  }

  if (error) {
    return (
      <div className="flex items-center justify-center h-full p-4">
        <div className="text-center space-y-2">
          <AlertCircle className="w-8 h-8 text-destructive mx-auto" />
          <p className="text-sm text-destructive font-mono">{error}</p>
        </div>
      </div>
    );
  }

  if (previewInfo && previewInfo.viewerKind !== "text") {
    return <FileViewerTab file={previewFile} worktreeId={currentWorktreeId} />;
  }

  return (
    <div className="flex flex-col h-full bg-background">
      {/* Toolbar */}
      <div className="flex items-center justify-between px-4 py-2 border-b border-border bg-muted/20">
        <div className="flex items-center gap-3 flex-1 min-w-0">
          <span className="text-xs text-muted-foreground font-mono truncate">
            {displayPath}
          </span>
          <StatusBadge status={file.status} />
        </div>
      </div>

      {/* Diff Editor Container */}
      <div 
        ref={containerRef} 
        className="flex-1 overflow-hidden"
        style={{ 
          opacity: isReady ? 1 : 0,
          pointerEvents: isReady ? 'auto' : 'none',
        }}
      />
      <style>{`
        /* Disable smooth scrolling on Monaco editor to prevent visible scroll animation */
        .monaco-editor .monaco-scrollable-element > .scrollbar {
          scroll-behavior: auto !important;
        }
        .monaco-editor .monaco-scrollable-element {
          scroll-behavior: auto !important;
        }
      `}</style>

      {/* Footer with file info */}
      <div className="flex items-center justify-between px-4 py-2 border-t border-border bg-muted/20 text-xs font-mono text-muted-foreground">
        <div className="flex items-center gap-4">
          <span>{modifiedContent.split("\n").length} lines</span>
          <span>{getLanguageDisplay(file.path)}</span>
        </div>
        <div className="flex items-center gap-2">
          <StatusLabel status={file.status} />
        </div>
      </div>
    </div>
  );
}

function getStatusConfig(status: FileChangeStatus): {
  label: string;
  color: string;
  title: string;
} {
  switch (status) {
    case FileChangeStatus.MODIFIED:
      return { label: "M", color: "text-amber-500", title: "Modified" };
    case FileChangeStatus.STAGED:
      return { label: "S", color: "text-blue-500", title: "Staged" };
    case FileChangeStatus.UNTRACKED:
      return { label: "N", color: "text-green-500", title: "New/Untracked" };
    default:
      return { label: "?", color: "text-muted-foreground", title: "Unknown" };
  }
}

function getStatusLabel(status: FileChangeStatus): string {
  switch (status) {
    case FileChangeStatus.MODIFIED:
      return "Modified";
    case FileChangeStatus.STAGED:
      return "Staged for commit";
    case FileChangeStatus.UNTRACKED:
      return "New file";
    default:
      return "Unknown";
  }
}

function StatusBadge({ status }: { status: FileChange["status"] }) {
  const config = getStatusConfig(status);

  return (
    <span
      className={`text-xs font-mono font-semibold ${config.color}`}
      title={config.title}
    >
      {config.label}
    </span>
  );
}

function StatusLabel({ status }: { status: FileChange["status"] }) {
  return <span>{getStatusLabel(status)}</span>;
}

function getLanguageDisplay(filename: string): string {
  const ext = filename.split(".").pop()?.toLowerCase() || "";
  const langMap: Record<string, string> = {
    ts: "TypeScript",
    tsx: "TypeScript React",
    js: "JavaScript",
    jsx: "JavaScript React",
    py: "Python",
    go: "Go",
    rs: "Rust",
    java: "Java",
    md: "Markdown",
    json: "JSON",
    html: "HTML",
    css: "CSS",
    yml: "YAML",
    yaml: "YAML",
  };
  return langMap[ext] || ext.toUpperCase();
}

/**
 * Parse a git diff to extract original and modified content
 * This is a simplified parser - for production, consider using a proper diff parser
 */
function parseGitDiff(diff: string): { original: string; modified: string } {
  const lines = diff.split("\n");
  const originalLines: string[] = [];
  const modifiedLines: string[] = [];

  for (const line of lines) {
    // Skip diff headers
    if (
      line.startsWith("diff --git") ||
      line.startsWith("index ") ||
      // Only treat these as headers when they match the unified-diff header format.
      // (Otherwise we risk dropping actual content like "++++foo" from added lines.)
      line.startsWith("--- ") ||
      line.startsWith("+++ ") ||
      line.startsWith("@@ ")
    ) {
      continue;
    }

    // Lines removed from original (shown in red in diff)
    if (line.startsWith("-")) {
      originalLines.push(line.slice(1));
    }
    // Lines added to modified (shown in green in diff)
    else if (line.startsWith("+")) {
      modifiedLines.push(line.slice(1));
    }
    // Context lines (unchanged)
    else if (line.startsWith(" ")) {
      const content = line.slice(1);
      originalLines.push(content);
      modifiedLines.push(content);
    }
    // Plain lines (no prefix) - treat as context
    else if (line.length > 0) {
      originalLines.push(line);
      modifiedLines.push(line);
    }
  }

  return {
    original: originalLines.join("\n"),
    modified: modifiedLines.join("\n"),
  };
}
