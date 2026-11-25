/**
 * The markup document for one image: what the user drew and commented on it,
 * in the image's own pixel coordinates. The SVG editor surface and the canvas
 * export both render from this, so what is saved is what was on screen.
 *
 * Everything here is pure: the editor's state is a `MarkupHistory` driven by
 * `markupReducer`, and nothing in this file touches the DOM.
 */

export interface Point {
  x: number;
  y: number;
}

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface StrokeShape {
  kind: 'stroke';
  id: string;
  color: string;
  size: number;
  points: Point[];
}

/** A rectangle or an ellipse, by its bounding box (w and h are never negative). */
export interface BoxShape {
  kind: 'rect' | 'ellipse';
  id: string;
  color: string;
  size: number;
  x: number;
  y: number;
  w: number;
  h: number;
}

export interface ArrowShape {
  kind: 'arrow';
  id: string;
  color: string;
  size: number;
  from: Point;
  to: Point;
}

/** Text anchored at its top-left corner; `text` may hold newlines. */
export interface TextShape {
  kind: 'text';
  id: string;
  color: string;
  fontSize: number;
  x: number;
  y: number;
  text: string;
}

/** A numbered comment: the pin is drawn on the image, the text goes into the message. */
export interface CommentPin {
  kind: 'pin';
  id: string;
  n: number;
  x: number;
  y: number;
  text: string;
}

export type Shape = StrokeShape | BoxShape | ArrowShape | TextShape | CommentPin;

export interface MarkupDoc {
  /** The source image's natural size. */
  width: number;
  height: number;
  /** The region kept on export, in image pixels. Absent: the whole image. */
  crop?: Rect;
  /** Painted in order: later shapes are on top. */
  shapes: Shape[];
}

/** The colours the toolbar offers; red first, the default. */
export const MARKUP_COLORS = ['#ef4444', '#f97316', '#eab308', '#22c55e', '#3b82f6', '#a855f7', '#ffffff', '#000000'] as const;
export const DEFAULT_MARKUP_COLOR: string = MARKUP_COLORS[0];

/** The comment pin's fill (brand violet) and number colour; fixed so pins read on any image. */
export const PIN_FILL = '#7c5cff';
export const PIN_TEXT = '#ffffff';

/**
 * Sizes scale with the image, so a stroke on a 4000px photo is as visible as
 * one on a 800px screenshot once both are shown fit-to-screen. Both renderers
 * call these; never hard-code a pixel size in one of them.
 */
function shortSide(doc: Pick<MarkupDoc, 'width' | 'height'>): number {
  return Math.max(1, Math.min(doc.width, doc.height));
}
export function defaultStrokeSize(doc: Pick<MarkupDoc, 'width' | 'height'>): number {
  return Math.max(2, Math.round(shortSide(doc) / 220));
}
export function defaultFontSize(doc: Pick<MarkupDoc, 'width' | 'height'>): number {
  return Math.max(14, Math.round(shortSide(doc) / 28));
}
export function pinRadius(doc: Pick<MarkupDoc, 'width' | 'height'>): number {
  return Math.max(12, Math.round(shortSide(doc) / 45));
}
/** Arrow head length for a stroke of `size`. */
export function arrowHeadLength(size: number): number {
  return Math.max(10, size * 4);
}

export function emptyDoc(width: number, height: number): MarkupDoc {
  return { width, height, shapes: [] };
}

let idCounter = 0;
export function newShapeId(): string {
  idCounter += 1;
  return `s${Date.now().toString(36)}${idCounter.toString(36)}`;
}

export function pinsOf(doc: MarkupDoc): CommentPin[] {
  return doc.shapes.filter((s): s is CommentPin => s.kind === 'pin').sort((a, b) => a.n - b.n);
}

export function nextPinNumber(doc: MarkupDoc): number {
  return pinsOf(doc).reduce((max, p) => Math.max(max, p.n), 0) + 1;
}

