import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { drawDoc, flattenImage, MAX_EXPORT_BYTES, traceStroke } from '../imageMarkup/renderCanvas';
import { arrowHead, emptyDoc, PIN_FILL, PIN_TEXT, pinRadius, type MarkupDoc, type Shape } from '../imageMarkup/model';

type Op = [kind: 'call' | 'set', name: string, ...rest: unknown[]];

/** A 2D context that records every method call and property assignment in order. */
function recordingCtx() {
  const ops: Op[] = [];
  const target: Record<string, unknown> = {};
  const ctx = new Proxy(target, {
    get(t, prop: string) {
      if (prop in t) return t[prop];
      return (...args: unknown[]) => {
        ops.push(['call', prop, ...args]);
      };
    },
    set(t, prop: string, value) {
      t[prop] = value;
      ops.push(['set', prop, value]);
      return true;
    },
  }) as unknown as CanvasRenderingContext2D;
  const calls = (name: string) => ops.filter((o) => o[0] === 'call' && o[1] === name).map((o) => o.slice(2));
  const sets = (name: string) => ops.filter((o) => o[0] === 'set' && o[1] === name).map((o) => o[2]);
  return { ctx, ops, calls, sets };
}

const doc = (shapes: Shape[], w = 1000, h = 800): MarkupDoc => ({ ...emptyDoc(w, h), shapes });

describe('traceStroke', () => {
  it('draws nothing for no points', () => {
    const { ctx, ops } = recordingCtx();
    traceStroke(ctx, []);
    expect(ops).toEqual([]);
  });

  it('draws a zero-length line for one point', () => {
    const { ctx, calls } = recordingCtx();
    traceStroke(ctx, [{ x: 3, y: 4 }]);
    expect(calls('moveTo')).toEqual([[3, 4]]);
    expect(calls('lineTo')).toEqual([[3, 4]]);
  });

  it('follows the same quadratic-through-midpoints curve as strokePath', () => {
    const { ctx, calls } = recordingCtx();
    traceStroke(ctx, [{ x: 0, y: 0 }, { x: 10, y: 0 }, { x: 10, y: 10 }, { x: 20, y: 10 }]);
    expect(calls('moveTo')).toEqual([[0, 0]]);
    expect(calls('quadraticCurveTo')).toEqual([
      [10, 0, 10, 5],
      [10, 10, 15, 10],
    ]);
    expect(calls('lineTo')).toEqual([[20, 10]]);
  });
});

