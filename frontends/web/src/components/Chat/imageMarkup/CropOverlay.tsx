import { useImperativeHandle, useRef, type PointerEvent as ReactPointerEvent, type Ref } from 'react';
import type { MarkupDoc, Rect } from './model';
import { aspectRatio, resizeCrop, type CropAspect, type CropHandle } from './cropMath';
import type { LayerHandle } from './surfaceCoords';

interface CropOverlayProps {
  doc: Pick<MarkupDoc, 'width' | 'height'>;
  /** Screen pixels per image pixel. */
  scale: number;
  rect: Rect;
  onRectChange: (rect: Rect) => void;
  aspect: CropAspect;
  ref?: Ref<LayerHandle>;
}

const HANDLES: Array<{ id: Exclude<CropHandle, 'move'>; x: 0 | 0.5 | 1; y: 0 | 0.5 | 1; cursor: string }> = [
  { id: 'nw', x: 0, y: 0, cursor: 'nwse-resize' },
  { id: 'n', x: 0.5, y: 0, cursor: 'ns-resize' },
  { id: 'ne', x: 1, y: 0, cursor: 'nesw-resize' },
  { id: 'e', x: 1, y: 0.5, cursor: 'ew-resize' },
  { id: 'se', x: 1, y: 1, cursor: 'nwse-resize' },
  { id: 's', x: 0.5, y: 1, cursor: 'ns-resize' },
  { id: 'sw', x: 0, y: 1, cursor: 'nesw-resize' },
  { id: 'w', x: 0, y: 0.5, cursor: 'ew-resize' },
];

const HANDLE_LABEL: Record<CropHandle, string> = {
  move: 'Move crop',
  n: 'Resize crop top',
  s: 'Resize crop bottom',
  e: 'Resize crop right',
  w: 'Resize crop left',
  nw: 'Resize crop top left',
  ne: 'Resize crop top right',
  sw: 'Resize crop bottom left',
  se: 'Resize crop bottom right',
};

interface Drag {
  handle: CropHandle;
  px: number;
  py: number;
  start: Rect;
}

/**
 * The crop frame: the outside dimmed, the kept rect bright with eight handles,
 * drag inside to move. It only edits the rect it is given; Done (in the crop
 * toolbar) is what writes the document's crop.
 */
export function CropOverlay({ doc, scale, rect, onRectChange, aspect, ref }: CropOverlayProps) {
  const drag = useRef<Drag | null>(null);
  const safeScale = scale > 0 ? scale : 1;
  const bounds: Rect = { x: 0, y: 0, w: doc.width, h: doc.height };

  useImperativeHandle(
    ref,
    () => ({
      cancel() {
        const d = drag.current;
        if (!d) return false;
        drag.current = null;
        onRectChange(d.start);
        return true;
      },
    }),
    [onRectChange],
  );

  const onDown = (handle: CropHandle) => (e: ReactPointerEvent<HTMLElement>) => {
    if (e.pointerType === 'mouse' && e.button !== 0) return;
    e.preventDefault();
    e.stopPropagation();
    try {
      e.currentTarget.setPointerCapture?.(e.pointerId);
    } catch {
      // A synthetic pointer has nothing to capture.
    }
    drag.current = { handle, px: e.clientX, py: e.clientY, start: rect };
  };
  const onMove = (e: ReactPointerEvent<HTMLElement>) => {
    const d = drag.current;
    if (!d) return;
    onRectChange(resizeCrop(d.start, d.handle, (e.clientX - d.px) / safeScale, (e.clientY - d.py) / safeScale, bounds, aspectRatio(aspect)));
  };
  const onUp = () => {
    drag.current = null;
  };

  const left = rect.x * safeScale;
  const top = rect.y * safeScale;
  const width = rect.w * safeScale;
  const height = rect.h * safeScale;
  const dim = 'absolute bg-black/55';

  return (
    <div data-testid="crop-overlay" className="absolute inset-0 z-10 touch-none overflow-hidden" onClick={(e) => e.stopPropagation()}>
      <div aria-hidden className={dim} style={{ left: 0, top: 0, right: 0, height: top }} />
      <div aria-hidden className={dim} style={{ left: 0, top: top + height, right: 0, bottom: 0 }} />
      <div aria-hidden className={dim} style={{ left: 0, top, width: left, height }} />
      <div aria-hidden className={dim} style={{ left: left + width, top, right: 0, height }} />
      <div
        role="group"
        aria-label="Crop area"
        data-testid="crop-rect"
        className="absolute cursor-move touch-none border border-white shadow-[0_0_0_1px_rgba(0,0,0,0.4)]"
        style={{ left, top, width, height }}
        onPointerDown={onDown('move')}
        onPointerMove={onMove}
        onPointerUp={onUp}
        onPointerCancel={onUp}
      >
        {HANDLES.map((h) => (
          <div
            key={h.id}
            role="button"
            tabIndex={-1}
            aria-label={HANDLE_LABEL[h.id]}
            data-handle={h.id}
            className="absolute flex h-6 w-6 -translate-x-1/2 -translate-y-1/2 touch-none items-center justify-center touch:h-11 touch:w-11"
            style={{ left: `${h.x * 100}%`, top: `${h.y * 100}%`, cursor: h.cursor }}
            onPointerDown={onDown(h.id)}
            onPointerMove={onMove}
            onPointerUp={onUp}
            onPointerCancel={onUp}
          >
            <span aria-hidden className="block h-2.5 w-2.5 rounded-[2px] border border-black/50 bg-white" />
          </div>
        ))}
      </div>
    </div>
  );
}