/** Pins numbered 1..n in their current order, so deleting pin 2 of 3 leaves 1 and 2. */
export function renumberPins(shapes: Shape[]): Shape[] {
  const order = shapes
    .filter((s): s is CommentPin => s.kind === 'pin')
    .sort((a, b) => a.n - b.n)
    .map((p) => p.id);
  return shapes.map((s) => (s.kind === 'pin' ? { ...s, n: order.indexOf(s.id) + 1 } : s));
}

/** Whether the doc changes the image at all: any shape, or a crop. */
export function hasMarkup(doc: MarkupDoc): boolean {
  return doc.shapes.length > 0 || doc.crop !== undefined;
}

// ── geometry ──────────────────────────────────────────────────────────────

/** Approximate text box: the renderers use a sans-serif at `fontSize`, ~0.6em per glyph. */
export function textBounds(shape: TextShape): Rect {
  const lines = shape.text.split('\n');
  const longest = lines.reduce((max, line) => Math.max(max, line.length), 0);
  return { x: shape.x, y: shape.y, w: Math.max(1, longest) * shape.fontSize * 0.6, h: lines.length * shape.fontSize * 1.25 };
}

export function shapeBounds(shape: Shape, doc: Pick<MarkupDoc, 'width' | 'height'>): Rect {
  switch (shape.kind) {
    case 'stroke': {
      const xs = shape.points.map((p) => p.x);
      const ys = shape.points.map((p) => p.y);
      const x = Math.min(...xs);
      const y = Math.min(...ys);
      return { x, y, w: Math.max(...xs) - x, h: Math.max(...ys) - y };
    }
    case 'rect':
    case 'ellipse':
      return { x: shape.x, y: shape.y, w: shape.w, h: shape.h };
    case 'arrow': {
      const x = Math.min(shape.from.x, shape.to.x);
      const y = Math.min(shape.from.y, shape.to.y);
      return { x, y, w: Math.abs(shape.to.x - shape.from.x), h: Math.abs(shape.to.y - shape.from.y) };
    }
    case 'text':
      return textBounds(shape);
    case 'pin': {
      const r = pinRadius(doc);
      return { x: shape.x - r, y: shape.y - r, w: 2 * r, h: 2 * r };
    }
  }
}

function distToSegment(p: Point, a: Point, b: Point): number {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const len2 = dx * dx + dy * dy;
  const t = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / len2));
  return Math.hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy));
}

function inRect(p: Point, r: Rect, pad: number): boolean {
  return p.x >= r.x - pad && p.x <= r.x + r.w + pad && p.y >= r.y - pad && p.y <= r.y + r.h + pad;
}

/** Whether `p` touches `shape`: on its outline for strokes, boxes and arrows, anywhere inside for text and pins. */
export function shapeHit(shape: Shape, p: Point, tolerance: number, doc: Pick<MarkupDoc, 'width' | 'height'>): boolean {
  switch (shape.kind) {
    case 'stroke': {
      const reach = tolerance + shape.size / 2;
      if (shape.points.length === 1) return Math.hypot(p.x - shape.points[0].x, p.y - shape.points[0].y) <= reach;
      for (let i = 1; i < shape.points.length; i++) {
        if (distToSegment(p, shape.points[i - 1], shape.points[i]) <= reach) return true;
      }
      return false;
    }
    case 'rect': {
      const reach = tolerance + shape.size / 2;
      const { x, y, w, h } = shape;
      const corners = [{ x, y }, { x: x + w, y }, { x: x + w, y: y + h }, { x, y: y + h }];
      return corners.some((c, i) => distToSegment(p, c, corners[(i + 1) % 4]) <= reach);
    }
    case 'ellipse': {
      const rx = shape.w / 2;
      const ry = shape.h / 2;
      if (rx <= 0 || ry <= 0) return inRect(p, shape, tolerance);
      const cx = shape.x + rx;
      const cy = shape.y + ry;
      // Distance from the outline, approximated by scaling the normalized radius back to pixels.
      const norm = Math.hypot((p.x - cx) / rx, (p.y - cy) / ry);
      return Math.abs(norm - 1) * Math.min(rx, ry) <= tolerance + shape.size / 2;
    }
    case 'arrow':
      return distToSegment(p, shape.from, shape.to) <= tolerance + shape.size / 2;
    case 'text':
    case 'pin':
      return inRect(p, shapeBounds(shape, doc), tolerance);
  }
}

