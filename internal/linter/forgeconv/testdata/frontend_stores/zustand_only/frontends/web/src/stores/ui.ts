// Fixture: pure client-side Zustand store. No generated Connect import.
// Canonical use case (theme, modal open state). Lint must NOT fire.

import { create } from "zustand";

type UIState = {
  themeDark: boolean;
  setThemeDark: (v: boolean) => void;
};

export const useUIStore = create<UIState>((set) => ({
  themeDark: false,
  setThemeDark: (v) => set({ themeDark: v }),
}));
