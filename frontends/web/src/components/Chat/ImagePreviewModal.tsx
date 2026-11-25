import { RotateCcw, X } from "lucide-react";
import Tooltip from "../forge-ui/tooltip";
import { useEffect, useRef, useCallback, useState } from "react";
import { createPortal } from "react-dom";
import { focusChatInput } from "../../hooks/useFocusManager";
import { toast } from "../../lib/toast-manager";
import { MarkupEditor, type EditorMode, type MarkupEditorHandle } from "./imageMarkup/MarkupEditor";
import { hasMarkup, pinsOf, type CommentPin, type MarkupDoc } from "./imageMarkup/model";
import { flattenImage } from "./imageMarkup/renderCanvas";

/** What Save hands back: the flattened image, and what it was made from so it can be re-edited. */
export interface ImageMarkupResult {
  /** The source with the markup (and crop) burned in. */
  file: File;
  doc: MarkupDoc;
  /** The un-annotated original the doc was drawn over. */
  source: Blob;
  /** The doc's comment pins, by number. */
  comments: CommentPin[];
}

/** Present: the preview can mark the image up. Absent: the preview is read-only. */
export interface ImageEditOptions {
  /** The un-annotated original. Always edit from this, never from a flattened image. */
  source: Blob;
  /** The markup saved earlier, to keep editing it. */
  initialDoc?: MarkupDoc;
  /** Rejecting keeps the editor open with the doc intact (the caller reports the error). */
  onSave: (result: ImageMarkupResult) => Promise<void>;
  /** Present when the image carries markup: puts the original back. */
  onRevert?: () => Promise<void>;
}

interface ImagePreviewModalProps {
  isOpen: boolean;
  onClose: () => void;
  imageUrl: string;
  filename: string;
  edit?: ImageEditOptions;
}

/** Whether `el` takes typing, so editing shortcuts must leave its keys alone. */
function isTextField(el: Element | null): boolean {
  if (!el) return false;
  if (el instanceof HTMLElement && el.isContentEditable) return true;
  return el.tagName === "INPUT" || el.tagName === "TEXTAREA" || el.tagName === "SELECT";
}

/** A blob URL for `blob`, revoked when it changes or the caller unmounts. */
function useObjectUrl(blob: Blob | undefined): string | undefined {
  const [url, setUrl] = useState<string>();
  useEffect(() => {
    if (!blob) {
      setUrl(undefined);
      return;
    }
    const next = URL.createObjectURL(blob);
    setUrl(next);
    return () => URL.revokeObjectURL(next);
  }, [blob]);
  return url;
}