/** The top-most shape under `p`, or undefined. */
export function hitTest(doc: MarkupDoc, p: Point, tolerance: number): Shape | undefined {
  for (let i = doc.shapes.length - 1; i >= 0; i--) {
    if (shapeHit(doc.shapes[i], p, tolerance, doc)) return doc.shapes[i];
  }
  return undefined;
}

export function translateShape(shape: Shape, dx: number, dy: number): Shape {
  switch (shape.kind) {
    case 'stroke':
      return { ...shape, points: shape.points.map((pt) => ({ x: pt.x + dx, y: pt.y + dy })) };
    case 'arrow':
      return { ...shape, from: { x: shape.from.x + dx, y: shape.from.y + dy }, to: { x: shape.to.x + dx, y: shape.to.y + dy } };
    default:
      return { ...shape, x: shape.x + dx, y: shape.y + dy };
  }
}

/** A box from two drag corners, in any direction. */
export function boxFromPoints(a: Point, b: Point): Rect {
  return { x: Math.min(a.x, b.x), y: Math.min(a.y, b.y), w: Math.abs(b.x - a.x), h: Math.abs(b.y - a.y) };
}

/**
 * A smooth SVG path through `points`: quadratic curves through the midpoints,
 * with the points as control points. `traceStroke` draws the same curve on a
 * canvas, so the export matches the editor.
 */
export function strokePath(points: Point[]): string {
  if (points.length === 0) return '';
  const f = (n: number) => Math.round(n * 100) / 100;
  if (points.length === 1) {
    const p = points[0];
    return `M${f(p.x)} ${f(p.y)}L${f(p.x)} ${f(p.y)}`;
  }
  let d = `M${f(points[0].x)} ${f(points[0].y)}`;
  for (let i = 1; i < points.length - 1; i++) {
    const mid = { x: (points[i].x + points[i + 1].x) / 2, y: (points[i].y + points[i + 1].y) / 2 };
    d += `Q${f(points[i].x)} ${f(points[i].y)} ${f(mid.x)} ${f(mid.y)}`;
  }
  const last = points[points.length - 1];
  return `${d}L${f(last.x)} ${f(last.y)}`;
}

/** The arrow head's two barb end points for an arrow from `from` to `to`. */
export function arrowHead(from: Point, to: Point, size: number): [Point, Point] {
  const angle = Math.atan2(to.y - from.y, to.x - from.x);
  const len = arrowHeadLength(size);
  const spread = Math.PI / 7;
  return [
    { x: to.x - len * Math.cos(angle - spread), y: to.y - len * Math.sin(angle - spread) },
    { x: to.x - len * Math.cos(angle + spread), y: to.y - len * Math.sin(angle + spread) },
  ];
}

/** Clamp a crop rect into the image, at least 16px on each side. Undefined when it covers the whole image. */
export function normalizeCrop(crop: Rect | undefined, doc: Pick<MarkupDoc, 'width' | 'height'>): Rect | undefined {
  if (!crop) return undefined;
  const x = Math.max(0, Math.min(crop.x, doc.width - 16));
  const y = Math.max(0, Math.min(crop.y, doc.height - 16));
  const w = Math.max(16, Math.min(crop.w, doc.width - x));
  const h = Math.max(16, Math.min(crop.h, doc.height - y));
  const rounded = { x: Math.round(x), y: Math.round(y), w: Math.round(w), h: Math.round(h) };
  if (rounded.x === 0 && rounded.y === 0 && rounded.w >= doc.width && rounded.h >= doc.height) return undefined;
  return rounded;
}