describe('drawDoc', () => {
  it('strokes a freehand path with round caps and joins at its size', () => {
    const { ctx, calls, sets } = recordingCtx();
    drawDoc(ctx, doc([{ kind: 'stroke', id: 's', color: '#ef4444', size: 5, points: [{ x: 0, y: 0 }, { x: 10, y: 10 }] }]));
    expect(sets('strokeStyle')).toEqual(['#ef4444']);
    expect(sets('lineWidth')).toEqual([5]);
    expect(sets('lineCap')).toEqual(['round']);
    expect(sets('lineJoin')).toEqual(['round']);
    expect(calls('beginPath')).toHaveLength(1);
    expect(calls('stroke')).toHaveLength(1);
  });

  it('outlines a rect, never fills it', () => {
    const { ctx, calls, sets } = recordingCtx();
    drawDoc(ctx, doc([{ kind: 'rect', id: 'r', color: '#fff', size: 3, x: 10, y: 20, w: 30, h: 40 }]));
    expect(calls('strokeRect')).toEqual([[10, 20, 30, 40]]);
    expect(calls('fill')).toHaveLength(0);
    expect(calls('fillRect')).toHaveLength(0);
    expect(sets('lineWidth')).toEqual([3]);
  });

  it('outlines an ellipse from its bounding box', () => {
    const { ctx, calls } = recordingCtx();
    drawDoc(ctx, doc([{ kind: 'ellipse', id: 'e', color: '#fff', size: 3, x: 10, y: 20, w: 30, h: 40 }]));
    expect(calls('ellipse')).toEqual([[25, 40, 15, 20, 0, 0, Math.PI * 2]]);
    expect(calls('stroke')).toHaveLength(1);
    expect(calls('fill')).toHaveLength(0);
  });

  it('draws an arrow as a shaft plus two barbs from arrowHead', () => {
    const { ctx, calls, sets } = recordingCtx();
    const from = { x: 0, y: 0 };
    const to = { x: 100, y: 50 };
    drawDoc(ctx, doc([{ kind: 'arrow', id: 'a', color: '#3b82f6', size: 4, from, to }]));
    const [a, b] = arrowHead(from, to, 4);
    expect(calls('moveTo')).toEqual([[0, 0], [100, 50], [100, 50]]);
    expect(calls('lineTo')).toEqual([[100, 50], [a.x, a.y], [b.x, b.y]]);
    expect(calls('stroke')).toHaveLength(1);
    expect(sets('lineCap')).toEqual(['round']);
  });

  it('draws text line by line at 1.25 line height, top-aligned, with a dark shadow', () => {
    const { ctx, calls, sets } = recordingCtx();
    drawDoc(ctx, doc([{ kind: 'text', id: 't', color: '#eab308', fontSize: 24, x: 50, y: 60, text: 'one\ntwo' }]));
    expect(sets('font')).toEqual(['24px ui-sans-serif, system-ui, sans-serif']);
    expect(sets('textBaseline')).toEqual(['top']);
    expect(sets('fillStyle')).toEqual(['#eab308']);
    expect(sets('shadowColor')).toEqual(['rgba(0,0,0,.6)']);
    expect(sets('shadowBlur')).toEqual([4]);
    expect(calls('fillText')).toEqual([
      ['one', 50, 60],
      ['two', 50, 90],
    ]);
  });

  it('draws a pin as a filled violet circle with a 2px white ring and a centred bold number', () => {
    const { ctx, calls, sets } = recordingCtx();
    const d = doc([{ kind: 'pin', id: 'p', n: 3, x: 200, y: 300, text: 'x' }]);
    drawDoc(ctx, d);
    const r = pinRadius(d);
    expect(calls('arc')).toEqual([[200, 300, r, 0, Math.PI * 2]]);
    expect(sets('fillStyle')).toEqual([PIN_FILL, PIN_TEXT]);
    expect(sets('strokeStyle')).toEqual(['#ffffff']);
    expect(sets('lineWidth')).toEqual([2]);
    expect(sets('font')).toEqual([`bold ${Math.round(r)}px ui-sans-serif, system-ui, sans-serif`]);
    expect(sets('textAlign')).toEqual(['center']);
    expect(sets('textBaseline')).toEqual(['middle']);
    expect(calls('fillText')).toEqual([['3', 200, 300]]);
    expect(calls('fill')).toHaveLength(1);
    expect(calls('stroke')).toHaveLength(1);
  });

  it('isolates each shape with save/restore and paints in order', () => {
    const { ctx, ops, calls } = recordingCtx();
    drawDoc(
      ctx,
      doc([
        { kind: 'rect', id: 'r1', color: '#111', size: 1, x: 1, y: 1, w: 1, h: 1 },
        { kind: 'rect', id: 'r2', color: '#222', size: 1, x: 2, y: 2, w: 2, h: 2 },
      ]),
    );
    expect(calls('save')).toHaveLength(2);
    expect(calls('restore')).toHaveLength(2);
    expect(calls('strokeRect')).toEqual([[1, 1, 1, 1], [2, 2, 2, 2]]);
    expect(ops[0]).toEqual(['call', 'save']);
  });

  it('draws nothing for an empty doc', () => {
    const { ctx, ops } = recordingCtx();
    drawDoc(ctx, doc([]));
    expect(ops).toEqual([]);
  });
});

