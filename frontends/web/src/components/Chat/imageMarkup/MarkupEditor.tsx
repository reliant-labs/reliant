import { useCallback, useEffect, useImperativeHandle, useLayoutEffect, useRef, useState, type Ref } from 'react';
import { CommentLayer } from './CommentLayer';
import { CommentToolbar } from './CommentToolbar';
import { CropOverlay } from './CropOverlay';
import { CropToolbar } from './CropToolbar';
import { EditToolbar } from './EditToolbar';
import { MarkupSurface, type SurfaceHandle } from './MarkupSurface';
import { MarkupToolbar } from './MarkupToolbar';
import { fitRatio, aspectRatio, sameRect, type CropAspect } from './cropMath';
import { emptyDoc, type MarkupDoc, type Rect } from './model';
import type { LayerHandle } from './surfaceCoords';
import { useMarkupEditor, type EditorMode } from './useMarkupEditor';

export type { EditorMode } from './useMarkupEditor';

/** What the preview dialog calls: it owns the keyboard, the editor only exposes these. */
export interface MarkupEditorHandle {
  undo(): void;
  redo(): void;
  /** Deletes the selected shape (markup mode); false when nothing was selected. */
  deleteSelection(): boolean;
  /**
   * Escape: cancels the innermost layer (the text box, a pending comment pin,
   * the selection, a pen/shape/move/crop drag). False when there was nothing
   * to cancel, so the caller can leave the mode or the dialog.
   */
  cancel(): boolean;
}

export interface MarkupEditorProps {
  /** The un-annotated source image to display (a blob URL). Key the editor on it. */
  imageUrl: string;
  /** Reopens an earlier markup; without it the doc starts empty at the image's natural size once it loads. */
  initialDoc?: MarkupDoc;
  mode: EditorMode;
  onModeChange: (mode: EditorMode) => void;
  /** Flatten and upload. The editor shows a spinner on Save while it is pending and stays open if it rejects. */
  onSave: (doc: MarkupDoc) => Promise<void>;
  /** Parent-owned saving state; combined with the editor's own pending state. */
  saving?: boolean;
  onDirtyChange?: (dirty: boolean) => void;
  editorRef?: Ref<MarkupEditorHandle>;
  /** Image alt text. */
  alt?: string;
}

/** Room kept around the image so handles and pins at its edge stay reachable. */
const AREA_PADDING = 32;
/** Scale cap: a small image is shown at its own size, never blown up. */
const MAX_SCALE = 1;

function useElementSize<T extends HTMLElement>() {
  const ref = useRef<T>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const r = el.getBoundingClientRect();
      setSize((prev) => (prev.w === r.width && prev.h === r.height ? prev : { w: r.width, h: r.height }));
    };
    measure();
    if (typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, size] as const;
}