// ── history ───────────────────────────────────────────────────────────────

export interface MarkupHistory {
  past: MarkupDoc[];
  present: MarkupDoc;
  future: MarkupDoc[];
  /**
   * The coalesce key of the last edit. An edit carrying the same key replaces
   * the present instead of pushing a new undo step, so a drag or a run of
   * typing undoes as one step.
   */
  lastKey?: string;
}

export type MarkupAction =
  | { type: 'add'; shape: Shape }
  | { type: 'update'; id: string; patch: Partial<Shape>; coalesce?: string }
  | { type: 'translate'; id: string; dx: number; dy: number; coalesce?: string }
  | { type: 'remove'; ids: string[] }
  | { type: 'setCrop'; crop: Rect | undefined; coalesce?: string }
  /** Replace the whole doc (Revert, or opening a saved markup) as one undo step. */
  | { type: 'reset'; doc: MarkupDoc }
  | { type: 'undo' }
  | { type: 'redo' }
  /** End a coalescing run: the next edit starts a new undo step even with the same key. */
  | { type: 'commit' };

export function createHistory(doc: MarkupDoc): MarkupHistory {
  return { past: [], present: doc, future: [] };
}

export function canUndo(h: MarkupHistory): boolean {
  return h.past.length > 0;
}
export function canRedo(h: MarkupHistory): boolean {
  return h.future.length > 0;
}
/** Whether there is anything to save: an edit since the editor opened. */
export function isDirty(h: MarkupHistory): boolean {
  return h.past.length > 0;
}

function edit(h: MarkupHistory, next: MarkupDoc, key?: string): MarkupHistory {
  if (next === h.present) return h;
  if (key !== undefined && key === h.lastKey) {
    return { ...h, present: next, future: [] };
  }
  return { past: [...h.past, h.present], present: next, future: [], lastKey: key };
}

function mapShape(doc: MarkupDoc, id: string, fn: (s: Shape) => Shape): MarkupDoc {
  let changed = false;
  const shapes = doc.shapes.map((s) => {
    if (s.id !== id) return s;
    changed = true;
    return fn(s);
  });
  return changed ? { ...doc, shapes } : doc;
}

export function markupReducer(h: MarkupHistory, action: MarkupAction): MarkupHistory {
  const doc = h.present;
  switch (action.type) {
    case 'add':
      return edit(h, { ...doc, shapes: [...doc.shapes, action.shape] });
    case 'update':
      return edit(h, mapShape(doc, action.id, (s) => ({ ...s, ...action.patch, kind: s.kind, id: s.id }) as Shape), action.coalesce);
    case 'translate':
      return edit(h, mapShape(doc, action.id, (s) => translateShape(s, action.dx, action.dy)), action.coalesce);
    case 'remove': {
      const ids = new Set(action.ids);
      const kept = doc.shapes.filter((s) => !ids.has(s.id));
      if (kept.length === doc.shapes.length) return h;
      const removedPin = doc.shapes.some((s) => ids.has(s.id) && s.kind === 'pin');
      return edit(h, { ...doc, shapes: removedPin ? renumberPins(kept) : kept });
    }
    case 'setCrop':
      return edit(h, { ...doc, crop: normalizeCrop(action.crop, doc) }, action.coalesce);
    case 'reset':
      return edit(h, action.doc);
    case 'undo': {
      if (h.past.length === 0) return h;
      return { past: h.past.slice(0, -1), present: h.past[h.past.length - 1], future: [h.present, ...h.future] };
    }
    case 'redo': {
      if (h.future.length === 0) return h;
      return { past: [...h.past, h.present], present: h.future[0], future: h.future.slice(1) };
    }
    case 'commit':
      return h.lastKey === undefined ? h : { ...h, lastKey: undefined };
  }
}