describe('flattenImage', () => {
  let canvases: { canvas: HTMLCanvasElement; ops: Op[] }[];
  let sizes: number[];
  let types: string[];
  let bitmap: { close: ReturnType<typeof vi.fn> };

  /** Make toBlob return blobs of the given sizes in call order (the last repeats). */
  function stubCanvas(blobSizes: number[]) {
    canvases = [];
    types = [];
    sizes = [...blobSizes];
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(function (this: HTMLCanvasElement) {
      const rec = recordingCtx();
      canvases.push({ canvas: this, ops: rec.ops });
      return rec.ctx as never;
    } as never);
    vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(function (cb: BlobCallback, type?: string) {
      types.push(type ?? '');
      const n = sizes.length > 1 ? sizes.shift()! : sizes[0];
      cb(new Blob([new Uint8Array(n)], { type }));
    } as never);
  }
  const dims = (i: number) => {
    const { canvas } = canvases[i];
    return [canvas.width, canvas.height];
  };

  beforeEach(() => {
    bitmap = { close: vi.fn() };
    vi.stubGlobal('createImageBitmap', vi.fn(async () => bitmap));
  });
  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  const source = new Blob(['x'], { type: 'image/png' });

  it('exports a PNG sized to the image, drawn with the bitmap and the markup', async () => {
    stubCanvas([1234]);
    const d = doc([{ kind: 'rect', id: 'r', color: '#f00', size: 2, x: 1, y: 2, w: 3, h: 4 }], 640, 480);
    const file = await flattenImage(source, d, 'shot.png');
    expect(createImageBitmap).toHaveBeenCalledWith(source);
    expect(dims(0)).toEqual([640, 480]);
    expect(types).toEqual(['image/png']);
    expect(file).toBeInstanceOf(File);
    expect(file.name).toBe('shot-marked.png');
    expect(file.type).toBe('image/png');
    expect(file.size).toBe(1234);
    const ops = canvases[0].ops;
    expect(ops).toContainEqual(['call', 'drawImage', bitmap, 0, 0]);
    expect(ops).toContainEqual(['call', 'strokeRect', 1, 2, 3, 4]);
    expect(ops.findIndex((o) => o[1] === 'drawImage')).toBeLessThan(ops.findIndex((o) => o[1] === 'strokeRect'));
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });

  it('sizes the canvas to the crop and shifts the image by its origin', async () => {
    stubCanvas([100]);
    const d: MarkupDoc = { ...emptyDoc(1000, 800), crop: { x: 100, y: 50, w: 300, h: 200 } };
    await flattenImage(source, d, 'a.png');
    expect(dims(0)).toEqual([300, 200]);
    expect(canvases[0].ops).toContainEqual(['call', 'translate', -100, -50]);
  });

  it('does not stack the -marked suffix', async () => {
    stubCanvas([100]);
    expect((await flattenImage(source, emptyDoc(10, 10), 'x-marked.png')).name).toBe('x-marked.png');
    expect((await flattenImage(source, emptyDoc(10, 10), 'x-marked-marked.jpg')).name).toBe('x-marked.png');
    expect((await flattenImage(source, emptyDoc(10, 10), 'noext')).name).toBe('noext-marked.png');
  });

  it('retries as JPEG when the PNG is over the cap', async () => {
    stubCanvas([MAX_EXPORT_BYTES + 1, 2000]);
    const file = await flattenImage(source, emptyDoc(100, 100), 'big.png');
    expect(types).toEqual(['image/png', 'image/jpeg']);
    expect(file.type).toBe('image/jpeg');
    expect(file.name).toBe('big-marked.jpg');
    expect(dims(1)).toEqual([100, 100]);
  });

  it('downscales by 0.75 steps until the JPEG fits', async () => {
    stubCanvas([MAX_EXPORT_BYTES + 1, MAX_EXPORT_BYTES + 1, MAX_EXPORT_BYTES + 1, 5000]);
    const file = await flattenImage(source, emptyDoc(1000, 800), 'big.png');
    expect(types).toEqual(['image/png', 'image/jpeg', 'image/jpeg', 'image/jpeg']);
    expect(dims(0)).toEqual([1000, 800]);
    expect(dims(1)).toEqual([1000, 800]);
    expect(dims(2)).toEqual([750, 600]);
    expect(dims(3)).toEqual([563, 450]);
    expect(file.type).toBe('image/jpeg');
    expect(file.size).toBe(5000);
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });

  it('gives up with a clear error when it can never fit, and still releases the bitmap', async () => {
    stubCanvas([MAX_EXPORT_BYTES + 1]);
    await expect(flattenImage(source, emptyDoc(8, 8), 'x.png')).rejects.toThrow(/too large/);
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });

  it('throws a clear error when decoding fails', async () => {
    vi.stubGlobal('createImageBitmap', vi.fn(async () => { throw new Error('bad data'); }));
    await expect(flattenImage(source, emptyDoc(10, 10), 'x.png')).rejects.toThrow(/Could not decode the image: bad data/);
  });

  it('throws when toBlob yields nothing, and still releases the bitmap', async () => {
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation((() => recordingCtx().ctx) as never);
    vi.spyOn(HTMLCanvasElement.prototype, 'toBlob').mockImplementation(((cb: BlobCallback) => cb(null)) as never);
    await expect(flattenImage(source, emptyDoc(10, 10), 'x.png')).rejects.toThrow(/Could not encode/);
    expect(bitmap.close).toHaveBeenCalledTimes(1);
  });
});