export function MarkupEditor({ imageUrl, initialDoc, mode, onModeChange, onSave, saving: savingProp, onDirtyChange, editorRef, alt = '' }: MarkupEditorProps) {
  const ed = useMarkupEditor(initialDoc);
  const { doc, init, dispatch } = ed;
  const [areaRef, area] = useElementSize<HTMLDivElement>();
  const [pillOffset, setPillOffset] = useState({ x: 0, y: 0 });
  const [pendingSave, setPendingSave] = useState(false);
  const saving = pendingSave || !!savingProp;

  const imgRef = useRef<HTMLImageElement>(null);
  const surfaceRef = useRef<SurfaceHandle>(null);
  const commentRef = useRef<LayerHandle>(null);
  const cropRef = useRef<LayerHandle>(null);

  // The doc starts at the image's natural size, known once the image has loaded.
  const adopt = useCallback(
    (img: HTMLImageElement) => {
      if (img.naturalWidth > 0 && img.naturalHeight > 0 && !ed.ready) init(emptyDoc(img.naturalWidth, img.naturalHeight));
    },
    [ed.ready, init],
  );
  useEffect(() => {
    if (imgRef.current) adopt(imgRef.current);
  }, [adopt]);

  const lastDirty = useRef(false);
  useEffect(() => {
    if (lastDirty.current === ed.dirty) return;
    lastDirty.current = ed.dirty;
    onDirtyChange?.(ed.dirty);
  }, [ed.dirty, onDirtyChange]);

  // ── crop ────────────────────────────────────────────────────────────────
  const full: Rect = { x: 0, y: 0, w: doc.width, h: doc.height };
  const [cropRect, setCropRect] = useState<Rect>(full);
  const [aspect, setAspect] = useState<CropAspect>('free');
  const prevMode = useRef<EditorMode>(mode);
  useEffect(() => {
    if (mode === 'crop' && prevMode.current !== 'crop') {
      setCropRect(doc.crop ?? { x: 0, y: 0, w: doc.width, h: doc.height });
      setAspect('free');
    }
    prevMode.current = mode;
  }, [mode, doc.crop, doc.width, doc.height]);
  // The doc arrives after the image loads; a crop frame opened before then spans it.
  useEffect(() => {
    if (mode === 'crop' && ed.ready) setCropRect((r) => (r.w === 0 || r.h === 0 ? { x: 0, y: 0, w: doc.width, h: doc.height } : r));
  }, [mode, ed.ready, doc.width, doc.height]);

  const pickAspect = (next: CropAspect) => {
    setAspect(next);
    const ratio = aspectRatio(next);
    if (ratio !== null) setCropRect((r) => fitRatio(r, ratio));
  };
  const finishCrop = () => {
    const unchanged = doc.crop ? sameRect(cropRect, doc.crop) : sameRect(cropRect, full);
    if (!unchanged) dispatch({ type: 'setCrop', crop: cropRect });
    onModeChange('view');
  };

  // ── save ────────────────────────────────────────────────────────────────
  const save = useCallback(async () => {
    if (pendingSave) return;
    setPendingSave(true);
    try {
      await onSave(doc);
    } catch {
      // The parent reports the failure; the editor stays open with the doc intact.
    } finally {
      setPendingSave(false);
    }
  }, [doc, onSave, pendingSave]);

  useImperativeHandle(
    editorRef,
    () => ({
      undo: ed.undo,
      redo: ed.redo,
      deleteSelection: () => surfaceRef.current?.deleteSelection() ?? false,
      cancel: () => {
        if (mode === 'markup') return surfaceRef.current?.cancel() ?? false;
        if (mode === 'comment') return commentRef.current?.cancel() ?? false;
        if (mode === 'crop') return cropRef.current?.cancel() ?? false;
        return false;
      },
    }),
    [ed.undo, ed.redo, mode],
  );

  // ── layout ──────────────────────────────────────────────────────────────
  const availW = Math.max(0, area.w - 2 * AREA_PADDING);
  const availH = Math.max(0, area.h - 2 * AREA_PADDING);
  const measured = availW > 0 && availH > 0 && doc.width > 0 && doc.height > 0;
  const scale = measured ? Math.min(availW / doc.width, availH / doc.height, MAX_SCALE) : 1;
  const boxStyle = ed.ready ? { width: doc.width * scale, height: doc.height * scale } : undefined;
  const pill = { offset: pillOffset, onOffsetChange: setPillOffset };

  return (
    <div className="relative h-full w-full overflow-hidden" data-testid="markup-editor" data-mode={mode}>
      <div ref={areaRef} className="absolute inset-0 flex items-center justify-center" style={{ padding: AREA_PADDING }}>
        <div className="relative max-h-full max-w-full select-none" style={boxStyle} data-testid="markup-image-box">
          <img
            ref={imgRef}
            src={imageUrl}
            alt={alt}
            draggable={false}
            onLoad={(e) => adopt(e.currentTarget)}
            className={ed.ready ? 'block h-full w-full object-contain' : 'block max-h-full max-w-full object-contain'}
          />
          {ed.ready ? (
            <>
              <MarkupSurface
                ref={surfaceRef}
                doc={doc}
                scale={scale}
                interactive={mode === 'markup'}
                tool={ed.tool}
                shapeKind={ed.shapeKind}
                color={ed.color}
                dispatch={dispatch}
                dimOutside={doc.crop && mode !== 'crop' ? doc.crop : undefined}
              />
              {mode === 'comment' ? <CommentLayer ref={commentRef} doc={doc} scale={scale} dispatch={dispatch} /> : null}
              {mode === 'crop' ? <CropOverlay ref={cropRef} doc={doc} scale={scale} rect={cropRect} onRectChange={setCropRect} aspect={aspect} /> : null}
            </>
          ) : null}
        </div>
      </div>

      {ed.ready && mode === 'view' ? <EditToolbar pill={pill} onMode={onModeChange} dirty={ed.dirty} saving={saving} onSave={save} /> : null}
      {ed.ready && mode === 'markup' ? (
        <MarkupToolbar
          pill={pill}
          tool={ed.tool}
          onTool={ed.setTool}
          shapeKind={ed.shapeKind}
          onShapeKind={ed.setShapeKind}
          color={ed.color}
          onColor={ed.setColor}
          canUndo={ed.canUndo}
          canRedo={ed.canRedo}
          onUndo={ed.undo}
          onRedo={ed.redo}
          dirty={ed.dirty}
          saving={saving}
          onSave={save}
          onClose={() => onModeChange('view')}
        />
      ) : null}
      {ed.ready && mode === 'comment' ? <CommentToolbar pill={pill} dirty={ed.dirty} saving={saving} onSave={save} onClose={() => onModeChange('view')} /> : null}
      {ed.ready && mode === 'crop' ? (
        <CropToolbar pill={pill} aspect={aspect} onAspect={pickAspect} onReset={() => { setAspect('free'); setCropRect(full); }} onDone={finishCrop} onClose={() => onModeChange('view')} />
      ) : null}
    </div>
  );
}
