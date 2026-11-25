import { describe, expect, it } from 'vitest';
import {
  arrowHead,
  boxFromPoints,
  canRedo,
  canUndo,
  createHistory,
  emptyDoc,
  hasMarkup,
  hitTest,
  isDirty,
  markupReducer,
  nextPinNumber,
  normalizeCrop,
  pinsOf,
  shapeBounds,
  strokePath,
  translateShape,
  type ArrowShape,
  type BoxShape,
  type CommentPin,
  type MarkupAction,
  type MarkupDoc,
  type MarkupHistory,
  type StrokeShape,
  type TextShape,
} from '../imageMarkup/model';

const doc0 = emptyDoc(1000, 800);
const rect = (id: string, x = 100, y = 100, w = 200, h = 100): BoxShape => ({ kind: 'rect', id, color: '#f00', size: 4, x, y, w, h });
const pin = (id: string, n: number, x = 50, y = 50, text = ''): CommentPin => ({ kind: 'pin', id, n, x, y, text });

function run(h: MarkupHistory, ...actions: MarkupAction[]): MarkupHistory {
  return actions.reduce(markupReducer, h);
}

describe('markupReducer', () => {
  it('adds, updates, translates and removes shapes', () => {
    let h = createHistory(doc0);
    h = run(h, { type: 'add', shape: rect('a') });
    expect(h.present.shapes).toHaveLength(1);

    h = run(h, { type: 'update', id: 'a', patch: { color: '#0f0' } });
    expect((h.present.shapes[0] as BoxShape).color).toBe('#0f0');

    h = run(h, { type: 'translate', id: 'a', dx: 10, dy: -5 });
    expect(h.present.shapes[0]).toMatchObject({ x: 110, y: 95 });

    h = run(h, { type: 'remove', ids: ['a'] });
    expect(h.present.shapes).toHaveLength(0);
  });

  it('never lets update change a shape kind or id', () => {
    let h = run(createHistory(doc0), { type: 'add', shape: rect('a') });
    h = run(h, { type: 'update', id: 'a', patch: { kind: 'ellipse', id: 'zzz' } as never });
    expect(h.present.shapes[0]).toMatchObject({ kind: 'rect', id: 'a' });
  });

  it('is a no-op (same history object) for unknown ids', () => {
    const h = run(createHistory(doc0), { type: 'add', shape: rect('a') });
    expect(markupReducer(h, { type: 'update', id: 'nope', patch: { color: '#000' } })).toBe(h);
    expect(markupReducer(h, { type: 'translate', id: 'nope', dx: 1, dy: 1 })).toBe(h);
    expect(markupReducer(h, { type: 'remove', ids: ['nope'] })).toBe(h);
  });

  it('renumbers pins after removing one', () => {
    let h = createHistory(doc0);
    h = run(h, { type: 'add', shape: pin('p1', 1) }, { type: 'add', shape: pin('p2', 2) }, { type: 'add', shape: pin('p3', 3) });
    h = run(h, { type: 'remove', ids: ['p2'] });
    expect(pinsOf(h.present).map((p) => [p.id, p.n])).toEqual([
      ['p1', 1],
      ['p3', 2],
    ]);
    expect(nextPinNumber(h.present)).toBe(3);
  });

  it('keeps non-pin shapes untouched when pins are renumbered', () => {
    let h = createHistory(doc0);
    h = run(h, { type: 'add', shape: pin('p1', 1) }, { type: 'add', shape: rect('r') }, { type: 'add', shape: pin('p2', 2) });
    h = run(h, { type: 'remove', ids: ['p1'] });
    expect(h.present.shapes.map((s) => s.id)).toEqual(['r', 'p2']);
    expect(pinsOf(h.present)[0].n).toBe(1);
  });

  it('coalesces edits with the same key into one undo step until commit', () => {
    let h = run(createHistory(doc0), { type: 'add', shape: rect('a') });
    h = run(
      h,
      { type: 'translate', id: 'a', dx: 1, dy: 0, coalesce: 'drag' },
      { type: 'translate', id: 'a', dx: 1, dy: 0, coalesce: 'drag' },
      { type: 'translate', id: 'a', dx: 1, dy: 0, coalesce: 'drag' },
    );
    expect(h.past).toHaveLength(2); // the add, and one drag step
    expect(h.present.shapes[0]).toMatchObject({ x: 103 });

    h = run(h, { type: 'commit' }, { type: 'translate', id: 'a', dx: 1, dy: 0, coalesce: 'drag' });
    expect(h.past).toHaveLength(3);

    h = run(h, { type: 'undo' });
    expect(h.present.shapes[0]).toMatchObject({ x: 103 });
  });

  it('commit is a no-op without an open run', () => {
    const h = run(createHistory(doc0), { type: 'add', shape: rect('a') });
    expect(markupReducer(h, { type: 'commit' })).toBe(h);
  });

  it('a different coalesce key starts a new step', () => {
    let h = run(createHistory(doc0), { type: 'add', shape: rect('a') });
    h = run(
      h,
      { type: 'update', id: 'a', patch: { color: '#111' }, coalesce: 'x' },
      { type: 'update', id: 'a', patch: { color: '#222' }, coalesce: 'y' },
    );
    expect(h.past).toHaveLength(3);
  });

  it('undoes and redoes, and a new edit clears redo', () => {
    let h = createHistory(doc0);
    expect(canUndo(h)).toBe(false);
    expect(canRedo(h)).toBe(false);
    expect(isDirty(h)).toBe(false);

    h = run(h, { type: 'add', shape: rect('a') }, { type: 'add', shape: rect('b') });
    expect(isDirty(h)).toBe(true);
    h = run(h, { type: 'undo' });
    expect(h.present.shapes.map((s) => s.id)).toEqual(['a']);
    expect(canRedo(h)).toBe(true);

    h = run(h, { type: 'redo' });
    expect(h.present.shapes.map((s) => s.id)).toEqual(['a', 'b']);
    expect(canRedo(h)).toBe(false);

    h = run(h, { type: 'undo' }, { type: 'add', shape: rect('c') });
    expect(canRedo(h)).toBe(false);
    expect(h.present.shapes.map((s) => s.id)).toEqual(['a', 'c']);
  });

  it('undo/redo with nothing to do return the same history', () => {
    const h = createHistory(doc0);
    expect(markupReducer(h, { type: 'undo' })).toBe(h);
    expect(markupReducer(h, { type: 'redo' })).toBe(h);
  });

  it('reset replaces the doc as one undoable step', () => {
    let h = run(createHistory(doc0), { type: 'add', shape: rect('a') });
    const replacement: MarkupDoc = { ...doc0, shapes: [rect('z')] };
    h = run(h, { type: 'reset', doc: replacement });
    expect(h.present).toBe(replacement);
    h = run(h, { type: 'undo' });
    expect(h.present.shapes.map((s) => s.id)).toEqual(['a']);
  });

  it('setCrop normalizes the crop and clears it when it covers the image', () => {
    let h = run(createHistory(doc0), { type: 'setCrop', crop: { x: -20, y: 10.4, w: 5000, h: 300.6 } });
    expect(h.present.crop).toEqual({ x: 0, y: 10, w: 1000, h: 301 });
    expect(hasMarkup(h.present)).toBe(true);

    h = run(h, { type: 'setCrop', crop: { x: 0, y: 0, w: 1000, h: 800 } });
    expect(h.present.crop).toBeUndefined();
    expect(hasMarkup(h.present)).toBe(false);
  });

  it('setCrop coalesces a drag', () => {
    let h = createHistory(doc0);
    h = run(
      h,
      { type: 'setCrop', crop: { x: 10, y: 10, w: 400, h: 300 }, coalesce: 'crop' },
      { type: 'setCrop', crop: { x: 20, y: 20, w: 400, h: 300 }, coalesce: 'crop' },
    );
    expect(h.past).toHaveLength(1);
    expect(h.present.crop).toEqual({ x: 20, y: 20, w: 400, h: 300 });
  });
});

