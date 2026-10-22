import { create } from "zustand";

interface SidebarState {
  width: number;
  diffHeightPercent: number;
  isResizing: boolean; // Global flag to track if any resize is in progress
  
  setWidth: (width: number) => void;
  setDiffHeightPercent: (percent: number) => void;
  setIsResizing: (isResizing: boolean) => void;
}

/**
 * The right column's width was only ever sized against the viewer panel, which
 * a user opens deliberately. Now that the terminal auto-opens and shares this
 * width, that same value shows up unprompted on launch — and a width chosen for
 * reading a diff is far too wide for a terminal nobody asked for yet.
 *
 * Halve an oversized stored width once. The floor is the viewer's own minWidth
 * (ResizableDiffPanel clamps anything smaller straight back up), so going below
 * it would just be undone the moment a viewer opens.
 */
const VIEWER_MIN_WIDTH = 350;
const DEFAULT_WIDTH = 300;
const WIDTH_HALVED_MARKER = 'right-sidebar-width-halved';

function readInitialWidth(): number {
  let saved: string | null = null;
  try {
    saved = localStorage.getItem('right-sidebar-width');
  } catch {
    return DEFAULT_WIDTH;
  }
  if (!saved) return DEFAULT_WIDTH;

  const stored = parseInt(saved, 10);
  if (!Number.isFinite(stored)) return DEFAULT_WIDTH;

  try {
    if (localStorage.getItem(WIDTH_HALVED_MARKER)) return stored;
    localStorage.setItem(WIDTH_HALVED_MARKER, '1');
    if (stored <= VIEWER_MIN_WIDTH) return stored;

    const halved = Math.max(VIEWER_MIN_WIDTH, Math.round(stored / 2));
    localStorage.setItem('right-sidebar-width', halved.toString());
    return halved;
  } catch {
    // Storage can throw in restricted contexts; the in-memory width is still
    // usable, we just cannot record that the one-time adjustment happened.
    return stored;
  }
}

export const useSidebarStore = create<SidebarState>((set) => ({
  width: readInitialWidth(),
  diffHeightPercent: (() => {
    const saved = localStorage.getItem('right-sidebar-diff-height-percent');
    return saved ? parseInt(saved, 10) : 50;
  })(),
  isResizing: false,
  
  setWidth: (width: number) => {
    localStorage.setItem('right-sidebar-width', width.toString());
    set({ width });
  },
  
  setDiffHeightPercent: (percent: number) => {
    localStorage.setItem('right-sidebar-diff-height-percent', percent.toString());
    set({ diffHeightPercent: percent });
  },
  
  setIsResizing: (isResizing: boolean) => set({ isResizing }),
}));
