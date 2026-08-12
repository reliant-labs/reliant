import { useEffect, useState, useRef, useCallback } from "react";
import {
  Save,
  Loader2,
  AlertCircle,
  RotateCcw,
  RefreshCw,
  Music,
  Check,
} from "lucide-react";
import Editor from "@monaco-editor/react";
import type { FileNode } from "./index";
import {
  getFileContent,
  saveFileContent,
  getFilePreviewInfo,
  getFilePreviewBlob,
  type FilePreviewInfo,
} from "../../api/fileSystem";
import { cn } from "../../lib/utils";
import { useEditorStore } from "../../store/editorStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import {
  getMonacoLanguage,
  configureMonacoTheme,
  getCurrentMonacoTheme,
  MONACO_FONT_FAMILY,
} from "../../lib/monacoTheme";
import { getRelativePath } from "../../lib/fileUtils";
import { isDaemonConnectingError } from "../../lib/daemon-errors";
import { sendWithDaemonWait } from "../../lib/daemon-retry";
import { DaemonWaitState } from "../DaemonWaitState";
import { useDaemonWait } from "../../hooks/useDaemonWait";
import { AddToChatPopup } from "./AddToChatPopup";
import { ImagePreviewModal } from "../ui/ImagePreviewModal";
import { FileIcon } from "../ui/FileIcon";
import { toast } from "sonner";

interface FileViewerTabProps {
  file: FileNode;
  worktreeId?: string;
  isActive?: boolean;
  viewerId?: string;
  embedded?: boolean;
}

function getPreviewKindLabel(viewerKind: FilePreviewInfo["viewerKind"]): string {
  switch (viewerKind) {
    case "text":
      return "Text";
    case "image":
      return "Image Preview";
    case "pdf":
    case "binary":
      return "Binary File";
    case "audio":
      return "Audio Preview";
    case "video":
      return "Video Preview";
    default:
      return "Binary File";
  }
}

