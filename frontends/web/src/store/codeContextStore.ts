/**
 * Code Context Store
 * 
 * Manages code contexts (file references with line ranges) that can be added to chat.
 * These are displayed as chips in the chat input, separate from file attachments.
 */

import { create } from 'zustand';
import { subscribeWithSelector } from 'zustand/middleware';

export interface CodeContext {
  id: string;
  filePath: string;
  fileName: string;
  startLine: number;
  endLine: number;
  language?: string; // File extension for language indicator
}

interface CodeContextStore {
  contexts: Map<string, CodeContext[]>; // sessionId -> contexts
  addContext: (sessionId: string, context: CodeContext) => boolean; // Returns true if added, false if skipped
  removeContext: (sessionId: string, contextId: string) => void;
  clearContexts: (sessionId: string) => void;
  getContexts: (sessionId: string) => CodeContext[];
}

export const useCodeContextStore = create<CodeContextStore>()(
  subscribeWithSelector((set, get) => ({
    contexts: new Map(),

    addContext: (sessionId: string, context: CodeContext) => {
      const currentContexts = get().contexts.get(sessionId) || [];
      
      // Allow duplicates - user can add the same line/range multiple times
      // Remove any existing contexts that are completely contained within this new range
      // (e.g., if 5-10 exists and user adds 1-20, remove 5-10)
      const filteredContexts = currentContexts.filter(
        ctx =>
          !(ctx.filePath === context.filePath &&
            context.startLine <= ctx.startLine &&
            context.endLine >= ctx.endLine)
      );

      const newContexts = [...filteredContexts, context];
      const newMap = new Map(get().contexts);
      newMap.set(sessionId, newContexts);
      
      set({ contexts: newMap });
      return true; // Successfully added
    },

    removeContext: (sessionId: string, contextId: string) => {
      const currentContexts = get().contexts.get(sessionId) || [];
      const filtered = currentContexts.filter(ctx => ctx.id !== contextId);
      const newMap = new Map(get().contexts);
      newMap.set(sessionId, filtered);
      set({ contexts: newMap });
    },

    clearContexts: (sessionId: string) => {
      const newMap = new Map(get().contexts);
      newMap.delete(sessionId);
      set({ contexts: newMap });
    },

    getContexts: (sessionId: string) => {
      return get().contexts.get(sessionId) || [];
    },
  }))
);
