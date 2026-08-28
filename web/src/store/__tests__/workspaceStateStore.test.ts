/**
 * Tests for Workspace State Store
 * 
 * Tests the hierarchical state management (Global → Project → Worktree)
 * including persistence, validation, and state isolation.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

// Mock localStorage before any store imports so zustand's persist middleware
// captures our mock instead of the (possibly incomplete) jsdom localStorage.
// vi.hoisted runs before static imports are resolved.
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

import { act } from '@testing-library/react';
import {
  useWorkspaceStateStore,
  createDefaultWorktreeState,
  createDefaultProjectState,
  MAIN_WORKTREE_KEY,

  type SerializedViewer,
} from '../workspaceStateStore';

// Reset store between tests
beforeEach(() => {
  localStorageMock.clear();
  act(() => {
    useWorkspaceStateStore.getState().reset();
  });
});

afterEach(() => {
  vi.clearAllMocks();
});

describe('workspaceStateStore', () => {
  describe('Global State', () => {
    it('should initialize with null lastProjectId', () => {
      const state = useWorkspaceStateStore.getState();
      expect(state.lastProjectId).toBeNull();
    });

    it('should set and get lastProjectId', () => {
      act(() => {
        useWorkspaceStateStore.getState().setLastProject('project-123');
      });
      
      expect(useWorkspaceStateStore.getState().lastProjectId).toBe('project-123');
    });

    it('should clear lastProjectId when set to null', () => {
      act(() => {
        useWorkspaceStateStore.getState().setLastProject('project-123');
        useWorkspaceStateStore.getState().setLastProject(null);
      });
      
      expect(useWorkspaceStateStore.getState().lastProjectId).toBeNull();
    });
  });

  describe('Project State', () => {
    const projectId = 'test-project';

    it('should return default project state for unknown project', () => {
      const state = useWorkspaceStateStore.getState().getProjectState(projectId);
      
      expect(state.lastWorktreeId).toBeNull();
      expect(state.activeView).toBe('chats');
      expect(state.worktrees[MAIN_WORKTREE_KEY]).toBeDefined();
    });

    it('should set and get lastWorktreeId', () => {
      act(() => {
        useWorkspaceStateStore.getState().setLastWorktree(projectId, 'worktree-456');
      });
      
      const state = useWorkspaceStateStore.getState().getProjectState(projectId);
      expect(state.lastWorktreeId).toBe('worktree-456');
    });

    it('should set and get activeView', () => {
      act(() => {
        useWorkspaceStateStore.getState().setActiveView(projectId, 'settings');
      });
      
      const state = useWorkspaceStateStore.getState().getProjectState(projectId);
      expect(state.activeView).toBe('settings');
    });

    it('should remove project state', () => {
      act(() => {
        useWorkspaceStateStore.getState().setLastProject(projectId);
        useWorkspaceStateStore.getState().setActiveView(projectId, 'files');
        useWorkspaceStateStore.getState().removeProjectState(projectId);
      });
      
      // Should clear lastProjectId since we removed the last project
      expect(useWorkspaceStateStore.getState().lastProjectId).toBeNull();
      
      // Should return default state for removed project
      const state = useWorkspaceStateStore.getState().getProjectState(projectId);
      expect(state.activeView).toBe('chats'); // Default
    });
  });

  describe('Worktree State', () => {
    const projectId = 'test-project';
    const worktreeId = 'test-worktree';

    it('should return default worktree state for unknown worktree', () => {
      const state = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      
      expect(state.activeChatId).toBeNull();
      expect(state.chatQueue).toEqual([]);
      expect(state.openViewers).toEqual([]);
      expect(state.terminalOpen).toBe(true);
    });

    it('should use MAIN_WORKTREE_KEY for null worktreeId', () => {
      act(() => {
        useWorkspaceStateStore.getState().setWorktreeState(projectId, null, {
          activeChatId: 'main-chat',
        });
      });
      
      // Should get same state with null
      const state1 = useWorkspaceStateStore.getState().getWorktreeState(projectId, null);
      expect(state1.activeChatId).toBe('main-chat');
      
      // Verify it's stored under MAIN_WORKTREE_KEY
      const projectState = useWorkspaceStateStore.getState().getProjectState(projectId);
      expect(projectState.worktrees[MAIN_WORKTREE_KEY]?.activeChatId).toBe('main-chat');
    });

    it('should set and get activeChatId', () => {
      act(() => {
        useWorkspaceStateStore.getState().setActiveChatId(projectId, worktreeId, 'chat-789');
      });
      
      const state = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      expect(state.activeChatId).toBe('chat-789');
    });

    it('should manage chat queue with LRU behavior', () => {
      act(() => {
        useWorkspaceStateStore.getState().addToChatQueue(projectId, worktreeId, 'chat-1');
        useWorkspaceStateStore.getState().addToChatQueue(projectId, worktreeId, 'chat-2');
        useWorkspaceStateStore.getState().addToChatQueue(projectId, worktreeId, 'chat-3');
      });
      
      let state = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      expect(state.chatQueue).toEqual(['chat-3', 'chat-2', 'chat-1']);
      
      // Adding existing chat should move it to front
      act(() => {
        useWorkspaceStateStore.getState().addToChatQueue(projectId, worktreeId, 'chat-1');
      });
      
      state = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      expect(state.chatQueue).toEqual(['chat-1', 'chat-3', 'chat-2']);
    });

    it('should remove chat from queue and clean up associated state', () => {
      act(() => {
        useWorkspaceStateStore.getState().setWorktreeState(projectId, worktreeId, {
          activeChatId: 'chat-to-remove',
          chatQueue: ['chat-to-remove', 'chat-2'],
          scrollPositions: { 'chat-to-remove': 100, 'chat-2': 200 },
          showTasksPanel: { 'chat-to-remove': true, 'chat-2': false },
        });
        useWorkspaceStateStore.getState().removeFromChatQueue(projectId, worktreeId, 'chat-to-remove');
      });
      
      const state = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      expect(state.chatQueue).toEqual(['chat-2']);
      expect(state.scrollPositions).toEqual({ 'chat-2': 200 });
      expect(state.showTasksPanel).toEqual({ 'chat-2': false });
      expect(state.activeChatId).toBeNull(); // Was cleared since it was the removed chat
    });

    it('should set open viewers', () => {
      const viewers: SerializedViewer[] = [
        { type: 'file', title: 'App.tsx', filePath: '/src/App.tsx' },
        { type: 'settings', title: 'Settings' },
      ];
      
      act(() => {
        useWorkspaceStateStore.getState().setOpenViewers(projectId, worktreeId, viewers, 0);
      });
      
      const state = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      expect(state.openViewers).toEqual(viewers);
      expect(state.activeViewerIndex).toBe(0);
    });

    it('should set scroll position for chat', () => {
      act(() => {
        useWorkspaceStateStore.getState().setScrollPosition(projectId, worktreeId, 'chat-1', 500);
      });
      
      const state = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      expect(state.scrollPositions['chat-1']).toBe(500);
    });

    it('should set panel states', () => {
      act(() => {
        useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktreeId, true);
        useWorkspaceStateStore.getState().setRightPanelState(projectId, worktreeId, {
          fileBrowser: true,
        });
      });
      
      const state = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      expect(state.terminalOpen).toBe(true);
      expect(state.rightPanelState.fileBrowser).toBe(true);
    });

    it('should set global left sidebar expanded state', () => {
      // Default should be true
      expect(useWorkspaceStateStore.getState().leftSidebarExpanded).toBe(true);
      
      act(() => {
        useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(false);
      });
      
      // Global state should be updated
      expect(useWorkspaceStateStore.getState().leftSidebarExpanded).toBe(false);
      
      // Should be same regardless of which project/worktree we're in
      act(() => {
        useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true);
      });
      expect(useWorkspaceStateStore.getState().leftSidebarExpanded).toBe(true);
    });

    it('should remove worktree state', () => {
      act(() => {
        useWorkspaceStateStore.getState().setLastWorktree(projectId, worktreeId);
        useWorkspaceStateStore.getState().setActiveChatId(projectId, worktreeId, 'chat-1');
        useWorkspaceStateStore.getState().removeWorktreeState(projectId, worktreeId);
      });
      
      // lastWorktreeId should be cleared
      const projectState = useWorkspaceStateStore.getState().getProjectState(projectId);
      expect(projectState.lastWorktreeId).toBeNull();
      
      // Should return default state for removed worktree
      const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId);
      expect(worktreeState.activeChatId).toBeNull();
    });
  });

  describe('Worktree Isolation', () => {
    const projectId = 'test-project';
    const worktree1 = 'worktree-1';
    const worktree2 = 'worktree-2';

    it('should maintain separate state for different worktrees', () => {
      act(() => {
        // Set state for worktree 1
        useWorkspaceStateStore.getState().setActiveChatId(projectId, worktree1, 'chat-wt1');
        useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktree1, true);
        
        // Set state for worktree 2
        useWorkspaceStateStore.getState().setActiveChatId(projectId, worktree2, 'chat-wt2');
        useWorkspaceStateStore.getState().setTerminalOpen(projectId, worktree2, false);
      });
      
      const state1 = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktree1);
      const state2 = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktree2);
      
      expect(state1.activeChatId).toBe('chat-wt1');
      expect(state1.terminalOpen).toBe(true);
      
      expect(state2.activeChatId).toBe('chat-wt2');
      expect(state2.terminalOpen).toBe(false);
    });

    it('should maintain separate state for main branch vs worktrees', () => {
      act(() => {
        // Set state for main branch (null worktreeId)
        useWorkspaceStateStore.getState().setActiveChatId(projectId, null, 'main-chat');
        
        // Set state for a worktree
        useWorkspaceStateStore.getState().setActiveChatId(projectId, worktree1, 'wt-chat');
      });
      
      const mainState = useWorkspaceStateStore.getState().getWorktreeState(projectId, null);
      const wtState = useWorkspaceStateStore.getState().getWorktreeState(projectId, worktree1);
      
      expect(mainState.activeChatId).toBe('main-chat');
      expect(wtState.activeChatId).toBe('wt-chat');
    });
  });

  describe('Project Isolation', () => {
    const project1 = 'project-1';
    const project2 = 'project-2';

    it('should maintain separate state for different projects', () => {
      act(() => {
        useWorkspaceStateStore.getState().setActiveView(project1, 'files');
        useWorkspaceStateStore.getState().setActiveView(project2, 'settings');
        
        useWorkspaceStateStore.getState().setActiveChatId(project1, null, 'p1-chat');
        useWorkspaceStateStore.getState().setActiveChatId(project2, null, 'p2-chat');
      });
      
      const state1 = useWorkspaceStateStore.getState().getProjectState(project1);
      const state2 = useWorkspaceStateStore.getState().getProjectState(project2);
      
      expect(state1.activeView).toBe('files');
      expect(state2.activeView).toBe('settings');
      
      const wt1 = useWorkspaceStateStore.getState().getWorktreeState(project1, null);
      const wt2 = useWorkspaceStateStore.getState().getWorktreeState(project2, null);
      
      expect(wt1.activeChatId).toBe('p1-chat');
      expect(wt2.activeChatId).toBe('p2-chat');
    });
  });

  describe('Cleanup Actions', () => {
    it('should cleanup stale references', () => {
      const projectId = 'valid-project';
      const invalidProjectId = 'deleted-project';
      
      act(() => {
        // Set up state for both projects
        useWorkspaceStateStore.getState().setLastProject(invalidProjectId);
        useWorkspaceStateStore.getState().setActiveChatId(projectId, null, 'valid-chat');
        useWorkspaceStateStore.getState().setActiveChatId(projectId, null, 'invalid-chat');
        useWorkspaceStateStore.getState().addToChatQueue(projectId, null, 'valid-chat');
        useWorkspaceStateStore.getState().addToChatQueue(projectId, null, 'invalid-chat');
        useWorkspaceStateStore.getState().setActiveChatId(invalidProjectId, null, 'chat-1');
        
        // Run cleanup with only valid project and chat
        useWorkspaceStateStore.getState().cleanupStaleReferences(
          [projectId],
          { [projectId]: [] },
          { [projectId]: ['valid-chat'] }
        );
      });
      
      // lastProjectId should be cleared (was invalid)
      expect(useWorkspaceStateStore.getState().lastProjectId).toBeNull();
      
      // Invalid project should be removed, valid project should remain
      const validProjectState = useWorkspaceStateStore.getState().getProjectState(projectId);
      expect(validProjectState).toBeDefined();
      
      // Invalid chat should be removed from queue
      const worktreeState = useWorkspaceStateStore.getState().getWorktreeState(projectId, null);
      expect(worktreeState.chatQueue).toEqual(['valid-chat']);
    });
  });

  describe('Reset', () => {
    it('should reset all state to defaults', () => {
      act(() => {
        useWorkspaceStateStore.getState().setLastProject('project-1');
        useWorkspaceStateStore.getState().setActiveView('project-1', 'settings');
        useWorkspaceStateStore.getState().setActiveChatId('project-1', null, 'chat-1');
        
        useWorkspaceStateStore.getState().reset();
      });
      
      const state = useWorkspaceStateStore.getState();
      expect(state.lastProjectId).toBeNull();
      expect(state.projects).toEqual({});
    });
  });
});

describe('Default State Factories', () => {
  describe('createDefaultWorktreeState', () => {
    it('should create state with all expected fields', () => {
      const state = createDefaultWorktreeState();
      
      expect(state).toEqual({
        activeChatId: null,
        chatQueue: [],
        openViewers: [],
        activeViewerIndex: null,
        rightPanelState: {
          fileBrowser: true,
        },
        rightSidebarTab: "files",
        terminalOpen: true,
        scrollPositions: {},
        showTasksPanel: {},
        showRecentChanges: {},
        chatDrafts: {},
        expandedLoops: {},
        workflowViewerOpen: {},
        workflowViewerMode: {},
        workflowLayoutDirection: {},
      });
    });
  });

  describe('createDefaultProjectState', () => {
    it('should create state with main worktree', () => {
      const state = createDefaultProjectState();
      
      expect(state.lastWorktreeId).toBeNull();
      expect(state.activeView).toBe('chats');
      expect(state.worktrees[MAIN_WORKTREE_KEY]).toBeDefined();
    });
  });
});

describe('Persisted state migration', () => {
  it('should open the terminal for worktrees persisted with the old closed default', async () => {
    // A v6 payload, which is what every existing install has on disk. The
    // explicit `false` came from the old default, not from a user choice, and
    // would otherwise shadow the new default forever.
    localStorageMock.setItem(
      'workspace-state',
      JSON.stringify({
        version: 6,
        state: {
          version: 6,
          lastProjectId: 'project-1',
          leftSidebarExpanded: true,
          projects: {
            'project-1': {
              lastWorktreeId: null,
              activeView: 'chats',
              worktrees: {
                [MAIN_WORKTREE_KEY]: {
                  ...createDefaultWorktreeState(),
                  terminalOpen: false,
                },
                'wt-1': {
                  ...createDefaultWorktreeState(),
                  terminalOpen: false,
                },
              },
            },
          },
        },
      })
    );

    await act(async () => {
      await useWorkspaceStateStore.persist.rehydrate();
    });

    const store = useWorkspaceStateStore.getState();
    expect(store.getWorktreeState('project-1', null).terminalOpen).toBe(true);
    expect(store.getWorktreeState('project-1', 'wt-1').terminalOpen).toBe(true);
  });
});
