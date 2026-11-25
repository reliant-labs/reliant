import { createRef } from 'react';
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest';
import { act, cleanup, fireEvent, render, screen, within } from '@testing-library/react';
import { MarkupEditor, type EditorMode, type MarkupEditorHandle } from '../imageMarkup/MarkupEditor';
import { createHistory, markupReducer, type MarkupDoc } from '../imageMarkup/model';
import { aspectRatio, resizeCrop } from '../imageMarkup/cropMath';

// jsdom ships no PointerEvent; React's pointer handlers listen for the event by name.
beforeAll(() => {
  if (typeof window.PointerEvent === 'undefined') {
    class TestPointerEvent extends MouseEvent {
      pointerId: number;
      pointerType: string;
      constructor(type: string, init: PointerEventInit = {}) {
        super(type, init);
        this.pointerId = init.pointerId ?? 1;
        this.pointerType = init.pointerType ?? 'mouse';
      }
    }
    (window as unknown as { PointerEvent: unknown }).PointerEvent = TestPointerEvent;
  }
});

afterEach(cleanup);

interface Harness {
  onModeChange: ReturnType<typeof vi.fn>;
  onSave: ReturnType<typeof vi.fn>;
  onDirtyChange: ReturnType<typeof vi.fn>;
  handle: React.RefObject<MarkupEditorHandle | null>;
  rerender: (mode: EditorMode) => void;
}

function mount(mode: EditorMode = 'view', opts: { initialDoc?: MarkupDoc; onSave?: (doc: MarkupDoc) => Promise<void> } = {}): Harness {
  const onModeChange = vi.fn();
  const onSave = vi.fn(opts.onSave ?? (async () => {}));
  const onDirtyChange = vi.fn();
  const handle = createRef<MarkupEditorHandle>();
  const ui = (m: EditorMode) => (
    <MarkupEditor
      imageUrl="blob:source"
      initialDoc={opts.initialDoc}
      mode={m}
      onModeChange={onModeChange}
      onSave={onSave}
      onDirtyChange={onDirtyChange}
      editorRef={handle}
    />
  );
  const view = render(ui(mode));
  if (!opts.initialDoc) {
    const img = view.container.querySelector('img') as HTMLImageElement;
    Object.defineProperty(img, 'naturalWidth', { configurable: true, value: 800 });
    Object.defineProperty(img, 'naturalHeight', { configurable: true, value: 600 });
    fireEvent.load(img);
  }
  return { onModeChange, onSave, onDirtyChange, handle, rerender: (m) => view.rerender(ui(m)) };
}

const surface = () => screen.getByTestId('markup-surface');
const drag = (el: Element, from: [number, number], to: [number, number]) => {
  fireEvent.pointerDown(el, { clientX: from[0], clientY: from[1], button: 0 });
  fireEvent.pointerMove(el, { clientX: (from[0] + to[0]) / 2, clientY: (from[1] + to[1]) / 2 });
  fireEvent.pointerMove(el, { clientX: to[0], clientY: to[1] });
  fireEvent.pointerUp(el, { clientX: to[0], clientY: to[1] });
};
const savedDoc = (h: Harness): MarkupDoc => h.onSave.mock.calls[0][0];

describe('entry toolbar', () => {
  it('offers Markup, Comment and Crop and reports the chosen mode', () => {
    const h = mount('view');
    expect(screen.getByRole('toolbar', { name: 'Edit image' })).toBeInTheDocument();
    for (const [label, mode] of [['Markup', 'markup'], ['Comment', 'comment'], ['Crop', 'crop']] as const) {
      fireEvent.click(screen.getByRole('button', { name: label }));
      expect(h.onModeChange).toHaveBeenLastCalledWith(mode);
    }
  });

  it('renders no toolbar until the image has loaded', () => {
    render(<MarkupEditor imageUrl="blob:x" mode="view" onModeChange={() => {}} onSave={async () => {}} />);
    expect(screen.queryByRole('toolbar')).toBeNull();
  });

  it('sizes the overlay to the image: viewBox is the natural size', () => {
    mount('view');
    expect(surface().getAttribute('viewBox')).toBe('0 0 800 600');
  });
});

