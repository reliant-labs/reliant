import { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState, type Dispatch, type PointerEvent as ReactPointerEvent, type Ref } from 'react';
import {
  boxFromPoints,
  defaultFontSize,
  defaultStrokeSize,
  hitTest,
  newShapeId,
  shapeBounds,
  shapeHit,
  type MarkupAction,
  type MarkupDoc,
  type Point,
  type Rect,
  type Shape,
  type TextShape,
} from './model';
import { ShapeView, TEXT_FONT_FAMILY, TEXT_LINE_HEIGHT } from './ShapeView';
import { clientToImage, type LayerHandle } from './surfaceCoords';
import type { MarkupTool, ShapeKind } from './useMarkupEditor';

export interface SurfaceHandle extends LayerHandle {
  /** Removes the selected shape; false when nothing is selected. */
  deleteSelection(): boolean;
}

interface MarkupSurfaceProps {
  doc: MarkupDoc;
  /** Screen pixels per image pixel. */
  scale: number;
  /** Whether the drawing tools are live (markup mode); otherwise the shapes are only shown. */
  interactive: boolean;
  tool: MarkupTool;
  shapeKind: ShapeKind;
  color: string;
  dispatch: Dispatch<MarkupAction>;
  /** The kept region to dim the rest of, when a crop is set and not being edited. */
  dimOutside?: Rect;
  ref?: Ref<SurfaceHandle>;
}

interface TextEdit {
  /** Set when editing an existing text shape. */
  id?: string;
  original: string;
  x: number;
  y: number;
  value: string;
  color: string;
  fontSize: number;
}

type Gesture =
  | { kind: 'pen'; points: Point[] }
  | { kind: 'shape'; from: Point }
  | { kind: 'move'; id: string; last: Point }
  | { kind: 'erase'; ids: Set<string> }
  | { kind: 'text'; at: Point; sx: number; sy: number };

const EMPTY_IDS: ReadonlySet<string> = new Set();
/** Below this many screen pixels a drag is a click, not a stroke or a shape. */
const MIN_STROKE_PX = 2;
const MIN_SHAPE_PX = 4;

function coarsePointer(): boolean {
  return typeof window !== 'undefined' && typeof window.matchMedia === 'function' && window.matchMedia('(pointer: coarse)').matches;
}

const CURSORS: Record<MarkupTool, string> = {
  select: 'default',
  pen: 'crosshair',
  shape: 'crosshair',
  eraser: 'crosshair',
  text: 'text',
};

