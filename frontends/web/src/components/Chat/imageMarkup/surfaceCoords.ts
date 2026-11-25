import type { MarkupDoc, Point } from './model';

/**
 * Maps a viewport point to image pixels. The overlay SVG has a viewBox of the
 * image's size, so its screen CTM answers; jsdom has none, so the element's
 * bounding box stands in. A box with no size maps 1:1, which lets a test drive
 * the editor in image coordinates directly.
 */
export function clientToImage(el: Element | null, clientX: number, clientY: number, size: Pick<MarkupDoc, 'width' | 'height'>): Point {
  if (!el) return { x: clientX, y: clientY };
  const svg = el as Partial<SVGGraphicsElement>;
  if (typeof svg.getScreenCTM === 'function') {
    const ctm = svg.getScreenCTM();
    if (ctm) {
      const inv = ctm.inverse();
      return { x: inv.a * clientX + inv.c * clientY + inv.e, y: inv.b * clientX + inv.d * clientY + inv.f };
    }
  }
  const box = el.getBoundingClientRect();
  if (box.width > 0 && box.height > 0) {
    return { x: ((clientX - box.left) / box.width) * size.width, y: ((clientY - box.top) / box.height) * size.height };
  }
  return { x: clientX, y: clientY };
}

export function clampPoint(p: Point, size: Pick<MarkupDoc, 'width' | 'height'>): Point {
  return { x: Math.max(0, Math.min(size.width, p.x)), y: Math.max(0, Math.min(size.height, p.y)) };
}

/** Handle every editing layer exposes, so the editor can cancel the innermost one. */
export interface LayerHandle {
  /** Cancel the layer's innermost transient state; false when it had none. */
  cancel(): boolean;
}
