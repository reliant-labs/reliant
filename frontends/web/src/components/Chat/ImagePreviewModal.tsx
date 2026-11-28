import { ChevronLeft, ChevronRight, Copy, Download, Share2, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState, type TouchEvent } from "react";
import { createPortal } from "react-dom";
import { focusChatInput } from "../../hooks/useFocusManager";
import { cn } from "../../lib/utils";

interface ImagePreviewModalProps {
  isOpen: boolean;
  onClose: () => void;
  restoreFocus?: () => void;
  imageUrl: string;
  filename: string;
  imageBlob?: Blob;
  navigation?: { index: number; total: number; onSelect: (index: number) => void };
}

const controlClass = "inline-flex size-11 shrink-0 items-center justify-center rounded-md hover:bg-foreground/5 active:bg-foreground/10 disabled:opacity-40";

async function convertToPng(blob: Blob): Promise<Blob> {
  // Decode the bytes directly: loading a blob: URL in an Image can be blocked
  // by img-src even though the attachment is already available in memory.
  const image = await createImageBitmap(blob);
  try {
    const canvas = document.createElement("canvas");
    canvas.width = image.width;
    canvas.height = image.height;
    const context = canvas.getContext("2d");
    if (!context) throw new Error("Image conversion unavailable");
    context.drawImage(image, 0, 0);
    return await new Promise<Blob>((resolve, reject) => {
      canvas.toBlob((png) => png ? resolve(png) : reject(new Error("Could not encode PNG")), "image/png");
    });
  } finally {
    image.close();
  }
}

function ImageFileActions({ blob, imageUrl, filename }: { blob: Blob; imageUrl: string; filename: string }) {
  const [status, setStatus] = useState("");
  const file = new File([blob], filename, { type: blob.type });
  const shareData = { files: [file] };
  const canShare = typeof navigator.share === "function" && navigator.canShare?.(shareData);
  const canCopy = typeof ClipboardItem !== "undefined" && typeof navigator.clipboard?.write === "function";

  async function copyImage() {
    setStatus("");
    try {
      // Clipboard writes must start within the click on Safari. Pass the
      // conversion promise into ClipboardItem instead of awaiting it first.
      const png = blob.type === "image/png" ? blob : convertToPng(blob);
      await navigator.clipboard.write([new ClipboardItem({ "image/png": png })]);
      setStatus("Image copied");
    } catch {
      setStatus("Could not copy image. Try downloading it instead.");
    }
  }

  async function shareImage() {
    setStatus("");
    try {
      // The attachment is already in memory; no fetch can consume the click's
      // user activation before the native share sheet opens.
      await navigator.share(shareData);
    } catch (error) {
      if (!(error instanceof DOMException && error.name === "AbortError")) {
        setStatus("Could not share image. Try downloading it instead.");
      }
    }
  }

  return (
    <>
      {status && (
        <span role="status" className="absolute inset-x-0 top-full z-10 border-b border-border bg-background px-4 py-2 text-center text-ui">
          {status}
        </span>
      )}
      {canCopy && (
        <button type="button" className={controlClass} aria-label="Copy image" title="Copy image" onClick={copyImage}>
          <Copy className="size-4" aria-hidden="true" />
        </button>
      )}
      {canShare && (
        <button type="button" className={controlClass} aria-label="Share image" title="Share image" onClick={shareImage}>
          <Share2 className="size-4" aria-hidden="true" />
        </button>
      )}
      <a className={controlClass} href={imageUrl} download={filename} aria-label="Download image" title="Download image">
        <Download className="size-4" aria-hidden="true" />
      </a>
    </>
  );
}