export function ImagePreviewModal({
  isOpen,
  onClose,
  imageUrl,
  filename,
  edit,
}: ImagePreviewModalProps) {
  const wasOpenRef = useRef(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const editorRef = useRef<MarkupEditorHandle>(null);
  const [mode, setMode] = useState<EditorMode>("view");
  const [dirty, setDirty] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);
  const [reverting, setReverting] = useState(false);
  // The editor shows the original, never the flattened copy the chip shows.
  const sourceUrl = useObjectUrl(edit?.source);

  // Stable close handler
  const handleClose = useCallback(() => {
    onClose();
  }, [onClose]);

  // Closing with unsaved markup asks first; nothing else does.
  const requestClose = useCallback(() => {
    if (edit && dirty) setConfirmDiscard(true);
    else handleClose();
  }, [edit, dirty, handleClose]);

  // Focus the container when the modal opens, so screen readers land inside the
  // dialog and the scroll container responds to arrow keys. Escape no longer
  // depends on this: see the listener below for why focus was never the issue.
  useEffect(() => {
    if (isOpen && containerRef.current) {
      containerRef.current.focus();
    }
  }, [isOpen]);

  // Keys, read through a ref so the listener below is registered once per
  // open and still sees the current mode and editor state.
  const onKeyRef = useRef<(e: KeyboardEvent) => void>(() => {});
  onKeyRef.current = (e: KeyboardEvent) => {
    const active = document.activeElement;
    // Focus in a portalled panel the editor opened (the colour or shape
    // popover) — that panel owns its keys, Escape included: its own
    // window-capture listener registers after this one, so this one must step
    // aside rather than claim the key first.
    if (edit && active && active !== document.body && !containerRef.current?.contains(active)) return;

    const claim = () => {
      e.preventDefault();
      // stopImmediatePropagation, not just stopPropagation: the global shortcut
      // dispatcher's listener is on `document`, and stopPropagation alone would
      // still let it run.
      e.stopPropagation();
      e.stopImmediatePropagation();
    };

    if (e.key === "Escape") {
      claim();
      if (!edit) {
        handleClose();
        return;
      }
      // Innermost first: the discard prompt, the editor's own layer (a text
      // box, a pending pin, a selection, a drag), the mode, then the dialog.
      if (confirmDiscard) setConfirmDiscard(false);
      else if (editorRef.current?.cancel()) return;
      else if (mode !== "view") setMode("view");
      else requestClose();
      return;
    }

    if (!edit || confirmDiscard || isTextField(active)) return;
    const mod = e.metaKey || e.ctrlKey;
    const key = e.key.toLowerCase();
    if (mod && key === "z") {
      claim();
      if (e.shiftKey) editorRef.current?.redo();
      else editorRef.current?.undo();
    } else if (mod && key === "y" && !e.metaKey) {
      claim();
      editorRef.current?.redo();
    } else if ((e.key === "Delete" || e.key === "Backspace") && mode === "markup") {
      if (editorRef.current?.deleteSelection()) claim();
    }
  };

  useEffect(() => {
    if (!isOpen) return;

    const handleKeyDown = (e: KeyboardEvent) => onKeyRef.current(e);

    // `window`, capture phase — and the node matters more than the phase.
    //
    // This modal previously registered Escape at `document` in capture phase AND
    // as a React onKeyDown on the focused container, and NEITHER fired. The
    // global shortcut dispatcher (useAppKeyboardShortcuts -> lib/keyboard) owns
    // Escape from a single capture-phase listener on `document`, registered when
    // the keyboard host goes live — long before any preview opens. Because this overlay
    // renders `data-modal-open="true"`, detectActiveContexts puts the dispatcher
    // in the `modal` context, where Escape resolves to the non-passthrough
    // `dismissModal` shortcut, so the dispatcher calls stopImmediatePropagation.
    // That killed the modal's own document-capture listener (same node,
    // registered later) and stopped the event before the bubble phase ever
    // reached React's onKeyDown. Both paths were dead by construction, and the
    // dispatcher's `onDismissModal` only closes the overlays whose open state
    // ModernApp itself holds — which does not include this one.
    //
    // Capture descends window -> document -> ... , so a window-capture listener
    // is ordered ahead of the dispatcher rather than racing it by mount order.
    // Claiming the event here also correctly suppresses the `stopStreaming`
    // binding, so Escape dismisses the top-most overlay instead of pausing the
    // chat behind it. The editor's undo/redo/delete keys are claimed here for
    // the same reason: a React handler would never see them.
    window.addEventListener("keydown", handleKeyDown, true);
    // Restore what was there, not "unset": the preview often opens over a
    // dialog that holds its own scroll lock, and clearing it would let the
    // page scroll behind that dialog once the preview closes.
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";

    return () => {
      window.removeEventListener("keydown", handleKeyDown, true);
      document.body.style.overflow = previousOverflow;
    };
  }, [isOpen]);

  // Restore focus to chat input when modal closes
  useEffect(() => {
    if (isOpen) {
      wasOpenRef.current = true;
    } else if (wasOpenRef.current) {
      wasOpenRef.current = false;
      focusChatInput();
    }
  }, [isOpen]);

  const handleSave = useCallback(
    async (doc: MarkupDoc) => {
      if (!edit) return;
      try {
        if (!hasMarkup(doc)) {
          // Everything was undone or erased: the original is what remains.
          if (edit.initialDoc && hasMarkup(edit.initialDoc) && edit.onRevert) await edit.onRevert();
        } else {
          const file = await flattenImage(edit.source, doc, filename);
          await edit.onSave({ file, doc, source: edit.source, comments: pinsOf(doc) });
        }
      } catch (error) {
        toast.error(`Could not save ${filename}: ${error instanceof Error ? error.message : String(error)}`, {
          duration: 6000,
        });
        throw error;
      }
      handleClose();
    },
    [edit, filename, handleClose],
  );

  const handleRevert = useCallback(async () => {
    if (!edit?.onRevert || reverting) return;
    setReverting(true);
    try {
      await edit.onRevert();
      handleClose();
    } catch (error) {
      toast.error(`Could not revert ${filename}: ${error instanceof Error ? error.message : String(error)}`, {
        duration: 6000,
      });
    } finally {
      setReverting(false);
    }
  }, [edit, filename, handleClose, reverting]);

  if (!isOpen) return null;

  const modalContent = (
    <div
      ref={containerRef}
      tabIndex={-1}
      data-modal-open="true"
      role="dialog"
      aria-modal="true"
      aria-label={filename}
      className="fixed inset-0 z-[9999] flex flex-col bg-black outline-none"
    >
      {/* Top bar with close button */}
      <div className="flex-shrink-0 flex items-center px-4 py-3 bg-black border-b border-white/10 relative">
        {/* Spacer for centering */}
        <div className={edit?.onRevert ? "w-32" : "w-8"} />
        
        {/* Centered title */}
        <div className="flex-1 text-center">
          <span className="text-white text-sm font-medium truncate">
            {filename}
          </span>
        </div>

        {edit?.onRevert && (
          <button
            type="button"
            onClick={() => void handleRevert()}
            disabled={reverting}
            className="mr-2 inline-flex flex-shrink-0 items-center gap-1.5 rounded-md px-2 py-1.5 text-xs text-white/80 transition-colors hover:bg-white/10 hover:text-white disabled:opacity-50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-11"
          >
            <RotateCcw className="h-3.5 w-3.5" aria-hidden />
            Revert to original
          </button>
        )}
        
        {/* Close button */}
        <Tooltip content="Close (ESC)" side="bottom" delay={300} triggerClassName="inline-flex flex-shrink-0">
<button
          onClick={requestClose}
          className="p-1.5 hover:bg-white/10 rounded-md transition-colors flex-shrink-0" aria-label="Close (ESC)">
          <X className="w-4 h-4 text-white" />
        </button>
</Tooltip>
      </div>

      {edit && sourceUrl ? (
        <div className="relative flex-1 min-h-0">
          <MarkupEditor
            key={sourceUrl}
            imageUrl={sourceUrl}
            initialDoc={edit.initialDoc}
            mode={mode}
            onModeChange={setMode}
            onSave={handleSave}
            onDirtyChange={setDirty}
            editorRef={editorRef}
            alt={filename}
          />
          {confirmDiscard && (
            <DiscardPrompt
              onKeep={() => setConfirmDiscard(false)}
              onDiscard={() => {
                setConfirmDiscard(false);
                handleClose();
              }}
            />
          )}
        </div>
      ) : (
        /* Image area - click to close */
        <div 
          className="flex-1 flex items-center justify-center overflow-auto p-8 cursor-pointer"
          onClick={onClose}
        >
          <img
            src={imageUrl}
            alt={filename}
            className="max-w-full max-h-full object-contain"
            onClick={(e) => e.stopPropagation()}
          />
        </div>
      )}
    </div>
  );

  return createPortal(modalContent, document.body);
}