export function MarkupSurface({ doc, scale, interactive, tool, shapeKind, color, dispatch, dimOutside, ref }: MarkupSurfaceProps) {
  const svgRef = useRef<SVGSVGElement>(null);
  const gesture = useRef<Gesture | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [live, setLive] = useState<Shape | null>(null);
  const [erasing, setErasing] = useState<ReadonlySet<string>>(EMPTY_IDS);
  const [textEdit, setTextEditState] = useState<TextEdit | null>(null);
  const textEditRef = useRef<TextEdit | null>(null);

  const setTextEdit = useCallback((next: TextEdit | null) => {
    textEditRef.current = next;
    setTextEditState(next);
  }, []);

  // Pins belong to comment mode: the markup tools never select, move or erase them.
  const editable = useMemo<MarkupDoc>(() => ({ ...doc, shapes: doc.shapes.filter((s) => s.kind !== 'pin') }), [doc]);
  const safeScale = scale > 0 ? scale : 1;
  const tolerance = (coarsePointer() ? 12 : 6) / safeScale;

  const selected = selectedId ? doc.shapes.find((s) => s.id === selectedId && s.kind !== 'pin') : undefined;

  useEffect(() => {
    setSelectedId(null);
  }, [tool, interactive]);

  const commitText = useCallback(() => {
    const te = textEditRef.current;
    if (!te) return;
    setTextEdit(null);
    const blank = te.value.trim() === '';
    if (te.id) {
      if (blank) dispatch({ type: 'remove', ids: [te.id] });
      else if (te.value !== te.original) dispatch({ type: 'update', id: te.id, patch: { text: te.value } });
    } else if (!blank) {
      const shape: TextShape = { kind: 'text', id: newShapeId(), color: te.color, fontSize: te.fontSize, x: te.x, y: te.y, text: te.value };
      dispatch({ type: 'add', shape });
    }
  }, [dispatch, setTextEdit]);

  // Leaving markup mode with the text box open keeps what was typed.
  const commitTextRef = useRef(commitText);
  commitTextRef.current = commitText;
  useEffect(() => () => commitTextRef.current(), []);

  const abortGesture = useCallback((): boolean => {
    if (!gesture.current) return false;
    const wasMove = gesture.current.kind === 'move';
    gesture.current = null;
    setLive(null);
    setErasing(EMPTY_IDS);
    if (wasMove) dispatch({ type: 'commit' });
    return true;
  }, [dispatch]);

  useImperativeHandle(
    ref,
    () => ({
      cancel() {
        if (textEditRef.current) {
          setTextEdit(null);
          return true;
        }
        if (abortGesture()) return true;
        if (selectedId !== null) {
          setSelectedId(null);
          return true;
        }
        return false;
      },
      deleteSelection() {
        if (!selected) return false;
        dispatch({ type: 'remove', ids: [selected.id] });
        setSelectedId(null);
        return true;
      },
    }),
    [abortGesture, dispatch, selected, selectedId, setTextEdit],
  );

  const toPoint = (e: { clientX: number; clientY: number }): Point => clientToImage(svgRef.current, e.clientX, e.clientY, doc);

  const buildShape = (from: Point, to: Point): Shape => {
    const size = defaultStrokeSize(doc);
    if (shapeKind === 'arrow') return { kind: 'arrow', id: 'live', color, size, from, to };
    return { kind: shapeKind, id: 'live', color, size, ...boxFromPoints(from, to) };
  };
  const buildStroke = (points: Point[]): Shape => ({ kind: 'stroke', id: 'live', color, size: defaultStrokeSize(doc), points: [...points] });

  const hitIds = (p: Point): string[] => editable.shapes.filter((s) => shapeHit(s, p, tolerance, doc)).map((s) => s.id);

  const openText = (p: Point) => {
    const hit = hitTest(editable, p, tolerance);
    if (hit?.kind === 'text') {
      setTextEdit({ id: hit.id, original: hit.text, x: hit.x, y: hit.y, value: hit.text, color: hit.color, fontSize: hit.fontSize });
    } else {
      setTextEdit({ original: '', x: p.x, y: p.y, value: '', color, fontSize: defaultFontSize(doc) });
    }
  };

  const onPointerDown = (e: ReactPointerEvent<SVGSVGElement>) => {
    if (!interactive || (e.pointerType === 'mouse' && e.button !== 0)) return;
    if (gesture.current) return;
    commitText();
    // Cancelling the press keeps the compatibility mousedown from moving focus off a text box we are about to open.
    e.preventDefault();
    try {
      e.currentTarget.setPointerCapture?.(e.pointerId);
    } catch {
      // A synthetic pointer has nothing to capture.
    }
    const p = toPoint(e);
    switch (tool) {
      case 'pen':
        gesture.current = { kind: 'pen', points: [p] };
        setLive(buildStroke([p]));
        break;
      case 'shape':
        gesture.current = { kind: 'shape', from: p };
        setLive(buildShape(p, p));
        break;
      case 'select': {
        const hit = hitTest(editable, p, tolerance);
        setSelectedId(hit?.id ?? null);
        if (hit) gesture.current = { kind: 'move', id: hit.id, last: p };
        break;
      }
      case 'eraser': {
        const ids = new Set(hitIds(p));
        gesture.current = { kind: 'erase', ids };
        setErasing(new Set(ids));
        break;
      }
      case 'text':
        gesture.current = { kind: 'text', at: p, sx: e.clientX, sy: e.clientY };
        break;
    }
  };

  const onPointerMove = (e: ReactPointerEvent<SVGSVGElement>) => {
    const g = gesture.current;
    if (!g) return;
    const p = toPoint(e);
    switch (g.kind) {
      case 'pen':
        g.points.push(p);
        setLive(buildStroke(g.points));
        break;
      case 'shape':
        setLive(buildShape(g.from, p));
        break;
      case 'move': {
        const dx = p.x - g.last.x;
        const dy = p.y - g.last.y;
        g.last = p;
        if (dx !== 0 || dy !== 0) dispatch({ type: 'translate', id: g.id, dx, dy, coalesce: `move:${g.id}` });
        break;
      }
      case 'erase': {
        const before = g.ids.size;
        for (const id of hitIds(p)) g.ids.add(id);
        if (g.ids.size !== before) setErasing(new Set(g.ids));
        break;
      }
      case 'text':
        break;
    }
  };

  const onPointerUp = (e: ReactPointerEvent<SVGSVGElement>) => {
    const g = gesture.current;
    if (!g) return;
    gesture.current = null;
    const p = toPoint(e);
    switch (g.kind) {
      case 'pen': {
        const stroke = buildStroke(g.points) as Extract<Shape, { kind: 'stroke' }>;
        const b = shapeBounds(stroke, doc);
        if (Math.max(b.w, b.h) * safeScale >= MIN_STROKE_PX) dispatch({ type: 'add', shape: { ...stroke, id: newShapeId() } });
        setLive(null);
        break;
      }
      case 'shape': {
        const shape = buildShape(g.from, p);
        const b = shapeBounds(shape, doc);
        const big = shape.kind === 'arrow' ? Math.hypot(b.w, b.h) : Math.max(b.w, b.h);
        if (big * safeScale >= MIN_SHAPE_PX) dispatch({ type: 'add', shape: { ...shape, id: newShapeId() } });
        setLive(null);
        break;
      }
      case 'move':
        dispatch({ type: 'commit' });
        break;
      case 'erase':
        if (g.ids.size > 0) dispatch({ type: 'remove', ids: [...g.ids] });
        setErasing(EMPTY_IDS);
        break;
      case 'text':
        if (Math.hypot(e.clientX - g.sx, e.clientY - g.sy) < 6) openText(g.at);
        break;
    }
  };

  const onPointerCancel = () => {
    abortGesture();
  };

  const bounds = selected ? shapeBounds(selected, doc) : undefined;
  const pad = selected ? 4 / safeScale + ('size' in selected ? selected.size / 2 : 0) : 0;

  return (
    <>
      <svg
        ref={svgRef}
        data-testid="markup-surface"
        viewBox={`0 0 ${doc.width} ${doc.height}`}
        className="absolute inset-0 h-full w-full overflow-hidden"
        style={{ touchAction: 'none', pointerEvents: interactive ? 'auto' : 'none', cursor: interactive ? CURSORS[tool] : undefined }}
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={onPointerUp}
        onPointerCancel={onPointerCancel}
      >
        {dimOutside ? (
          <path
            d={`M0 0H${doc.width}V${doc.height}H0Z M${dimOutside.x} ${dimOutside.y}V${dimOutside.y + dimOutside.h}H${dimOutside.x + dimOutside.w}V${dimOutside.y}Z`}
            fill="#000000"
            fillOpacity={0.55}
            fillRule="evenodd"
            pointerEvents="none"
          />
        ) : null}
        {doc.shapes.map((s) =>
          s.id === textEdit?.id ? null : <ShapeView key={s.id} shape={s} width={doc.width} height={doc.height} opacity={erasing.has(s.id) ? 0.3 : undefined} />,
        )}
        {live ? <ShapeView shape={live} width={doc.width} height={doc.height} /> : null}
        {bounds ? (
          <g pointerEvents="none" data-testid="markup-selection">
            <rect x={bounds.x - pad} y={bounds.y - pad} width={bounds.w + 2 * pad} height={bounds.h + 2 * pad} fill="none" stroke="#000000" strokeOpacity={0.5} strokeWidth={3} vectorEffect="non-scaling-stroke" />
            <rect x={bounds.x - pad} y={bounds.y - pad} width={bounds.w + 2 * pad} height={bounds.h + 2 * pad} fill="none" stroke="#ffffff" strokeWidth={1.5} strokeDasharray="5 4" vectorEffect="non-scaling-stroke" />
          </g>
        ) : null}
      </svg>
      {textEdit ? (
        <TextBox
          edit={textEdit}
          scale={safeScale}
          onChange={(value) => setTextEdit({ ...textEdit, value })}
          onCommit={commitText}
          onCancel={() => setTextEdit(null)}
        />
      ) : null}
    </>
  );
}

