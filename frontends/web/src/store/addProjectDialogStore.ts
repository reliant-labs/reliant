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

export interface AddProjectOptions {
  /**
   * A repository URL to fill the clone form with, when the caller knows which
   * code it wants (a forge app's recorded source). The reader still confirms.
   */
  repoUrl?: string;
}

interface AddProjectDialogState {
  isOpen: boolean;
  /** The method the dialog opens on; null shows both choices. */
  method: AddProjectMethod | null;
  /** The clone form's starting URL; null leaves it empty. */
  repoUrl: string | null;
  openAddProject: (method?: AddProjectMethod, options?: AddProjectOptions) => void;
  closeAddProject: () => void;
}

export const useAddProjectDialogStore = create<AddProjectDialogState>((set) => ({
  isOpen: false,
  method: null,
  repoUrl: null,
  openAddProject: (method, options) => set({ isOpen: true, method: method ?? null, repoUrl: options?.repoUrl?.trim() || null }),
  closeAddProject: () => set({ isOpen: false, method: null, repoUrl: null }),
}));

/** Open the Add project dialog from outside React. */
export function openAddProject(method?: AddProjectMethod, options?: AddProjectOptions): void {
  useAddProjectDialogStore.getState().openAddProject(method, options);
}