/**
 * "Discard your markup?" — inside the dialog rather than a second portalled
 * dialog, so focus and Escape stay with the preview that asked.
 */
function DiscardPrompt({ onKeep, onDiscard }: { onKeep: () => void; onDiscard: () => void }) {
  const keepRef = useRef<HTMLButtonElement>(null);
  useEffect(() => {
    keepRef.current?.focus();
  }, []);
  return (
    <div className="absolute inset-0 z-10 flex items-center justify-center bg-black/60 p-4">
      <div
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="image-markup-discard-title"
        className="w-full max-w-sm rounded-xl border border-white/15 bg-black/85 p-5 text-white shadow-lg backdrop-blur"
      >
        <h2 id="image-markup-discard-title" className="text-sm font-medium">
          Discard your changes?
        </h2>
        <p className="mt-1.5 text-xs text-white/70">Your markup and comments on this image have not been saved.</p>
        <div className="mt-4 flex justify-end gap-2">
          <button
            ref={keepRef}
            type="button"
            onClick={onKeep}
            className="rounded-md px-3 py-1.5 text-xs text-white/80 hover:bg-white/10 hover:text-white focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-11"
          >
            Keep editing
          </button>
          <button
            type="button"
            onClick={onDiscard}
            className="rounded-md bg-danger px-3 py-1.5 text-xs font-medium text-white hover:bg-danger-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:h-11"
          >
            Discard
          </button>
        </div>
      </div>
    </div>
  );
}
