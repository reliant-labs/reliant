/**
 * Tests for the shared right-column width in sidebarStore.
 *
 * The terminal and the viewer panel are stacked in one flex column and share
 * this single width. Widths persisted before the terminal auto-opened were
 * sized for reading a diff, so they are halved once on first read.
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';

const localStorageMock = vi.hoisted(() => {
  let store: Record<string, string> = {};
  const mock = {
    getItem: (key: string) => store[key] ?? null,
    setItem: (key: string, value: string) => { store[key] = value; },
    removeItem: (key: string) => { delete store[key]; },
    clear: () => { store = {}; },
    get length() { return Object.keys(store).length; },
    key: (i: number) => Object.keys(store)[i] ?? null,
  };
  (globalThis as any).localStorage = mock;
  return mock;
});

const WIDTH_KEY = 'right-sidebar-width';
const MARKER_KEY = 'right-sidebar-width-halved';

// The width is read once, at module evaluation, so each case needs a fresh
// import of the store rather than a re-render.
async function loadStoreWidth(): Promise<number> {
  vi.resetModules();
  const { useSidebarStore } = await import('../sidebarStore');
  return useSidebarStore.getState().width;
}

beforeEach(() => {
  localStorageMock.clear();
});

describe('sidebarStore initial width', () => {
  it('halves an oversized persisted width and writes it back', async () => {
    localStorageMock.setItem(WIDTH_KEY, '900');

    expect(await loadStoreWidth()).toBe(450);
    // Persisted, so the narrower width survives a reload.
    expect(localStorageMock.getItem(WIDTH_KEY)).toBe('450');
  });

  it('clamps to the viewer minimum when half would land below it', async () => {
    // The real-world case: 647 halves to 324, under the 350 floor.
    localStorageMock.setItem(WIDTH_KEY, '647');

    expect(await loadStoreWidth()).toBe(350);
    expect(localStorageMock.getItem(WIDTH_KEY)).toBe('350');
  });

  it('only halves once, so a width the user re-widens is left alone', async () => {
    localStorageMock.setItem(WIDTH_KEY, '800');
    expect(await loadStoreWidth()).toBe(400);

    // User drags it back out; a later launch must not halve it again.
    localStorageMock.setItem(WIDTH_KEY, '900');
    expect(await loadStoreWidth()).toBe(900);
  });

  it('does not shrink below the viewer panel minimum', async () => {
    // 500/2 = 250, which ResizableDiffPanel would clamp straight back to 350.
    localStorageMock.setItem(WIDTH_KEY, '500');
    expect(await loadStoreWidth()).toBe(350);
  });

  it('leaves an already-narrow width untouched', async () => {
    localStorageMock.setItem(WIDTH_KEY, '320');
    expect(await loadStoreWidth()).toBe(320);
  });

  it('uses the default when nothing is persisted', async () => {
    expect(await loadStoreWidth()).toBe(300);
    expect(localStorageMock.getItem(MARKER_KEY)).toBeNull();
  });
});