export function ImagePreviewModal({
  isOpen,
  onClose,
  restoreFocus = focusChatInput,
  imageUrl,
  filename,
  imageBlob,
  navigation,
}: ImagePreviewModalProps) {
  const wasOpenRef = useRef(false);
  const containerRef = useRef<HTMLDivElement>(null);
  const touchStart = useRef<{ x: number; y: number } | null>(null);
  const suppressBackdropClick = useRef(false);

  const moveImage = useCallback((direction: -1 | 1) => {
    if (!navigation) return;
    const nextIndex = navigation.index + direction;
    if (nextIndex >= 0 && nextIndex < navigation.total) navigation.onSelect(nextIndex);
  }, [navigation]);

  function startSwipe(event: TouchEvent) {
    const touch = event.touches[0];
    suppressBackdropClick.current = false;
    touchStart.current = navigation && event.touches.length === 1 && touch
      ? { x: touch.clientX, y: touch.clientY }
      : null;
  }

  function endSwipe(event: TouchEvent) {
    const start = touchStart.current;
    const end = event.changedTouches[0];
    touchStart.current = null;
    if (!start || !end || event.touches.length > 0) return;
    const dx = end.clientX - start.x;
    const dy = end.clientY - start.y;
    if (Math.abs(dx) < 45 || Math.abs(dx) <= Math.abs(dy) * 1.5) return;
    suppressBackdropClick.current = true;
    moveImage(dx < 0 ? 1 : -1);
  }

  const handleKeyDown = useCallback((event: KeyboardEvent) => {
    const { key, altKey, ctrlKey, metaKey, shiftKey } = event;
    const isGalleryArrow = navigation && !altKey && !ctrlKey && !metaKey && !shiftKey
      && (key === "ArrowLeft" || key === "ArrowRight");
    if (key !== "Escape" && !isGalleryArrow) return;
    event.preventDefault();
    event.stopImmediatePropagation();
    if (key === "Escape") onClose();
    else moveImage(key === "ArrowLeft" ? -1 : 1);
  }, [navigation, onClose, moveImage]);

  // Focus the container when the modal opens, so screen readers land inside the
  // dialog and the scroll container responds to arrow keys. Escape no longer
  // depends on this: see the listener below for why focus was never the issue.
  useEffect(() => {
    if (isOpen && containerRef.current) {
      containerRef.current.focus();
    }
  }, [isOpen]);

  useEffect(() => {
    if (!isOpen) return;

    // Capture on window so the global document-capture shortcut dispatcher
    // can't swallow Escape or act on the chat behind the viewer.
    window.addEventListener("keydown", handleKeyDown, true);
    return () => window.removeEventListener("keydown", handleKeyDown, true);
  }, [isOpen, handleKeyDown]);

  useEffect(() => {
    if (!isOpen) return;
    // A parent dialog may already hold its own scroll lock.
    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => { document.body.style.overflow = previousOverflow; };
  }, [isOpen]);

  // For a gallery, return to the tile that opened it, not the chat keyboard.
  useEffect(() => {
    if (isOpen) {
      wasOpenRef.current = true;
    } else if (wasOpenRef.current) {
      wasOpenRef.current = false;
      restoreFocus();
    }
  }, [isOpen, restoreFocus]);

  if (!isOpen) return null;

  const modalContent = (
    <div
      ref={containerRef}
      tabIndex={-1}
      data-modal-open="true"
      role="dialog"
      aria-modal="true"
      aria-label={filename}
      className="fixed inset-0 z-[9999] flex flex-col bg-background text-foreground outline-none"
      style={{
        paddingTop: "env(safe-area-inset-top)",
        paddingBottom: "env(safe-area-inset-bottom)",
        paddingLeft: "env(safe-area-inset-left)",
        paddingRight: "env(safe-area-inset-right)",
      }}
    >
      <div className="relative flex shrink-0 items-center gap-1 border-b border-border px-2 py-1 sm:px-4">
        <span className="min-w-0 flex-1 truncate pl-2 text-ui font-medium" title={filename}>{filename}</span>
        {imageBlob && <ImageFileActions key={imageUrl} blob={imageBlob} imageUrl={imageUrl} filename={filename} />}
        <button type="button" onClick={onClose} className={controlClass} aria-label="Close (ESC)" title="Close (ESC)">
          <X className="size-5" aria-hidden="true" />
        </button>
      </div>

      <div
        className={cn("flex min-h-0 flex-1 cursor-pointer items-center justify-center overflow-auto p-2 sm:p-8", navigation && "touch-pan-y touch-pinch-zoom")}
        onTouchStart={startSwipe}
        onTouchEnd={endSwipe}
        onTouchCancel={() => { touchStart.current = null; }}
        onClick={(event) => {
          if (event.target === event.currentTarget && !suppressBackdropClick.current) onClose();
          suppressBackdropClick.current = false;
        }}
      >
        <img src={imageUrl} alt={filename} className="max-h-full max-w-full cursor-default object-contain" />
      </div>
      {navigation && (
        <div className="flex shrink-0 items-center justify-center gap-3 border-t border-border">
          <button type="button" className={controlClass} aria-label="Previous image" disabled={navigation.index === 0} onClick={() => moveImage(-1)}>
            <ChevronLeft className="size-5" aria-hidden="true" />
          </button>
          <span aria-live="polite" className="text-meta tabular-nums">{navigation.index + 1} / {navigation.total}</span>
          <button type="button" className={controlClass} aria-label="Next image" disabled={navigation.index === navigation.total - 1} onClick={() => moveImage(1)}>
            <ChevronRight className="size-5" aria-hidden="true" />
          </button>
        </div>
      )}
    </div>
  );

  return createPortal(modalContent, document.body);
}