describe('normalizeCrop', () => {
  it('enforces a 16px minimum and stays inside the image', () => {
    expect(normalizeCrop({ x: 995, y: 795, w: 2, h: 2 }, doc0)).toEqual({ x: 984, y: 784, w: 16, h: 16 });
  });
  it('passes undefined through', () => {
    expect(normalizeCrop(undefined, doc0)).toBeUndefined();
  });
});

describe('hitTest', () => {
  const stroke: StrokeShape = {
    kind: 'stroke',
    id: 's',
    color: '#f00',
    size: 6,
    points: [
      { x: 0, y: 0 },
      { x: 100, y: 0 },
      { x: 100, y: 100 },
    ],
  };
  const doc = (...shapes: MarkupDoc['shapes']): MarkupDoc => ({ ...doc0, shapes });

  it('hits strokes along the path, not away from it', () => {
    const d = doc(stroke);
    expect(hitTest(d, { x: 50, y: 2 }, 4)?.id).toBe('s');
    expect(hitTest(d, { x: 100, y: 60 }, 4)?.id).toBe('s');
    expect(hitTest(d, { x: 50, y: 50 }, 4)).toBeUndefined();
  });

  it('hits a single-point stroke (a dot)', () => {
    const dot: StrokeShape = { ...stroke, points: [{ x: 10, y: 10 }] };
    expect(hitTest(doc(dot), { x: 12, y: 10 }, 2)?.id).toBe('s');
    expect(hitTest(doc(dot), { x: 40, y: 10 }, 2)).toBeUndefined();
  });

  it('hits a rect on its outline but not in its empty interior', () => {
    const d = doc(rect('r', 100, 100, 200, 100));
    expect(hitTest(d, { x: 100, y: 150 }, 3)?.id).toBe('r');
    expect(hitTest(d, { x: 200, y: 100 }, 3)?.id).toBe('r');
    expect(hitTest(d, { x: 200, y: 150 }, 3)).toBeUndefined();
    expect(hitTest(d, { x: 10, y: 10 }, 3)).toBeUndefined();
  });

  it('hits an ellipse on its outline but not at its centre', () => {
    const e: BoxShape = { kind: 'ellipse', id: 'e', color: '#f00', size: 4, x: 100, y: 100, w: 200, h: 100 };
    const d = doc(e);
    expect(hitTest(d, { x: 100, y: 150 }, 3)?.id).toBe('e'); // left extreme
    expect(hitTest(d, { x: 200, y: 100 }, 3)?.id).toBe('e'); // top extreme
    expect(hitTest(d, { x: 200, y: 150 }, 3)).toBeUndefined(); // centre
    expect(hitTest(d, { x: 500, y: 500 }, 3)).toBeUndefined();
  });

  it('hits an arrow along its shaft', () => {
    const a: ArrowShape = { kind: 'arrow', id: 'a', color: '#f00', size: 4, from: { x: 0, y: 0 }, to: { x: 200, y: 200 } };
    expect(hitTest(doc(a), { x: 100, y: 101 }, 3)?.id).toBe('a');
    expect(hitTest(doc(a), { x: 100, y: 140 }, 3)).toBeUndefined();
  });

  it('hits text anywhere in its box', () => {
    const t: TextShape = { kind: 'text', id: 't', color: '#fff', fontSize: 20, x: 100, y: 100, text: 'hello\nworld!' };
    const d = doc(t);
    expect(hitTest(d, { x: 110, y: 110 }, 0)?.id).toBe('t');
    expect(hitTest(d, { x: 110, y: 130 }, 0)?.id).toBe('t'); // second line
    expect(hitTest(d, { x: 400, y: 400 }, 0)).toBeUndefined();
  });

  it('hits a pin within its radius box', () => {
    const d = doc(pin('p', 1, 300, 300));
    expect(hitTest(d, { x: 305, y: 295 }, 0)?.id).toBe('p');
    expect(hitTest(d, { x: 400, y: 300 }, 0)).toBeUndefined();
  });

  it('returns the top-most shape when several overlap', () => {
    const d = doc(rect('under', 100, 100, 200, 100), rect('over', 100, 100, 200, 100));
    expect(hitTest(d, { x: 100, y: 150 }, 3)?.id).toBe('over');
  });
});

