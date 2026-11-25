import type { Rect } from './model';

export type CropHandle = 'move' | 'n' | 's' | 'e' | 'w' | 'nw' | 'ne' | 'sw' | 'se';
export type CropAspect = 'free' | '1:1' | '16:9';

export const CROP_MIN = 16;

export function aspectRatio(aspect: CropAspect): number | null {
  if (aspect === '1:1') return 1;
  if (aspect === '16:9') return 16 / 9;
  return null;
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

export function moveCrop(start: Rect, dx: number, dy: number, bounds: Rect): Rect {
  return {
    ...start,
    x: clamp(start.x + dx, bounds.x, bounds.x + bounds.w - start.w),
    y: clamp(start.y + dy, bounds.y, bounds.y + bounds.h - start.h),
  };
}

/** The largest rect of `ratio` that fits inside `rect`, sharing its centre. */
export function fitRatio(rect: Rect, ratio: number): Rect {
  let w = rect.w;
  let h = rect.h;
  if (w / h > ratio) w = h * ratio;
  else h = w / ratio;
  return { x: rect.x + (rect.w - w) / 2, y: rect.y + (rect.h - h) / 2, w, h };
}

/**
 * Drags `handle` of `start` by (dx, dy), staying inside `bounds` and at least
 * `min` on each side. With a `ratio` the opposite corner (or edge midpoint)
 * stays put and the rect keeps its proportions.
 */
export function resizeCrop(start: Rect, handle: CropHandle, dx: number, dy: number, bounds: Rect, ratio: number | null, min = CROP_MIN): Rect {
  if (handle === 'move') return moveCrop(start, dx, dy, bounds);
  const bx0 = bounds.x;
  const by0 = bounds.y;
  const bx1 = bounds.x + bounds.w;
  const by1 = bounds.y + bounds.h;
  const movesW = handle.includes('w');
  const movesE = handle.includes('e');
  const movesN = handle.includes('n');
  const movesS = handle.includes('s');

  let left = start.x;
  let right = start.x + start.w;
  let top = start.y;
  let bottom = start.y + start.h;
  if (movesW) left = clamp(left + dx, bx0, right - min);
  if (movesE) right = clamp(right + dx, left + min, bx1);
  if (movesN) top = clamp(top + dy, by0, bottom - min);
  if (movesS) bottom = clamp(bottom + dy, top + min, by1);
  if (ratio === null) return { x: left, y: top, w: right - left, h: bottom - top };

  const minW = Math.max(min, min * ratio);
  const minH = Math.max(min, min / ratio);
  if (handle.length === 2) {
    const ax = movesW ? start.x + start.w : start.x;
    const ay = movesN ? start.y + start.h : start.y;
    const availW = movesW ? ax - bx0 : bx1 - ax;
    const availH = movesN ? ay - by0 : by1 - ay;
    let w = right - left;
    const h = bottom - top;
    if (w / ratio < h) w = h * ratio;
    w = Math.min(Math.max(w, minW), Math.min(availW, availH * ratio));
    const nh = w / ratio;
    return { x: movesW ? ax - w : ax, y: movesN ? ay - nh : ay, w, h: nh };
  }
  if (movesW || movesE) {
    const ax = movesW ? start.x + start.w : start.x;
    const cy = start.y + start.h / 2;
    const availW = movesW ? ax - bx0 : bx1 - ax;
    const maxH = 2 * Math.min(cy - by0, by1 - cy);
    const w = Math.min(Math.max(right - left, minW), Math.min(availW, maxH * ratio));
    const h = w / ratio;
    return { x: movesW ? ax - w : ax, y: cy - h / 2, w, h };
  }
  const ay = movesN ? start.y + start.h : start.y;
  const cx = start.x + start.w / 2;
  const availH = movesN ? ay - by0 : by1 - ay;
  const maxW = 2 * Math.min(cx - bx0, bx1 - cx);
  const h = Math.min(Math.max(bottom - top, minH), Math.min(availH, maxW / ratio));
  const w = h * ratio;
  return { x: cx - w / 2, y: movesN ? ay - h : ay, w, h };
}

export function sameRect(a: Rect, b: Rect): boolean {
  return Math.round(a.x) === Math.round(b.x) && Math.round(a.y) === Math.round(b.y) && Math.round(a.w) === Math.round(b.w) && Math.round(a.h) === Math.round(b.h);
}
