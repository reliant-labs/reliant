// Copyright (c) 2025 Reliant Labs

/**
 * Whether the Add project dialog is open, and which method it opens on.
 *
 * Adding a project is a dialog over whatever the user is doing — the sidebar's
 * project menu, Settings → Projects, the new-chat project chip, the tray — not
 * a page that replaces the window. One store so every entry point opens the
 * same dialog, mounted once by the app shell.
 */

import { create } from "zustand";

/** The two ways to add a project. */
export type AddProjectMethod = "clone" | "folder";

interface AddProjectDialogState {
  isOpen: boolean;
  /** The method the dialog opens on; null shows both choices. */
  method: AddProjectMethod | null;
  openAddProject: (method?: AddProjectMethod) => void;
  closeAddProject: () => void;
}

export const useAddProjectDialogStore = create<AddProjectDialogState>((set) => ({
  isOpen: false,
  method: null,
  openAddProject: (method) => set({ isOpen: true, method: method ?? null }),
  closeAddProject: () => set({ isOpen: false, method: null }),
}));

/** Open the Add project dialog from outside React. */
export function openAddProject(method?: AddProjectMethod): void {
  useAddProjectDialogStore.getState().openAddProject(method);
}