describe('markup mode', () => {
  it('toggles aria-pressed as tools are picked', () => {
    mount('markup');
    const pressed = (name: string) => screen.getByRole('button', { name }).getAttribute('aria-pressed');
    expect(pressed('Pen')).toBe('true');
    fireEvent.click(screen.getByRole('button', { name: 'Text' }));
    expect(pressed('Text')).toBe('true');
    expect(pressed('Pen')).toBe('false');
    fireEvent.click(screen.getByRole('button', { name: 'Eraser' }));
    expect(pressed('Eraser')).toBe('true');
    expect(pressed('Text')).toBe('false');
    fireEvent.click(screen.getByRole('button', { name: 'Select' }));
    expect(pressed('Select')).toBe('true');
  });

  it('draws a pen stroke on pointer up, drops a tiny one, and enables Save and Undo', () => {
    const h = mount('markup');
    const save = screen.getByRole('button', { name: 'Save' });
    expect(save).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Undo' })).toBeDisabled();

    fireEvent.pointerDown(surface(), { clientX: 10, clientY: 10, button: 0 });
    fireEvent.pointerUp(surface(), { clientX: 10, clientY: 10 });
    expect(save).toBeDisabled();

    drag(surface(), [10, 10], [200, 120]);
    expect(save).toBeEnabled();
    expect(h.onDirtyChange).toHaveBeenLastCalledWith(true);
    expect(surface().querySelectorAll('path').length).toBe(1);
  });

  it('draws the chosen shape and remembers it', () => {
    const h = mount('markup');
    fireEvent.click(screen.getByRole('button', { name: 'Shapes' }));
    fireEvent.click(screen.getByRole('button', { name: 'Ellipse' }));
    expect(screen.getByRole('button', { name: 'Shapes' })).toHaveAttribute('aria-pressed', 'true');
    drag(surface(), [20, 20], [220, 160]);
    expect(surface().querySelectorAll('ellipse').length).toBe(1);
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    return vi.waitFor(() => expect(savedDoc(h).shapes[0]).toMatchObject({ kind: 'ellipse', x: 20, y: 20, w: 200, h: 140 }));
  });

  it('colours new strokes with the picked swatch', async () => {
    const h = mount('markup');
    fireEvent.click(screen.getByRole('button', { name: 'Colour' }));
    fireEvent.click(screen.getByRole('button', { name: 'Colour #3b82f6' }));
    drag(surface(), [10, 10], [100, 100]);
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await vi.waitFor(() => expect(savedDoc(h).shapes[0]).toMatchObject({ kind: 'stroke', color: '#3b82f6' }));
  });

  it('undoes and redoes through the toolbar and the editor handle', () => {
    const h = mount('markup');
    drag(surface(), [10, 10], [200, 120]);
    expect(surface().querySelectorAll('path').length).toBe(1);
    fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
    expect(surface().querySelectorAll('path').length).toBe(0);
    expect(screen.getByRole('button', { name: 'Redo' })).toBeEnabled();
    fireEvent.click(screen.getByRole('button', { name: 'Redo' }));
    expect(surface().querySelectorAll('path').length).toBe(1);
    act(() => h.handle.current!.undo());
    expect(surface().querySelectorAll('path').length).toBe(0);
    act(() => h.handle.current!.redo());
    expect(surface().querySelectorAll('path').length).toBe(1);
  });

  it('erases every shape the drag touches', () => {
    const h = mount('markup');
    drag(surface(), [10, 10], [300, 10]);
    drag(surface(), [10, 100], [300, 100]);
    expect(surface().querySelectorAll('path').length).toBe(2);
    fireEvent.click(screen.getByRole('button', { name: 'Eraser' }));
    fireEvent.pointerDown(surface(), { clientX: 100, clientY: 10, button: 0 });
    fireEvent.pointerMove(surface(), { clientX: 100, clientY: 100 });
    fireEvent.pointerUp(surface(), { clientX: 100, clientY: 100 });
    expect(surface().querySelectorAll('path').length).toBe(0);
    expect(h.onDirtyChange).toHaveBeenLastCalledWith(true);
  });

  it('selects a shape, moves it as one undo step, and deletes it through the handle', () => {
    const h = mount('markup');
    drag(surface(), [10, 10], [300, 10]);
    fireEvent.click(screen.getByRole('button', { name: 'Select' }));
    drag(surface(), [100, 10], [130, 50]);
    expect(screen.getByTestId('markup-selection')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Undo' }));
    expect(surface().querySelectorAll('path').length).toBe(1);
    // The move was one step: undoing it leaves the stroke, not an empty doc.
    expect(screen.getByRole('button', { name: 'Undo' })).toBeEnabled();
    fireEvent.pointerDown(surface(), { clientX: 100, clientY: 10, button: 0 });
    fireEvent.pointerUp(surface(), { clientX: 100, clientY: 10 });
    let deleted = false;
    act(() => {
      deleted = h.handle.current!.deleteSelection();
    });
    expect(deleted).toBe(true);
    expect(surface().querySelectorAll('path').length).toBe(0);
    act(() => {
      deleted = h.handle.current!.deleteSelection();
    });
    expect(deleted).toBe(false);
  });

  it('cancel() peels the selection and returns false when nothing is left', () => {
    const h = mount('markup');
    drag(surface(), [10, 10], [300, 10]);
    fireEvent.click(screen.getByRole('button', { name: 'Select' }));
    fireEvent.pointerDown(surface(), { clientX: 100, clientY: 10, button: 0 });
    fireEvent.pointerUp(surface(), { clientX: 100, clientY: 10 });
    expect(screen.queryByTestId('markup-selection')).not.toBeNull();
    let cancelled = false;
    act(() => {
      cancelled = h.handle.current!.cancel();
    });
    expect(cancelled).toBe(true);
    expect(screen.queryByTestId('markup-selection')).toBeNull();
    act(() => {
      cancelled = h.handle.current!.cancel();
    });
    expect(cancelled).toBe(false);
  });

  it('places text on click, commits on Cmd+Enter, and keeps newlines for plain Enter', async () => {
    const h = mount('markup');
    fireEvent.click(screen.getByRole('button', { name: 'Text' }));
    fireEvent.pointerDown(surface(), { clientX: 40, clientY: 50, button: 0 });
    fireEvent.pointerUp(surface(), { clientX: 40, clientY: 50 });
    const box = screen.getByRole('textbox', { name: 'Text' });
    fireEvent.change(box, { target: { value: 'hello\nworld' } });
    fireEvent.keyDown(box, { key: 'Enter', metaKey: true });
    expect(screen.queryByRole('textbox', { name: 'Text' })).toBeNull();
    expect(surface().querySelectorAll('text').length).toBe(2);
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await vi.waitFor(() => expect(savedDoc(h).shapes[0]).toMatchObject({ kind: 'text', text: 'hello\nworld', x: 40, y: 50 }));
  });

  it('commits text on blur, and Escape through the handle drops the open text box', () => {
    const h = mount('markup');
    fireEvent.click(screen.getByRole('button', { name: 'Text' }));
    fireEvent.pointerDown(surface(), { clientX: 40, clientY: 50, button: 0 });
    fireEvent.pointerUp(surface(), { clientX: 40, clientY: 50 });
    fireEvent.change(screen.getByRole('textbox', { name: 'Text' }), { target: { value: 'kept' } });
    fireEvent.blur(screen.getByRole('textbox', { name: 'Text' }));
    expect(surface().querySelectorAll('text').length).toBe(1);

    fireEvent.pointerDown(surface(), { clientX: 300, clientY: 300, button: 0 });
    fireEvent.pointerUp(surface(), { clientX: 300, clientY: 300 });
    fireEvent.change(screen.getByRole('textbox', { name: 'Text' }), { target: { value: 'dropped' } });
    let cancelled = false;
    act(() => {
      cancelled = h.handle.current!.cancel();
    });
    expect(cancelled).toBe(true);
    expect(screen.queryByRole('textbox', { name: 'Text' })).toBeNull();
    expect(surface().querySelectorAll('text').length).toBe(1);
  });

  it('the close button goes back to view mode', () => {
    const h = mount('markup');
    fireEvent.click(screen.getByRole('button', { name: 'Close editor' }));
    expect(h.onModeChange).toHaveBeenCalledWith('view');
  });

  it('shows a spinner while saving and stays usable when the save rejects', async () => {
    let reject: (e: Error) => void = () => {};
    const h = mount('markup', { onSave: () => new Promise<void>((_, rej) => { reject = rej; }) });
    drag(surface(), [10, 10], [200, 120]);
    const save = screen.getByRole('button', { name: 'Save' });
    fireEvent.click(save);
    expect(save).toBeDisabled();
    expect(save).toHaveAttribute('aria-busy', 'true');
    expect(h.onSave).toHaveBeenCalledTimes(1);
    await act(async () => reject(new Error('upload failed')));
    expect(save).toBeEnabled();
    expect(surface().querySelectorAll('path').length).toBe(1);
  });
});

describe('comment mode', () => {
  const click = (x: number, y: number) => fireEvent.click(screen.getByTestId('comment-layer'), { clientX: x, clientY: y });

  it('shows the hint, drops numbered pins with an input, and commits on Enter', () => {
    mount('comment');
    expect(screen.getByText('Click on the image to add comments')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();

    click(100, 80);
    const input = screen.getByPlaceholderText('Add a comment…');
    expect(input).toHaveFocus();
    expect(screen.getByRole('button', { name: 'Comment 1' })).toBeInTheDocument();
    fireEvent.change(input, { target: { value: 'fix this' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(screen.queryByPlaceholderText('Add a comment…')).toBeNull();
    expect(screen.getByRole('button', { name: 'Comment 1: fix this' })).toBeInTheDocument();

    click(300, 200);
    fireEvent.change(screen.getByPlaceholderText('Add a comment…'), { target: { value: 'and this' } });
    fireEvent.blur(screen.getByPlaceholderText('Add a comment…'));
    expect(screen.getByRole('button', { name: 'Comment 2: and this' })).toBeInTheDocument();
    expect(surface().querySelectorAll('circle').length).toBe(2);
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled();
  });

  it('reopens a pin for editing, and deleting pin 1 renumbers the rest', () => {
    mount('comment');
    click(100, 80);
    fireEvent.change(screen.getByPlaceholderText('Add a comment…'), { target: { value: 'one' } });
    fireEvent.keyDown(screen.getByPlaceholderText('Add a comment…'), { key: 'Enter' });
    click(300, 200);
    fireEvent.change(screen.getByPlaceholderText('Add a comment…'), { target: { value: 'two' } });
    fireEvent.keyDown(screen.getByPlaceholderText('Add a comment…'), { key: 'Enter' });

    fireEvent.click(screen.getByRole('button', { name: 'Comment 1: one' }));
    const input = screen.getByPlaceholderText('Add a comment…') as HTMLInputElement;
    expect(input.value).toBe('one');
    fireEvent.click(screen.getByRole('button', { name: 'Delete comment 1' }));

    expect(screen.queryByRole('button', { name: /Comment 2/ })).toBeNull();
    expect(screen.getByRole('button', { name: 'Comment 1: two' })).toBeInTheDocument();
    expect(surface().querySelectorAll('circle').length).toBe(1);
  });

  it('Escape on an empty new pin removes it and leaves nothing to save', () => {
    const h = mount('comment');
    click(100, 80);
    let cancelled = false;
    act(() => {
      cancelled = h.handle.current!.cancel();
    });
    expect(cancelled).toBe(true);
    expect(screen.queryByRole('button', { name: /^Comment 1/ })).toBeNull();
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
    act(() => {
      cancelled = h.handle.current!.cancel();
    });
    expect(cancelled).toBe(false);
  });

  it('Escape inside the input does not reach the dialog', () => {
    mount('comment');
    click(100, 80);
    const outer = vi.fn();
    document.body.addEventListener('keydown', outer);
    fireEvent.keyDown(screen.getByPlaceholderText('Add a comment…'), { key: 'Escape' });
    document.body.removeEventListener('keydown', outer);
    expect(outer).not.toHaveBeenCalled();
  });

  it('typing in a pin is one undo step', () => {
    const h = mount('comment');
    click(100, 80);
    const input = screen.getByPlaceholderText('Add a comment…');
    fireEvent.change(input, { target: { value: 'a' } });
    fireEvent.change(input, { target: { value: 'ab' } });
    fireEvent.change(input, { target: { value: 'abc' } });
    fireEvent.keyDown(input, { key: 'Enter' });
    act(() => h.handle.current!.undo());
    expect(screen.getByRole('button', { name: 'Comment 1' })).toBeInTheDocument();
    act(() => h.handle.current!.undo());
    expect(screen.queryByRole('button', { name: /^Comment 1/ })).toBeNull();
  });

  it('saves the doc with its pins', async () => {
    const h = mount('comment');
    click(100, 80);
    fireEvent.change(screen.getByPlaceholderText('Add a comment…'), { target: { value: 'look' } });
    fireEvent.keyDown(screen.getByPlaceholderText('Add a comment…'), { key: 'Enter' });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await vi.waitFor(() => expect(h.onSave).toHaveBeenCalledTimes(1));
    expect(savedDoc(h)).toMatchObject({ width: 800, height: 600, shapes: [{ kind: 'pin', n: 1, x: 100, y: 80, text: 'look' }] });
  });
});

describe('crop mode', () => {
  it('Done writes doc.crop and returns to view; a pill Save then offers the result', async () => {
    const h = mount('crop');
    expect(screen.getByRole('button', { name: 'Aspect ratio Free' })).toHaveAttribute('aria-pressed', 'true');
    fireEvent.click(screen.getByRole('button', { name: 'Aspect ratio 1:1' }));
    expect(screen.getByRole('button', { name: 'Aspect ratio 1:1' })).toHaveAttribute('aria-pressed', 'true');
    // A 1:1 frame fits the 800x600 image: 600x600, centred.
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(h.onModeChange).toHaveBeenCalledWith('view');

    h.rerender('view');
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await vi.waitFor(() => expect(h.onSave).toHaveBeenCalledTimes(1));
    expect(savedDoc(h).crop).toEqual({ x: 100, y: 0, w: 600, h: 600 });
  });

  it('Done without a change leaves the doc untouched', () => {
    const h = mount('crop');
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    h.rerender('view');
    expect(screen.queryByRole('button', { name: 'Save' })).toBeNull();
  });

  it('dragging a handle resizes the frame', async () => {
    const h = mount('crop');
    const handle = screen.getByRole('button', { name: 'Resize crop bottom right' });
    fireEvent.pointerDown(handle, { clientX: 800, clientY: 600, button: 0 });
    fireEvent.pointerMove(handle, { clientX: 500, clientY: 300 });
    fireEvent.pointerUp(handle, { clientX: 500, clientY: 300 });
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    h.rerender('view');
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await vi.waitFor(() => expect(h.onSave).toHaveBeenCalledTimes(1));
    expect(savedDoc(h).crop).toEqual({ x: 0, y: 0, w: 500, h: 300 });
  });

  it('Reset restores the full frame and the close button cancels', () => {
    const h = mount('crop');
    fireEvent.click(screen.getByRole('button', { name: 'Aspect ratio 16:9' }));
    fireEvent.click(screen.getByRole('button', { name: 'Reset crop' }));
    expect(screen.getByRole('button', { name: 'Aspect ratio Free' })).toHaveAttribute('aria-pressed', 'true');
    fireEvent.click(screen.getByRole('button', { name: 'Cancel crop' }));
    expect(h.onModeChange).toHaveBeenCalledWith('view');
  });

  it('dims the outside of an existing crop in view mode', () => {
    const doc: MarkupDoc = { width: 800, height: 600, crop: { x: 100, y: 100, w: 400, h: 300 }, shapes: [] };
    mount('view', { initialDoc: doc });
    expect(surface().querySelector('path[fill-rule="evenodd"]')).not.toBeNull();
  });
});

describe('crop geometry', () => {
  const bounds = { x: 0, y: 0, w: 800, h: 600 };
  it('keeps a locked ratio while dragging a corner and stays inside the image', () => {
    const r = resizeCrop({ x: 100, y: 100, w: 200, h: 200 }, 'se', 5000, 5000, bounds, aspectRatio('16:9'));
    expect(r.w / r.h).toBeCloseTo(16 / 9, 3);
    expect(r.x).toBe(100);
    expect(r.y).toBe(100);
    expect(r.x + r.w).toBeLessThanOrEqual(800 + 1e-6);
    expect(r.y + r.h).toBeLessThanOrEqual(600 + 1e-6);
  });
  it('never shrinks below the minimum or crosses the opposite edge', () => {
    const r = resizeCrop({ x: 100, y: 100, w: 200, h: 200 }, 'nw', 1000, 1000, bounds, null);
    expect(r.w).toBe(16);
    expect(r.h).toBe(16);
    expect(r.x + r.w).toBe(300);
  });
  it('moves inside the bounds', () => {
    expect(resizeCrop({ x: 100, y: 100, w: 200, h: 200 }, 'move', -500, 900, bounds, null)).toEqual({ x: 0, y: 400, w: 200, h: 200 });
  });
});

describe('model sanity used by the editor', () => {
  it('history is dirty only after an edit', () => {
    const h = createHistory({ width: 1, height: 1, shapes: [] });
    expect(markupReducer(h, { type: 'commit' }).past).toHaveLength(0);
  });
});

describe('toolbars', () => {
  it('are labelled toolbars with a drag handle', () => {
    mount('markup');
    const bar = screen.getByRole('toolbar', { name: 'Markup tools' });
    expect(within(bar).getByRole('button', { name: 'Move toolbar' })).toBeInTheDocument();
  });

  it('drag the pill by its handle and keep it inside the viewport', () => {
    mount('view');
    const bar = screen.getByRole('toolbar', { name: 'Edit image' });
    const handle = within(bar).getByRole('button', { name: 'Move toolbar' });
    bar.getBoundingClientRect = () => ({ left: 400, right: 600, top: 700, bottom: 740, width: 200, height: 40, x: 400, y: 700, toJSON: () => ({}) }) as DOMRect;
    fireEvent.pointerDown(handle, { clientX: 410, clientY: 710, button: 0 });
    fireEvent.pointerMove(handle, { clientX: 310, clientY: 690 });
    expect(bar.style.transform).toContain('-100px');
    expect(bar.style.transform).toContain('-20px');
    fireEvent.pointerMove(handle, { clientX: -9999, clientY: 710 });
    expect(bar.style.transform).toContain('-400px');
    fireEvent.pointerUp(handle);
  });
});