describe('geometry helpers', () => {
  it('translateShape moves every kind', () => {
    const stroke: StrokeShape = { kind: 'stroke', id: 's', color: '#f00', size: 2, points: [{ x: 1, y: 1 }, { x: 2, y: 2 }] };
    const arrow: ArrowShape = { kind: 'arrow', id: 'a', color: '#f00', size: 2, from: { x: 1, y: 1 }, to: { x: 5, y: 5 } };
    expect(translateShape(stroke, 10, 20)).toMatchObject({ points: [{ x: 11, y: 21 }, { x: 12, y: 22 }] });
    expect(translateShape(arrow, 10, 20)).toMatchObject({ from: { x: 11, y: 21 }, to: { x: 15, y: 25 } });
    expect(translateShape(pin('p', 1, 5, 5), 1, 2)).toMatchObject({ x: 6, y: 7 });
  });

  it('boxFromPoints works in any drag direction', () => {
    expect(boxFromPoints({ x: 10, y: 50 }, { x: 4, y: 20 })).toEqual({ x: 4, y: 20, w: 6, h: 30 });
  });

  it('shapeBounds covers a pin by its radius', () => {
    const b = shapeBounds(pin('p', 1, 100, 100), doc0);
    expect(b.w).toBe(b.h);
    expect(b.x + b.w / 2).toBe(100);
  });

  it('arrowHead barbs are symmetric about the shaft and sit behind the tip', () => {
    const [a, b] = arrowHead({ x: 0, y: 0 }, { x: 100, y: 0 }, 4);
    expect(a.x).toBeCloseTo(b.x);
    expect(a.y).toBeCloseTo(-b.y);
    expect(a.x).toBeLessThan(100);
  });
});

describe('strokePath', () => {
  it('is empty for no points', () => {
    expect(strokePath([])).toBe('');
  });
  it('draws a dot for one point', () => {
    expect(strokePath([{ x: 5, y: 6 }])).toBe('M5 6L5 6');
  });
  it('is a straight line for two points', () => {
    expect(strokePath([{ x: 0, y: 0 }, { x: 10, y: 4 }])).toBe('M0 0L10 4');
  });
  it('curves through midpoints for three or more', () => {
    const d = strokePath([{ x: 0, y: 0 }, { x: 10, y: 0 }, { x: 10, y: 10 }, { x: 20, y: 10 }]);
    expect(d).toBe('M0 0Q10 0 10 5Q10 10 15 10L20 10');
  });
  it('rounds to two decimals', () => {
    expect(strokePath([{ x: 0.12345, y: 0 }, { x: 1, y: 1 }])).toBe('M0.12 0L1 1');
  });
});