export function FileViewerTab({ file, worktreeId, isActive, viewerId, embedded = false }: FileViewerTabProps) {
  const settings = useEditorStore((state) => state.settings);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const worktrees = useWorktreeStore((state) => state.worktrees);

  const effectiveWorktreeId = worktreeId || currentWorktree?.id;
  const effectiveWorktree = effectiveWorktreeId
    ? worktrees.find((w) => w.id === effectiveWorktreeId) || currentWorktree
    : currentWorktree;

  const displayPath = getRelativePath(file.path, effectiveWorktree?.path);

  const [previewInfo, setPreviewInfo] = useState<FilePreviewInfo | null>(null);
  const [content, setContent] = useState<string>("");
  const [originalContent, setOriginalContent] = useState<string>("");
  const [previewUrl, setPreviewUrl] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [showLoadingSpinner, setShowLoadingSpinner] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Blocked on the machine rather than broken — rendered as a wait, not an error.
  const [waitingOnDaemon, setWaitingOnDaemon] = useState(false);
  const [isSaving, setIsSaving] = useState(false);
  const [saveSuccess, setSaveSuccess] = useState(false);
  const [showImageModal, setShowImageModal] = useState(false);

  const editorRef = useRef<any>(null);
  const monacoRef = useRef<any>(null);
  const autoSaveTimerRef = useRef<NodeJS.Timeout | undefined>(undefined);
  const loadingTimeoutRef = useRef<number | null>(null);
  const decorationsRef = useRef<string[]>([]);
  const popupContainerRef = useRef<HTMLDivElement>(null);
  const previewFocusRef = useRef<HTMLDivElement>(null);
  const previewUrlRef = useRef<string | null>(null);
  const loadRequestIdRef = useRef(0);

  const [showAddToChatPopup, setShowAddToChatPopup] = useState(false);
  const [popupPosition, setPopupPosition] = useState({ x: 0, y: 0 });
  const [selectedRange, setSelectedRange] = useState<{
    startLine: number;
    endLine: number;
  } | null>(null);

  const isTextPreview =
    previewInfo?.viewerKind === "text" || previewInfo?.viewerKind === "pdf";
  const isEditableTextPreview =
    previewInfo?.viewerKind === "text" && previewInfo?.isEditable !== false;
  const hasChanges = isEditableTextPreview && content !== originalContent;

  const revokePreviewUrl = useCallback(() => {
    if (previewUrlRef.current) {
      URL.revokeObjectURL(previewUrlRef.current);
      previewUrlRef.current = null;
    }
    setPreviewUrl(null);
  }, []);

  const clearLoadingTimer = useCallback(() => {
    if (loadingTimeoutRef.current !== null) {
      window.clearTimeout(loadingTimeoutRef.current);
      loadingTimeoutRef.current = null;
    }
  }, []);

  const mapLoadError = useCallback((err: unknown) => {
    if (!(err instanceof Error)) {
      return "Failed to load file";
    }

    const errorMessage = err.message.toLowerCase();
    if (errorMessage.includes("404") || errorMessage.includes("not found")) {
      return "File not found. It may have been deleted or moved.";
    }
    if (errorMessage.includes("403") || errorMessage.includes("forbidden")) {
      return "Access denied. You don't have permission to view this file.";
    }
    if (errorMessage.includes("415") || errorMessage.includes("unsupported media type")) {
      return `Cannot preview ${file.name}. This file type is not supported for inline viewing.`;
    }

    return err.message;
  }, [file.name]);

  const loadPreview = useCallback(async () => {
    const requestId = ++loadRequestIdRef.current;

    clearLoadingTimer();
    revokePreviewUrl();
    setLoading(true);
    setShowLoadingSpinner(false);
    setError(null);
    setSaveSuccess(false);
    setShowImageModal(false);
    setPreviewInfo(null);
    setContent("");
    setOriginalContent("");
    setShowAddToChatPopup(false);
    setSelectedRange(null);

    loadingTimeoutRef.current = window.setTimeout(() => {
      if (loadRequestIdRef.current === requestId) {
        setShowLoadingSpinner(true);
      }
    }, 5000);

    try {
      const info = await getFilePreviewInfo(file.path, effectiveWorktreeId);
      if (loadRequestIdRef.current !== requestId) {
        return;
      }

      // Got a response, so the machine is serving again.
      setWaitingOnDaemon(false);
      setPreviewInfo(info);

      if (info.viewerKind === "text" || info.viewerKind === "pdf") {
        const fileContent = await getFileContent(file.path, effectiveWorktreeId);
        if (loadRequestIdRef.current !== requestId) {
          return;
        }

        setContent(fileContent);
        setOriginalContent(fileContent);
      } else if (
        info.viewerKind === "image" ||
        info.viewerKind === "audio" ||
        info.viewerKind === "video"
      ) {
        const blob = await getFilePreviewBlob(file.path, effectiveWorktreeId);
        if (loadRequestIdRef.current !== requestId) {
          return;
        }

        const objectUrl = URL.createObjectURL(blob);
        previewUrlRef.current = objectUrl;
        setPreviewUrl(objectUrl);
      }
    } catch (err) {
      if (loadRequestIdRef.current !== requestId) {
        return;
      }
      // A machine that isn't up yet is not a broken file. Route it to the
      // shared waiting state instead of `mapLoadError`, which would fall
      // through to the raw `[internal] unavailable: no daemon connected`.
      if (isDaemonConnectingError(err)) {
        setWaitingOnDaemon(true);
        return;
      }
      setWaitingOnDaemon(false);
      console.error("Failed to load file preview:", err);
      setError(mapLoadError(err));
    } finally {
      clearLoadingTimer();
      if (loadRequestIdRef.current === requestId) {
        setLoading(false);
        setShowLoadingSpinner(false);
      }
    }
  }, [
    clearLoadingTimer,
    effectiveWorktreeId,
    file.path,
    mapLoadError,
    revokePreviewUrl,
  ]);

  useEffect(() => {
    void loadPreview();

    return () => {
      loadRequestIdRef.current += 1;
      clearLoadingTimer();
      revokePreviewUrl();
    };
  }, [clearLoadingTimer, loadPreview, revokePreviewUrl]);

  // Re-open the file on the shared cadence while the machine comes up, so the
  // editor fills itself in rather than making the user close and reopen the tab.
  const daemonWait = useDaemonWait({
    waiting: waitingOnDaemon,
    onRetry: useCallback(() => {
      void loadPreview();
    }, [loadPreview]),
  });

  useEffect(() => {
    if (!isTextPreview) {
      setShowAddToChatPopup(false);
      setSelectedRange(null);
    }
  }, [isTextPreview]);

  useEffect(() => {
    if (!isTextPreview || loading || error || !editorRef.current || !monacoRef.current || !file.line) {
      return;
    }

    const timeoutId = window.setTimeout(() => {
      if (!editorRef.current || !monacoRef.current) {
        return;
      }

      editorRef.current.setPosition({
        lineNumber: file.line,
        column: file.column || 1,
      });
      editorRef.current.revealLineInCenter(file.line);

      if (file.line && file.lineEnd && file.lineEnd > file.line) {
        decorationsRef.current = editorRef.current.deltaDecorations(
          decorationsRef.current,
          [
            {
              range: new monacoRef.current.Range(file.line, 1, file.lineEnd, 1),
              options: {
                isWholeLine: true,
                className: "line-highlight",
                linesDecorationsClassName: "line-highlight-gutter",
              },
            },
          ]
        );
      }
    }, 100);

    return () => window.clearTimeout(timeoutId);
  }, [error, file.column, file.line, file.lineEnd, isTextPreview, loading]);

  useEffect(() => {
    if (!viewerId) return;

    const handleFocus = (event: CustomEvent<{ viewerId: string }>) => {
      if (event.detail.viewerId !== viewerId) {
        return;
      }

      if (isTextPreview && editorRef.current) {
        editorRef.current.focus();
      } else {
        previewFocusRef.current?.focus();
      }
    };

    window.addEventListener("file-viewer-focus", handleFocus as EventListener);
    return () => {
      window.removeEventListener("file-viewer-focus", handleFocus as EventListener);
    };
  }, [isTextPreview, viewerId]);

  useEffect(() => {
    if (!isActive) return;

    const handleKeyDown = () => {
      const activeElement = document.activeElement;
      const isMonacoFocused =
        activeElement?.closest(".monaco-editor") !== null ||
        activeElement?.classList.contains("inputarea") ||
        activeElement?.closest(".monaco-editor .inputarea") !== null;

      if (!isMonacoFocused || !editorRef.current) return;

      // Let Monaco handle all arrow keys - don't intercept for file tree navigation.
    };

    window.addEventListener("keydown", handleKeyDown, true);
    return () => {
      window.removeEventListener("keydown", handleKeyDown, true);
    };
  }, [isActive]);

  useEffect(() => {
    const handleThemeChange = () => {
      if (monacoRef.current && editorRef.current) {
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

  useEffect(() => {
    const handleScrollEvent = (event: Event) => {
      if (!isTextPreview || !editorRef.current || !monacoRef.current) {
        return;
      }

      const customEvent = event as CustomEvent;
      if (!customEvent.detail?.line) {
        return;
      }

      editorRef.current.setPosition({
        lineNumber: customEvent.detail.line,
        column: customEvent.detail.column || 1,
      });
      editorRef.current.revealLineInCenter(customEvent.detail.line);

      if (customEvent.detail.lineEnd && customEvent.detail.lineEnd > customEvent.detail.line) {
        decorationsRef.current = editorRef.current.deltaDecorations(
          decorationsRef.current,
          [
            {
              range: new monacoRef.current.Range(
                customEvent.detail.line,
                1,
                customEvent.detail.lineEnd,
                1
              ),
              options: {
                isWholeLine: true,
                className: "line-highlight",
                linesDecorationsClassName: "line-highlight-gutter",
              },
            },
          ]
        );
      }
    };

    window.addEventListener("file-viewer-scroll-to-line", handleScrollEvent);
    return () => {
      window.removeEventListener("file-viewer-scroll-to-line", handleScrollEvent);
    };
  }, [isTextPreview]);

  useEffect(() => {
    if (isActive && !loading && !error) {
      const timeoutId = window.setTimeout(() => {
        if (isTextPreview && editorRef.current) {
          editorRef.current.focus();
        } else {
          previewFocusRef.current?.focus();
        }
      }, 50);

      return () => window.clearTimeout(timeoutId);
    }
  }, [error, isActive, isTextPreview, loading]);

  const handleSave = useCallback(async () => {
    if (!isEditableTextPreview || content === originalContent) return;

    setIsSaving(true);
    setError(null);

    try {
      // Retry across a machine that's still coming up rather than failing the
      // save. The buffer is not cleared until this resolves, so the user's
      // edits survive; failing fast here would show "Failed to save file" for
      // a machine that was about to accept it.
      await sendWithDaemonWait({
        action: () => saveFileContent(file.path, content, effectiveWorktreeId),
        onWaiting: () =>
          toast.info("Your machine is starting — this file will save automatically."),
      });
      setOriginalContent(content);
      setSaveSuccess(true);
      window.setTimeout(() => setSaveSuccess(false), 2000);
    } catch (err) {
      setError(
        isDaemonConnectingError(err)
          ? "Your machine didn't come online, so this file wasn't saved. Your changes are still here — try again."
          : err instanceof Error
            ? err.message
            : "Failed to save file",
      );
    } finally {
      setIsSaving(false);
    }
  }, [content, effectiveWorktreeId, file.path, isEditableTextPreview, originalContent]);

  useEffect(() => {
    if (!settings.autoSave || !hasChanges) return;

    if (autoSaveTimerRef.current) {
      clearTimeout(autoSaveTimerRef.current);
    }

    autoSaveTimerRef.current = setTimeout(() => {
      void handleSave();
    }, settings.autoSaveDelay);

    return () => {
      if (autoSaveTimerRef.current) {
        clearTimeout(autoSaveTimerRef.current);
      }
    };
  }, [handleSave, hasChanges, settings.autoSave, settings.autoSaveDelay]);

  const handleRevert = useCallback(() => {
    if (!isEditableTextPreview) return;

    setContent(originalContent);
    if (editorRef.current) {
      editorRef.current.setValue(originalContent);
    }
  }, [isEditableTextPreview, originalContent]);

  const handleAddToChat = useCallback(async (selection?: any) => {
    if (!isTextPreview || !editorRef.current || !content) return;

    const sel = selection || editorRef.current.getSelection();
    if (!sel || sel.isEmpty()) {
      toast.error("Please select some text first");
      return;
    }

    const startLine = sel.startLineNumber;
    const endLine = sel.endLineNumber;
    const fileName = file.path.split("/").pop() || file.path;
    const language = editorRef.current?.getModel()?.getLanguageId() || "";
    const marker = `[[${file.path}:${startLine}-${endLine}]]`;

    window.dispatchEvent(
      new CustomEvent("add-context-marker", {
        detail: { marker, filePath: file.path, fileName, startLine, endLine, language },
      })
    );

    toast.success(
      `Added ${fileName} (${startLine}${endLine !== startLine ? `-${endLine}` : ""}) to chat`
    );

    editorRef.current.setSelection({
      startLineNumber: startLine,
      startColumn: 1,
      endLineNumber: startLine,
      endColumn: 1,
    });
    setShowAddToChatPopup(false);
    setSelectedRange(null);

    requestAnimationFrame(() => {
      window.dispatchEvent(new CustomEvent("focus-chat-input"));
    });
  }, [content, file.path, isTextPreview]);

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key === "s") {
        const activeElement = document.activeElement;
        const isMonacoFocused =
          activeElement?.closest(".monaco-editor") !== null ||
          activeElement?.classList.contains("inputarea") ||
          activeElement?.closest(".monaco-editor .inputarea") !== null;

        if (isMonacoFocused && hasChanges) {
          e.preventDefault();
          e.stopPropagation();
          e.stopImmediatePropagation();
          void handleSave();
        }
      }
    };

    document.addEventListener("keydown", handleKeyDown, true);
    return () => document.removeEventListener("keydown", handleKeyDown, true);
  }, [handleSave, hasChanges]);

  useEffect(() => {
    const handleCmdL = (e: Event) => {
      if (!isTextPreview) {
        return;
      }

      const customEvent = e as CustomEvent<{ originalEvent: KeyboardEvent }>;
      const originalEvent = customEvent.detail?.originalEvent;
      if (!originalEvent || !editorRef.current) return;

      const activeElement = document.activeElement;
      const isMonacoFocused =
        activeElement?.closest(".monaco-editor") !== null ||
        activeElement?.classList.contains("inputarea") ||
        activeElement?.closest(".monaco-editor .inputarea") !== null;

      if (!isMonacoFocused) {
        return;
      }

      const selection = editorRef.current.getSelection();
      if (selection && !selection.isEmpty()) {
        originalEvent.preventDefault();
        originalEvent.stopPropagation();
        originalEvent.stopImmediatePropagation();
        e.preventDefault();
        void handleAddToChat(selection);
        setTimeout(() => {
          window.dispatchEvent(new CustomEvent("focus-chat-input"));
        }, 100);
      }
    };

    window.addEventListener("cmd-l-in-monaco", handleCmdL as EventListener);
    return () => window.removeEventListener("cmd-l-in-monaco", handleCmdL as EventListener);
  }, [handleAddToChat, isTextPreview]);

  const handleEditorDidMount = (editor: any, monaco: any) => {
    editorRef.current = editor;
    monacoRef.current = monaco;

    configureMonacoTheme(monaco);
    const themeName = getCurrentMonacoTheme();
    monaco.editor.setTheme(themeName);

    editor.addAction({
      id: "reliant.closeFindWidget",
      label: "Close Find Widget",
      keybindings: [monaco.KeyCode.Escape],
      run: () => {
        editor.trigger("reliant", "closeFindWidget", {});
      },
    });

    editor.addAction({
      id: "reliant.startFindAction",
      label: "Find",
      keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyF],
      run: () => {
        editor.getAction("actions.find")?.run();
      },
    });

    editor.addCommand(
      window.navigator.platform.includes("Mac") ? 2097 : 2048,
      () => {
        if (hasChanges) {
          void handleSave();
        }
      }
    );

    editor.addAction({
      id: "reliant.selectLineLeft",
      label: "Select Entire Line (Left)",
      keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.ArrowLeft],
      run: () => {
        const position = editor.getPosition();
        if (!position) return;

        const lineNumber = position.lineNumber;
        const model = editor.getModel();
        if (!model) return;

        const lineContent = model.getLineContent(lineNumber);
        editor.setSelection({
          startLineNumber: lineNumber,
          startColumn: 1,
          endLineNumber: lineNumber,
          endColumn: lineContent.length + 1,
        });
      },
    });

    editor.addAction({
      id: "reliant.selectLineRight",
      label: "Select Entire Line (Right)",
      keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.ArrowRight],
      run: () => {
        const position = editor.getPosition();
        if (!position) return;

        const lineNumber = position.lineNumber;
        const model = editor.getModel();
        if (!model) return;

        const lineContent = model.getLineContent(lineNumber);
        editor.setSelection({
          startLineNumber: lineNumber,
          startColumn: 1,
          endLineNumber: lineNumber,
          endColumn: lineContent.length + 1,
        });
      },
    });

    editor.addAction({
      id: "reliant.selectToTop",
      label: "Select to Top of File",
      keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.ArrowUp],
      run: () => {
        const selection = editor.getSelection();
        if (!selection) return;

        editor.setSelection({
          startLineNumber: 1,
          startColumn: 1,
          endLineNumber: selection.endLineNumber,
          endColumn: selection.endColumn,
        });
      },
    });

    editor.addAction({
      id: "reliant.selectToBottom",
      label: "Select to Bottom of File",
      keybindings: [monaco.KeyMod.CtrlCmd | monaco.KeyMod.Shift | monaco.KeyCode.ArrowDown],
      run: () => {
        const selection = editor.getSelection();
        const model = editor.getModel();
        if (!selection || !model) return;

        const lineCount = model.getLineCount();
        const lastLineContent = model.getLineContent(lineCount);
        editor.setSelection({
          startLineNumber: selection.startLineNumber,
          startColumn: selection.startColumn,
          endLineNumber: lineCount,
          endColumn: lastLineContent.length + 1,
        });
      },
    });

    let selectionTimeout: NodeJS.Timeout | null = null;
    let currentRange: { startLine: number; endLine: number } | null = null;

    const updatePopupPosition = (range: { startLine: number; endLine: number } | null) => {
      if (!editorRef.current || !range) {
        setShowAddToChatPopup(false);
        setSelectedRange(null);
        return;
      }

      const editorInstance = editorRef.current;
      const model = editorInstance.getModel();
      if (!model) return;

      const { startLine, endLine } = range;
      const visibleRanges = editorInstance.getVisibleRanges();
      const isLineVisible = visibleRanges.some(
        (visibleRange: { startLineNumber: number; endLineNumber: number }) =>
          startLine >= visibleRange.startLineNumber && startLine <= visibleRange.endLineNumber
      );

      if (!isLineVisible) {
        setShowAddToChatPopup(false);
        return;
      }

      const lastLineContent = model.getLineContent(endLine);
      const endCoords = editorInstance.getScrolledVisiblePosition({
        lineNumber: endLine,
        column: lastLineContent.length + 1,
      });
      const startCoords = editorInstance.getScrolledVisiblePosition({
        lineNumber: startLine,
        column: 1,
      });

      if (!endCoords || !startCoords) {
        setShowAddToChatPopup(false);
        return;
      }

      const container = editorInstance.getContainerDomNode();
      const containerRect = container.getBoundingClientRect();
      const lineHeight = editorInstance.getOption(monaco.editor.EditorOption.lineHeight);
      const layoutInfo = editorInstance.getLayoutInfo();
      const decorationsWidth = layoutInfo.decorationsWidth;
      const fileViewerContainer = popupContainerRef.current;
      if (!fileViewerContainer) return;

      const fileViewerRect = fileViewerContainer.getBoundingClientRect();
      const toolbar = fileViewerContainer.querySelector(".border-b") as HTMLElement | null;
      const toolbarRect = toolbar?.getBoundingClientRect();
      const toolbarBottom = toolbarRect ? toolbarRect.bottom : fileViewerRect.top;

      let parent: HTMLElement | null = fileViewerContainer.parentElement;
      let tabsHeader: HTMLElement | null = null;
      while (parent && !tabsHeader) {
        const header = parent.querySelector(".border-b") as HTMLElement | null;
        if (header && header.querySelector(".flex.items-center.overflow-x-auto")) {
          tabsHeader = header;
          break;
        }
        parent = parent.parentElement;
      }
      const tabsHeaderBottom = tabsHeader
        ? tabsHeader.getBoundingClientRect().bottom
        : fileViewerRect.top;

      const popupWidth = 200;
      const popupHeight = 32;
      const padding = 20;
      const rightOfSelectionX =
        containerRect.left + decorationsWidth + endCoords.left + padding;
      const availableSpaceRight = fileViewerRect.right - rightOfSelectionX;
      const hasSpaceToRight = availableSpaceRight >= popupWidth;

      let x: number;
      let y: number;
      if (hasSpaceToRight) {
        x = rightOfSelectionX;
        y = containerRect.top + startCoords.top;
      } else {
        x = containerRect.left + decorationsWidth + startCoords.left;
        y = containerRect.top + startCoords.top + lineHeight + 4;
      }

      const minY = Math.max(tabsHeaderBottom, toolbarBottom) + 4;
      y = Math.max(minY, y);
      x = Math.max(
        fileViewerRect.left + decorationsWidth,
        Math.min(x, fileViewerRect.right - popupWidth)
      );
      y = Math.min(y, fileViewerRect.bottom - popupHeight);

      setPopupPosition({ x, y });
      setShowAddToChatPopup(true);
    };

    editor.onDidChangeCursorSelection(() => {
      if (selectionTimeout) {
        clearTimeout(selectionTimeout);
      }

      const selection = editor.getSelection();
      if (selection && !selection.isEmpty()) {
        currentRange = {
          startLine: selection.startLineNumber,
          endLine: selection.endLineNumber,
        };
        setSelectedRange(currentRange);
        selectionTimeout = setTimeout(() => updatePopupPosition(currentRange), 150);
      } else {
        currentRange = null;
        setShowAddToChatPopup(false);
        setSelectedRange(null);
      }
    });

    editor.onDidScrollChange(() => {
      if (currentRange) {
        updatePopupPosition(currentRange);
      }
    });

    editor.onMouseMove(() => {
      // Popup visibility is managed by selection changes.
    });

    const isMac =
      typeof window !== "undefined" &&
      (window.navigator.platform.toUpperCase().includes("MAC") ||
        window.navigator.userAgent.toUpperCase().includes("MAC"));

    if (!isMac) {
      editor.addAction({
        id: "reliant.ignoreCtrlAltArrow",
        label: "Ignore Ctrl+Alt+Arrow",
        keybindings: [
          monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.ArrowUp,
          monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.ArrowDown,
          monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.ArrowLeft,
          monaco.KeyMod.CtrlCmd | monaco.KeyMod.Alt | monaco.KeyCode.ArrowRight,
        ],
        run: () => {},
      });
    }

    if (file.line) {
      editor.setPosition({
        lineNumber: file.line,
        column: file.column || 1,
      });
      editor.revealLineInCenter(file.line);

      if (file.lineEnd && file.lineEnd > file.line) {
        decorationsRef.current = editor.deltaDecorations(decorationsRef.current, [
          {
            range: new monaco.Range(file.line, 1, file.lineEnd, 1),
            options: {
              isWholeLine: true,
              className: "line-highlight",
              linesDecorationsClassName: "line-highlight-gutter",
            },
          },
        ]);
      }
    }
  };

  useEffect(() => {
    if (editorRef.current) {
      editorRef.current.updateOptions({
        minimap: { enabled: settings.minimap },
        fontSize: settings.fontSize,
        fontFamily: MONACO_FONT_FAMILY,
        lineNumbers: settings.lineNumbers ? "on" : "off",
        wordWrap: settings.wordWrap ? "on" : "off",
        renderWhitespace: settings.renderWhitespace ? "all" : "none",
        bracketPairColorization: { enabled: settings.bracketPairColorization },
        guides: {
          bracketPairs: settings.guides,
          indentation: settings.guides,
        },
        cursorBlinking: settings.cursorBlinking,
        cursorSmoothCaretAnimation: settings.cursorSmoothCaretAnimation ? "on" : "off",
        renderLineHighlight: settings.renderLineHighlight,
        tabSize: settings.tabSize,
        suggest: {
          enabled: settings.quickSuggestions,
        },
        quickSuggestions: settings.quickSuggestions
          ? {
              other: true,
              comments: true,
              strings: true,
            }
          : false,
        acceptSuggestionOnEnter: settings.acceptSuggestionOnEnter ? "on" : "off",
        suggestOnTriggerCharacters: settings.suggestOnTriggerCharacters,
      });
    }
  }, [settings]);

  const renderMainContent = () => {
    // Ahead of the loading branches: a machine that isn't up yet keeps the
    // request "in flight" indefinitely, and a bare spinner would never explain
    // why the file won't open.
    if (waitingOnDaemon && daemonWait.state) {
      return (
        <DaemonWaitState
          state={daemonWait.state}
          variant="panel"
          onRetry={daemonWait.retryNow}
        />
      );
    }

    if (loading && showLoadingSpinner) {
      return (
        <div className="flex items-center justify-center h-full">
          <div className="text-center space-y-2">
            <Loader2 className="w-8 h-8 animate-spin text-muted-foreground mx-auto" />
            <p className="text-sm text-muted-foreground">Loading preview...</p>
          </div>
        </div>
      );
    }

    if (loading && !showLoadingSpinner) {
      return <div className="flex-1 overflow-hidden" />;
    }

    if (error) {
      return (
        <div className="flex items-center justify-center h-full p-4">
          <div className="text-center space-y-3 max-w-md">
            <AlertCircle className="w-12 h-12 text-destructive mx-auto" />
            <p className="text-sm text-muted-foreground whitespace-pre-line">{error}</p>
            <button
              onClick={() => void loadPreview()}
              className="inline-flex items-center gap-1.5 text-xs text-primary hover:underline"
            >
              <RefreshCw className="w-3 h-3" />
              Try again
            </button>
          </div>
        </div>
      );
    }

    if (!previewInfo) {
      return null;
    }

    if (isTextPreview) {
      return (
        <Editor
          height="100%"
          language={getMonacoLanguage(file.name)}
          value={content}
          onChange={(value) => setContent(value || "")}
          onMount={handleEditorDidMount}
          theme={getCurrentMonacoTheme()}
          beforeMount={(monaco) => {
            configureMonacoTheme(monaco);
          }}
          loading={null}
          options={{
            readOnly: previewInfo.isEditable === false,
            minimap: { enabled: settings.minimap },
            fontSize: settings.fontSize,
            fontFamily: MONACO_FONT_FAMILY,
            lineNumbers: settings.lineNumbers ? "on" : "off",
            rulers: [],
            wordWrap: settings.wordWrap ? "on" : "off",
            automaticLayout: true,
            scrollBeyondLastLine: false,
            renderWhitespace: settings.renderWhitespace ? "all" : "none",
            bracketPairColorization: { enabled: settings.bracketPairColorization },
            guides: {
              bracketPairs: settings.guides,
              indentation: settings.guides,
            },
            cursorBlinking: settings.cursorBlinking,
            cursorSmoothCaretAnimation: settings.cursorSmoothCaretAnimation ? "on" : "off",
            renderLineHighlight: settings.renderLineHighlight,
            tabSize: settings.tabSize,
            quickSuggestions: settings.quickSuggestions
              ? {
                  other: true,
                  comments: true,
                  strings: true,
                }
              : false,
            acceptSuggestionOnEnter: settings.acceptSuggestionOnEnter ? "on" : "off",
            suggestOnTriggerCharacters: settings.suggestOnTriggerCharacters,
          }}
        />
      );
    }

    if (previewInfo.viewerKind === "binary") {
      return (
        <div
          ref={previewFocusRef}
          tabIndex={0}
          className="h-full overflow-auto p-6 outline-none"
        >
          <div className="flex items-center justify-center min-h-full">
            <div className="w-full max-w-2xl flex flex-col items-center justify-center gap-4 text-center">
              <div className="rounded-lg bg-muted/40 p-4">
                <FileIcon fileName={file.name} className="w-10 h-10" />
              </div>
              <div className="space-y-1">
                <p className="text-sm font-medium">{file.name}</p>
                <p className="text-sm text-muted-foreground">
                  This file type cannot be previewed inline.
                </p>
              </div>
            </div>
          </div>
        </div>
      );
    }

    const mediaPreview = previewInfo.viewerKind === "image" ? (
      previewUrl ? (
        <button
          type="button"
          className="w-full max-w-5xl bg-transparent p-0 flex items-center justify-center"
          onClick={() => setShowImageModal(true)}
        >
          <img
            src={previewUrl}
            alt={file.name}
            className="max-w-full max-h-[70vh] object-contain rounded-md"
          />
        </button>
      ) : null
    ) : previewInfo.viewerKind === "audio" && previewUrl ? (
      <div className="w-full max-w-xl flex flex-col items-center justify-center gap-4 min-h-[240px] px-4">
        <div className="rounded-full bg-muted/40 p-4">
          <Music className="w-8 h-8 text-muted-foreground" />
        </div>
        <audio controls src={previewUrl} className="w-full" preload="metadata" />
      </div>
    ) : previewInfo.viewerKind === "video" && previewUrl ? (
      <div className="w-full max-w-5xl p-4 bg-black/40 flex items-center justify-center min-h-[320px]">
        <video
          controls
          src={previewUrl}
          className="max-w-full max-h-[70vh] rounded-md bg-black"
          preload="metadata"
        />
      </div>
    ) : null;

    return (
      <div
        ref={previewFocusRef}
        tabIndex={0}
        className="h-full overflow-auto p-6 outline-none"
      >
        <div className="flex items-center justify-center min-h-full">
          <div className="w-full flex flex-col items-center">
            {mediaPreview}
          </div>
        </div>
      </div>
    );
  };

  return (
    <div className="flex flex-col h-full bg-background" ref={popupContainerRef}>
      {!embedded && (
        <div className="flex items-center justify-between px-4 py-2 border-b border-border bg-muted/20">
          <div className="flex items-center gap-3 flex-1 min-w-0">
            <span className="text-xs text-muted-foreground font-mono truncate">{displayPath}</span>
            {previewInfo && !isTextPreview && (
              <span className="text-[11px] px-2 py-0.5 rounded-full border border-border bg-background text-muted-foreground">
                {getPreviewKindLabel(previewInfo.viewerKind)}
              </span>
            )}
            {hasChanges && <span className="text-xs text-amber-500 font-medium">• Modified</span>}
          </div>
          <div className="flex items-center gap-2">
            {hasChanges && (
              <button
                onClick={handleRevert}
                className="flex items-center gap-1.5 px-2 py-1.5 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground hover:bg-destructive/10 hover:text-destructive"
                title="Undo all changes and revert to saved version"
              >
                <RotateCcw className="w-4 h-4" />
                <span className="text-xs">Undo</span>
              </button>
            )}
            {!settings.autoSave && hasChanges && (
              <button
                onClick={() => void handleSave()}
                disabled={isSaving}
                className={cn(
                  "flex items-center gap-1.5 px-3 py-1.5 rounded text-xs font-medium transition-colors",
                  "bg-primary text-primary-foreground hover:bg-primary/90"
                )}
                title="Save (Cmd+S / Ctrl+S)"
              >
                {isSaving ? (
                  <Loader2 className="w-3 h-3 animate-spin" />
                ) : saveSuccess ? (
                  <Check className="w-3 h-3 text-green-500" />
                ) : (
                  <Save className="w-3 h-3" />
                )}
                {isSaving ? "Saving..." : saveSuccess ? "Saved!" : "Save"}
              </button>
            )}
            {settings.autoSave && (isSaving || saveSuccess) && (
              <div className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium text-muted-foreground">
                {isSaving ? (
                  <>
                    <Loader2 className="w-3 h-3 animate-spin" />
                    <span>Auto-saving...</span>
                  </>
                ) : saveSuccess ? (
                  <>
                    <Check className="w-3 h-3 text-green-500" />
                    <span className="text-green-500">Auto-saved</span>
                  </>
                ) : null}
              </div>
            )}
          </div>
        </div>
      )}

      <div className="flex-1 overflow-hidden">{renderMainContent()}</div>

      {!embedded && (
        <AddToChatPopup
          visible={isTextPreview && showAddToChatPopup && selectedRange !== null}
          x={popupPosition.x}
          y={popupPosition.y}
          onAdd={() => void handleAddToChat()}
          shortcut={window.navigator.platform.includes("Mac") ? "⌘L" : "Ctrl+L"}
          container={popupContainerRef.current}
        />
      )}

      {!embedded && !loading && !error && previewInfo && (
        <div className="flex items-center justify-between px-4 py-2 border-t border-border bg-muted/20 text-xs font-mono text-muted-foreground">
          <div className="flex items-center gap-4 min-w-0">
            {isTextPreview ? (
              <span>{content.split("\n").length} lines</span>
            ) : (
              <span>{getPreviewKindLabel(previewInfo.viewerKind)}</span>
            )}
            <span>{formatFileSize(previewInfo.size)}</span>
            <span className="truncate">{previewInfo.mimeType}</span>
          </div>
          <span>Modified: {new Date(previewInfo.modified).toLocaleString()}</span>
        </div>
      )}

      {previewInfo?.viewerKind === "image" && previewUrl && (
        <ImagePreviewModal
          isOpen={showImageModal}
          onClose={() => setShowImageModal(false)}
          imageUrl={previewUrl}
          filename={file.name}
        />
      )}
    </div>
  );
}

function formatFileSize(bytes: number): string {
  if (bytes === 0) return "0 B";
  const k = 1024;
  const sizes = ["B", "KB", "MB", "GB"];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${parseFloat((bytes / Math.pow(k, i)).toFixed(1))} ${sizes[i]}`;
}