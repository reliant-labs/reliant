/**
 * Canvas2D renderer for a `MarkupDoc`: the export path. It must look the same
 * as the SVG editor surface, so every size comes from model.ts and strokes are
 * traced with the same curve as `strokePath`.
 */
import {
  arrowHead,
  pinRadius,
  PIN_FILL,
  PIN_TEXT,
  type CommentPin,
  type MarkupDoc,
  type Point,
  type Shape,
  type TextShape,
} from './model';

type Ctx2D = CanvasRenderingContext2D | OffscreenCanvasRenderingContext2D;

/** The server rejects uploads over 10MB; stay clear of it whichever MB is meant. */
export const MAX_EXPORT_BYTES = 9_500_000;
const JPEG_QUALITY = 0.9;
const DOWNSCALE_STEP = 0.75;
const TEXT_LINE_HEIGHT = 1.25;
const TEXT_FONT_FAMILY = 'ui-sans-serif, system-ui, sans-serif';

/** Adds the stroke's curve to the current path (no beginPath): quadratic through midpoints, like `strokePath`. */
export function traceStroke(ctx: Ctx2D, points: Point[]): void {
  if (points.length === 0) return;
  ctx.moveTo(points[0].x, points[0].y);
  if (points.length === 1) {
    ctx.lineTo(points[0].x, points[0].y);
    return;
  }
  for (let i = 1; i < points.length - 1; i++) {
    const mid = { x: (points[i].x + points[i + 1].x) / 2, y: (points[i].y + points[i + 1].y) / 2 };
    ctx.quadraticCurveTo(points[i].x, points[i].y, mid.x, mid.y);
  }
  const last = points[points.length - 1];
  ctx.lineTo(last.x, last.y);
}

function strokeStyle(ctx: Ctx2D, color: string, size: number): void {
  ctx.strokeStyle = color;
  ctx.lineWidth = size;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
}

function drawText(ctx: Ctx2D, shape: TextShape): void {
  ctx.save();
  ctx.font = `${shape.fontSize}px ${TEXT_FONT_FAMILY}`;
  ctx.textBaseline = 'top';
  ctx.textAlign = 'left';
  ctx.fillStyle = shape.color;
  ctx.shadowColor = 'rgba(0,0,0,.6)';
  ctx.shadowBlur = shape.fontSize / 6;
  ctx.shadowOffsetX = 0;
  ctx.shadowOffsetY = 0;
  shape.text.split('\n').forEach((line, i) => {
    ctx.fillText(line, shape.x, shape.y + i * shape.fontSize * TEXT_LINE_HEIGHT);
  });
  ctx.restore();
}

function drawPin(ctx: Ctx2D, pin: CommentPin, doc: MarkupDoc): void {
  const r = pinRadius(doc);
  ctx.save();
  ctx.beginPath();
  ctx.arc(pin.x, pin.y, r, 0, Math.PI * 2);
  ctx.fillStyle = PIN_FILL;
  ctx.fill();
  ctx.lineWidth = 2;
  ctx.strokeStyle = '#ffffff';
  ctx.stroke();
  ctx.fillStyle = PIN_TEXT;
  ctx.font = `bold ${Math.round(r)}px ${TEXT_FONT_FAMILY}`;
  ctx.textAlign = 'center';
  ctx.textBaseline = 'middle';
  ctx.fillText(String(pin.n), pin.x, pin.y);
  ctx.restore();
}

function drawShape(ctx: Ctx2D, shape: Shape, doc: MarkupDoc): void {
  switch (shape.kind) {
    case 'stroke':
      ctx.beginPath();
      strokeStyle(ctx, shape.color, shape.size);
      traceStroke(ctx, shape.points);
      ctx.stroke();
      return;
    case 'rect':
      strokeStyle(ctx, shape.color, shape.size);
      ctx.strokeRect(shape.x, shape.y, shape.w, shape.h);
      return;
    case 'ellipse':
      ctx.beginPath();
      strokeStyle(ctx, shape.color, shape.size);
      ctx.ellipse(shape.x + shape.w / 2, shape.y + shape.h / 2, shape.w / 2, shape.h / 2, 0, 0, Math.PI * 2);
      ctx.stroke();
      return;
    case 'arrow': {
      const [a, b] = arrowHead(shape.from, shape.to, shape.size);
      ctx.beginPath();
      strokeStyle(ctx, shape.color, shape.size);
      ctx.moveTo(shape.from.x, shape.from.y);
      ctx.lineTo(shape.to.x, shape.to.y);
      ctx.moveTo(shape.to.x, shape.to.y);
      ctx.lineTo(a.x, a.y);
      ctx.moveTo(shape.to.x, shape.to.y);
      ctx.lineTo(b.x, b.y);
      ctx.stroke();
      return;
    }
    case 'text':
      drawText(ctx, shape);
      return;
    case 'pin':
      drawPin(ctx, shape, doc);
      return;
  }
}