function TextBox({ edit, scale, onChange, onCommit, onCancel }: { edit: TextEdit; scale: number; onChange: (v: string) => void; onCommit: () => void; onCancel: () => void }) {
  const ref = useRef<HTMLTextAreaElement>(null);
  useEffect(() => {
    ref.current?.focus();
  }, []);
  const lines = edit.value.split('\n');
  const longest = lines.reduce((m, l) => Math.max(m, l.length), 0);
  const fontPx = edit.fontSize * scale;
  return (
    <textarea
      ref={ref}
      aria-label="Text"
      value={edit.value}
      wrap="off"
      rows={lines.length}
      spellCheck={false}
      onChange={(e) => onChange(e.target.value)}
      onBlur={onCommit}
      onPointerDown={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        if (e.key === 'Escape') {
          e.preventDefault();
          e.stopPropagation();
          onCancel();
        } else if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          e.stopPropagation();
          onCommit();
        }
      }}
      className="absolute z-10 m-0 resize-none overflow-visible whitespace-pre border-0 bg-transparent p-0 outline outline-1 outline-dashed outline-white/70"
      style={{
        left: edit.x * scale,
        top: edit.y * scale,
        width: Math.max(longest * 0.6 * fontPx + fontPx, fontPx * 3),
        height: lines.length * TEXT_LINE_HEIGHT * fontPx,
        color: edit.color,
        fontSize: fontPx,
        lineHeight: TEXT_LINE_HEIGHT,
        fontFamily: TEXT_FONT_FAMILY,
        textShadow: '0 0 3px rgba(0,0,0,.6)',
        caretColor: edit.color,
      }}
    />
  );
}
