import { create } from "zustand";

interface SidebarState {
  width: number;
  diffHeightPercent: number;
  isResizing: boolean; // Global flag to track if any resize is in progress
  
  setWidth: (width: number) => void;
  setDiffHeightPercent: (percent: number) => void;
  setIsResizing: (isResizing: boolean) => void;
}

export const useSidebarStore = create<SidebarState>((set) => ({
  width: (() => {
    const saved = localStorage.getItem('right-sidebar-width');
    return saved ? parseInt(saved, 10) : 300;
  })(),
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
