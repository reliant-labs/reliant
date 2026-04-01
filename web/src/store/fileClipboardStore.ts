// File Clipboard Store - manages cut/copy/paste operations for files
import { create } from "zustand";

export type ClipboardOperation = "cut" | "copy" | null;

interface FileClipboardState {
  operation: ClipboardOperation;
  filePath: string | null;
  fileName: string | null;
  isDirectory: boolean; // Track if the item is a directory
  worktreeId: string | undefined;
  projectId: string | null;
  isPasting: boolean; // Flag to prevent multiple simultaneous pastes
  
  setClipboard: (
    operation: ClipboardOperation,
    filePath: string,
    fileName: string,
    isDirectory: boolean,
    worktreeId?: string,
    projectId?: string
  ) => void;
  clearClipboard: () => void;
  setPasting: (isPasting: boolean) => void;
}

export const useFileClipboardStore = create<FileClipboardState>()((set) => ({
  operation: null,
  filePath: null,
  fileName: null,
  isDirectory: false,
  worktreeId: undefined,
  projectId: null,
  isPasting: false,

  setClipboard: (operation, filePath, fileName, isDirectory, worktreeId, projectId) => {
    set({
      operation,
      filePath,
      fileName,
      isDirectory,
      worktreeId,
      projectId: projectId || null,
      isPasting: false, // Reset paste flag when setting new clipboard
    });
  },

  clearClipboard: () => {
    set({
      operation: null,
      filePath: null,
      fileName: null,
      isDirectory: false,
      worktreeId: undefined,
      projectId: null,
      isPasting: false,
    });
  },

  setPasting: (isPasting) => {
    set({ isPasting });
  },
}));