/** Draws every shape in image pixels, in order; the caller sets any transform. */
export function drawDoc(ctx: Ctx2D, doc: MarkupDoc): void {
  for (const shape of doc.shapes) {
    ctx.save();
    drawShape(ctx, shape, doc);
    ctx.restore();
  }
}

// ── flatten ───────────────────────────────────────────────────────────────

type ExportCanvas = {
  ctx: Ctx2D;
  toBlob: (type: string, quality?: number) => Promise<Blob | null>;
};

function createExportCanvas(width: number, height: number): ExportCanvas {
  if (typeof OffscreenCanvas !== 'undefined') {
    const canvas = new OffscreenCanvas(width, height);
    const ctx = canvas.getContext('2d');
    if (ctx) {
      return { ctx, toBlob: (type, quality) => canvas.convertToBlob({ type, quality }) };
    }
  }
  const canvas = document.createElement('canvas');
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext('2d');
  if (!ctx) throw new Error('Could not create a canvas to export the image');
  return {
    ctx,
    toBlob: (type, quality) => new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, type, quality)),
  };
}

function markedName(filename: string, type: string): string {
  const ext = type === 'image/jpeg' ? 'jpg' : 'png';
  const base = filename.replace(/\.[^./\\]+$/, '').replace(/(-marked)+$/, '') || 'image';
  return `${base}-marked.${ext}`;
}

/**
 * Burns the markup (and crop) into the image and returns it as a new file:
 * PNG, or JPEG / a smaller JPEG when PNG would exceed the upload cap.
 */
export async function flattenImage(source: Blob, doc: MarkupDoc, filename: string): Promise<File> {
  let bitmap: ImageBitmap;
  try {
    bitmap = await createImageBitmap(source);
  } catch (err) {
    throw new Error(`Could not decode the image: ${err instanceof Error ? err.message : String(err)}`);
  }
  try {
    const region = doc.crop ?? { x: 0, y: 0, w: doc.width, h: doc.height };

    const render = async (scale: number, type: string, quality?: number): Promise<Blob> => {
      const width = Math.max(1, Math.round(region.w * scale));
      const height = Math.max(1, Math.round(region.h * scale));
      const { ctx, toBlob } = createExportCanvas(width, height);
      if (type === 'image/jpeg') {
        // JPEG has no alpha: transparent pixels would turn black.
        ctx.fillStyle = '#ffffff';
        ctx.fillRect(0, 0, width, height);
      }
      ctx.save();
      ctx.scale(scale, scale);
      ctx.translate(-region.x, -region.y);
      ctx.drawImage(bitmap, 0, 0);
      drawDoc(ctx, doc);
      ctx.restore();
      const blob = await toBlob(type, quality);
      if (!blob) throw new Error('Could not encode the image');
      return blob;
    };

    let blob = await render(1, 'image/png');
    if (blob.size > MAX_EXPORT_BYTES) {
      let scale = 1;
      blob = await render(scale, 'image/jpeg', JPEG_QUALITY);
      while (blob.size > MAX_EXPORT_BYTES) {
        scale *= DOWNSCALE_STEP;
        if (Math.min(region.w, region.h) * scale < 1) throw new Error('The image is too large to attach');
        blob = await render(scale, 'image/jpeg', JPEG_QUALITY);
      }
    }
    const type = blob.type === 'image/jpeg' ? 'image/jpeg' : 'image/png';
    return new File([blob], markedName(filename, type), { type });
  } finally {
    bitmap.close();
  }
}
