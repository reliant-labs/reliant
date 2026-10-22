// Copyright (c) 2025 Reliant Labs

import { create } from "zustand";

export interface DeletedFileInfo {
  path: string;
  type: "file" | "directory";
  content?: string; // For text files, store the content before deletion
  canUndo: boolean;
  undoReason?: string;
  worktreeId?: string;
  projectId: string;
  deletedAt: number; // Timestamp
}

export interface FileMoveInfo {
  sourcePath: string;
  destinationPath: string;
  fileName: string;
  worktreeId?: string;
  projectId: string;
  movedAt: number; // Timestamp
}

export interface FileCopyInfo {
  sourcePath: string;
  destinationPath: string;
  fileName: string;
  worktreeId?: string;
  projectId: string;
  copiedAt: number; // Timestamp
}

export type FileOperation = 
  | { type: "delete"; data: DeletedFileInfo }
  | { type: "move"; data: FileMoveInfo }
  | { type: "copy"; data: FileCopyInfo };

interface FileDeletionStore {
  deletedFiles: DeletedFileInfo[];
  movedFiles: FileMoveInfo[];
  copiedFiles: FileCopyInfo[];
  maxHistorySize: number;
  
  // Add a deleted file to history
  addDeletedFile: (file: DeletedFileInfo) => void;
  
  // Add a moved file to history
  addMovedFile: (file: FileMoveInfo) => void;
  
  // Add a copied file to history
  addCopiedFile: (file: FileCopyInfo) => void;
  
  // Get the most recent operation (delete, move, or copy)
  getLastOperation: () => FileOperation | null;
  
  // Get the most recently deleted file
  getLastDeletedFile: () => DeletedFileInfo | null;
  
  // Get the most recently moved file
  getLastMovedFile: () => FileMoveInfo | null;
  
  // Get the most recently copied file
  getLastCopiedFile: () => FileCopyInfo | null;
  
  // Remove the last deleted file from history (after undo)
  removeLastDeletedFile: () => DeletedFileInfo | null;
  
  // Remove the last moved file from history (after undo)
  removeLastMovedFile: () => FileMoveInfo | null;
  
  // Remove the last copied file from history (after undo)
  removeLastCopiedFile: () => FileCopyInfo | null;
  
  // Clear all history
  clearHistory: () => void;
  
  // Check if there's anything to undo
  canUndo: () => boolean;
}

export const useFileDeletionStore = create<FileDeletionStore>((set, get) => ({
  deletedFiles: [],
  movedFiles: [],
  copiedFiles: [],
  maxHistorySize: 50,
  
  addDeletedFile: (file: DeletedFileInfo) => {
    set((state) => ({
      deletedFiles: [...state.deletedFiles, file].slice(-state.maxHistorySize),
    }));
  },
  
  addMovedFile: (file: FileMoveInfo) => {
    set((state) => ({
      movedFiles: [...state.movedFiles, file].slice(-state.maxHistorySize),
    }));
  },
  
  addCopiedFile: (file: FileCopyInfo) => {
    set((state) => ({
      copiedFiles: [...state.copiedFiles, file].slice(-state.maxHistorySize),
    }));
  },
  
  getLastOperation: () => {
    const state = get();
    const allOps: FileOperation[] = [
      ...state.deletedFiles.map(f => ({ type: "delete" as const, data: f })),
      ...state.movedFiles.map(f => ({ type: "move" as const, data: f })),
      ...state.copiedFiles.map(f => ({ type: "copy" as const, data: f })),
    ];
    
    if (allOps.length === 0) return null;
    
    // Sort by timestamp and return most recent
    allOps.sort((a, b) => {
      const aTime = a.type === "delete" ? a.data.deletedAt : 
                   a.type === "move" ? a.data.movedAt : a.data.copiedAt;
      const bTime = b.type === "delete" ? b.data.deletedAt : 
                   b.type === "move" ? b.data.movedAt : b.data.copiedAt;
      return bTime - aTime;
    });
    
    return allOps[0];
  },
  
  getLastDeletedFile: () => {
    const state = get();
    return state.deletedFiles.length > 0
      ? state.deletedFiles[state.deletedFiles.length - 1]
      : null;
  },
  
  getLastMovedFile: () => {
    const state = get();
    return state.movedFiles.length > 0
      ? state.movedFiles[state.movedFiles.length - 1]
      : null;
  },
  
  getLastCopiedFile: () => {
    const state = get();
    return state.copiedFiles.length > 0
      ? state.copiedFiles[state.copiedFiles.length - 1]
      : null;
  },
  
  removeLastDeletedFile: () => {
    const state = get();
    if (state.deletedFiles.length === 0) {
      return null;
    }
    
    const lastFile = state.deletedFiles[state.deletedFiles.length - 1];
    set({
      deletedFiles: state.deletedFiles.slice(0, -1),
    });
    
    return lastFile;
  },
  
  removeLastMovedFile: () => {
    const state = get();
    if (state.movedFiles.length === 0) {
      return null;
    }
    
    const lastFile = state.movedFiles[state.movedFiles.length - 1];
    set({
      movedFiles: state.movedFiles.slice(0, -1),
    });
    
    return lastFile;
  },
  
  removeLastCopiedFile: () => {
    const state = get();
    if (state.copiedFiles.length === 0) {
      return null;
    }
    
    const lastFile = state.copiedFiles[state.copiedFiles.length - 1];
    set({
      copiedFiles: state.copiedFiles.slice(0, -1),
    });
    
    return lastFile;
  },
  
  clearHistory: () => {
    set({ deletedFiles: [], movedFiles: [], copiedFiles: [] });
  },
  
  canUndo: () => {
    const state = get();
    return state.deletedFiles.length > 0 || 
           state.movedFiles.length > 0 || 
           state.copiedFiles.length > 0;
  },
}));
