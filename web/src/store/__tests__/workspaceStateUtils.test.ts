/**
 * Tests for Workspace State Utilities
 * 
 * Tests validation, sanitization, and restoration planning functions.
 */

import { describe, it, expect } from 'vitest';
import {
  validateProjectId,
  validateWorktreeId,
  validateChatId,
  validateWorktreeState,
  validateProjectState,
  sanitizeViewers,
  isAlwaysSafeViewer,
  buildRestorationPlan,
  createDefaultRestorationPlan,
  type ValidationContext,
} from '../workspaceStateUtils';
import {
  createDefaultWorktreeState,
  createDefaultProjectState,
  MAIN_WORKTREE_KEY,
  type SerializedViewer,
  type WorktreeState,
  type ProjectWorkspaceState,
} from '../workspaceStateStore';

describe('Validation Functions', () => {
  const ctx: ValidationContext = {
    validProjectIds: ['project-1', 'project-2'],
    validWorktreeIds: {
      'project-1': ['wt-1', 'wt-2'],
      'project-2': ['wt-3'],
    },
    validChatIds: {
      'project-1': ['chat-1', 'chat-2'],
      'project-2': ['chat-3'],
    },
  };

  describe('validateProjectId', () => {
    it('should return true for null projectId', () => {
      expect(validateProjectId(null, ctx)).toBe(true);
    });

    it('should return true for valid projectId', () => {
      expect(validateProjectId('project-1', ctx)).toBe(true);
      expect(validateProjectId('project-2', ctx)).toBe(true);
    });

    it('should return false for invalid projectId', () => {
      expect(validateProjectId('invalid-project', ctx)).toBe(false);
    });
  });

  describe('validateWorktreeId', () => {
    it('should return true for null worktreeId (main branch)', () => {
      expect(validateWorktreeId('project-1', null, ctx)).toBe(true);
    });

    it('should return true for valid worktreeId', () => {
      expect(validateWorktreeId('project-1', 'wt-1', ctx)).toBe(true);
      expect(validateWorktreeId('project-1', 'wt-2', ctx)).toBe(true);
    });

    it('should return false for invalid worktreeId', () => {
      expect(validateWorktreeId('project-1', 'invalid-wt', ctx)).toBe(false);
    });

    it('should return false for worktree from different project', () => {
      expect(validateWorktreeId('project-1', 'wt-3', ctx)).toBe(false);
    });
  });

  describe('validateChatId', () => {
    it('should return true for null chatId', () => {
      expect(validateChatId('project-1', null, ctx)).toBe(true);
    });

    it('should return true for valid chatId', () => {
      expect(validateChatId('project-1', 'chat-1', ctx)).toBe(true);
    });

    it('should return false for invalid chatId', () => {
      expect(validateChatId('project-1', 'invalid-chat', ctx)).toBe(false);
    });
  });

  describe('validateWorktreeState', () => {
    it('should sanitize invalid activeChatId', () => {
      const state: WorktreeState = {
        ...createDefaultWorktreeState(),
        activeChatId: 'invalid-chat',
      };

      const { state: validated, result } = validateWorktreeState(state, 'project-1', ctx);

      expect(validated.activeChatId).toBeNull();
      expect(result.warnings).toContain('Invalid activeChatId: invalid-chat');
    });

    it('should filter invalid chats from queue', () => {
      const state: WorktreeState = {
        ...createDefaultWorktreeState(),
        chatQueue: ['chat-1', 'invalid-chat', 'chat-2'],
      };

      const { state: validated, result } = validateWorktreeState(state, 'project-1', ctx);

      expect(validated.chatQueue).toEqual(['chat-1', 'chat-2']);
      expect(result.warnings.some(w => w.includes('invalid-chat'))).toBe(true);
    });

    it('should filter scroll positions for invalid chats', () => {
      const state: WorktreeState = {
        ...createDefaultWorktreeState(),
        scrollPositions: {
          'chat-1': 100,
          'invalid-chat': 200,
        },
      };

      const { state: validated } = validateWorktreeState(state, 'project-1', ctx);

      expect(validated.scrollPositions).toEqual({ 'chat-1': 100 });
    });
  });

  describe('validateProjectState', () => {
    it('should sanitize invalid lastWorktreeId', () => {
      const state: ProjectWorkspaceState = {
        ...createDefaultProjectState(),
        lastWorktreeId: 'invalid-wt',
      };

      const { state: validated, result } = validateProjectState(state, 'project-1', ctx);

      expect(validated.lastWorktreeId).toBeNull();
      expect(result.warnings).toContain('Invalid lastWorktreeId: invalid-wt');
    });

    it('should remove invalid worktree states but keep main', () => {
      const state: ProjectWorkspaceState = {
        ...createDefaultProjectState(),
        worktrees: {
          [MAIN_WORKTREE_KEY]: createDefaultWorktreeState(),
          'invalid-wt': createDefaultWorktreeState(),
          'wt-1': createDefaultWorktreeState(),
        },
      };

      const { state: validated, result } = validateProjectState(state, 'project-1', ctx);

      expect(validated.worktrees[MAIN_WORKTREE_KEY]).toBeDefined();
      expect(validated.worktrees['wt-1']).toBeDefined();
      expect(validated.worktrees['invalid-wt']).toBeUndefined();
      expect(result.warnings.some(w => w.includes('invalid-wt'))).toBe(true);
    });
  });
});

describe('Viewer Sanitization', () => {
  describe('isAlwaysSafeViewer', () => {
    it('should return true for safe viewer types', () => {
      // Note: 'settings' and 'workflows' are now full-screen modes, not tabs
      const safeTypes = ['worktrees', 'projects', 'agents', 'sandbox'];
      
      for (const type of safeTypes) {
        expect(isAlwaysSafeViewer({ type: type as any, title: 'Test' })).toBe(true);
      }
    });

    it('should return false for file/diff viewers', () => {
      expect(isAlwaysSafeViewer({ type: 'file', title: 'Test' })).toBe(false);
      expect(isAlwaysSafeViewer({ type: 'diff', title: 'Test' })).toBe(false);
    });
  });

  describe('sanitizeViewers', () => {
    it('should keep safe viewers', () => {
      const viewers: SerializedViewer[] = [
        { type: 'worktrees', title: 'Worktrees' },
        { type: 'agents', title: 'Agents' },
      ];

      const result = sanitizeViewers(viewers);

      expect(result).toHaveLength(2);
    });

    it('should optionally remove file viewers', () => {
      const viewers: SerializedViewer[] = [
        { type: 'file', title: 'App.tsx', filePath: '/src/App.tsx' },
        { type: 'worktrees', title: 'Worktrees' },
      ];

      const result = sanitizeViewers(viewers, { removeFileViewers: true });

      expect(result).toHaveLength(1);
      expect(result[0].type).toBe('worktrees');
    });

    it('should filter browser viewers with invalid tab IDs', () => {
      const viewers: SerializedViewer[] = [
        { type: 'browser', title: 'Browser', browserTabId: 'valid-tab' },
        { type: 'browser', title: 'Browser 2', browserTabId: 'invalid-tab' },
      ];

      const result = sanitizeViewers(viewers, {
        validBrowserTabIds: ['valid-tab'],
      });

      expect(result).toHaveLength(1);
      expect(result[0].browserTabId).toBe('valid-tab');
    });

    it('should remove browser viewers without tab ID', () => {
      const viewers: SerializedViewer[] = [
        { type: 'browser', title: 'Browser' }, // Missing browserTabId
      ];

      const result = sanitizeViewers(viewers);

      expect(result).toHaveLength(0);
    });

    it('should keep commands viewers', () => {
      const viewers: SerializedViewer[] = [
        { type: 'commands', title: 'Commands' },
      ];

      const result = sanitizeViewers(viewers);

      expect(result).toHaveLength(1);
    });
  });
});

describe('Restoration Planning', () => {
  const ctx: ValidationContext = {
    validProjectIds: ['project-1'],
    validWorktreeIds: { 'project-1': ['wt-1'] },
    validChatIds: { 'project-1': ['chat-1'] },
  };

  describe('createDefaultRestorationPlan', () => {
    it('should create plan with sensible defaults', () => {
      const plan = createDefaultRestorationPlan();

      expect(plan.projectId).toBeNull();
      expect(plan.worktreeId).toBeNull();
      expect(plan.viewers).toEqual([]);
      expect(plan.chatId).toBeNull();
      expect(plan.panels.terminalOpen).toBe(true);
      expect(plan.panels.fileBrowser).toBe(true);
      expect(plan.leftSidebarExpanded).toBe(true); // Global state
      expect(plan.activeView).toBe('chats');
    });
  });

  describe('buildRestorationPlan', () => {
    it('should return default plan for null lastProjectId', () => {
      const plan = buildRestorationPlan(null, {}, ctx);

      expect(plan.projectId).toBeNull();
    });

    it('should return default plan for invalid lastProjectId', () => {
      const plan = buildRestorationPlan('invalid-project', {}, ctx);

      expect(plan.projectId).toBeNull();
      expect(plan.warnings).toContain('Last project no longer exists: invalid-project');
    });

    it('should build valid plan for existing project', () => {
      const projects = {
        'project-1': {
          lastWorktreeId: null,
          activeView: 'files' as const,
          worktrees: {
            [MAIN_WORKTREE_KEY]: {
              ...createDefaultWorktreeState(),
              activeChatId: 'chat-1',
              terminalOpen: true,
            },
          },
        },
      };

      const plan = buildRestorationPlan('project-1', projects, ctx);

      expect(plan.projectId).toBe('project-1');
      expect(plan.worktreeId).toBeNull();
      expect(plan.chatId).toBe('chat-1');
      expect(plan.panels.terminalOpen).toBe(true);
      expect(plan.activeView).toBe('files');
      // leftSidebarExpanded is now global, uses default
      expect(plan.leftSidebarExpanded).toBe(true);
    });

    it('should handle invalid lastWorktreeId gracefully', () => {
      const projects = {
        'project-1': {
          lastWorktreeId: 'invalid-wt',
          activeView: 'chats' as const,
          worktrees: {
            [MAIN_WORKTREE_KEY]: createDefaultWorktreeState(),
          },
        },
      };

      const plan = buildRestorationPlan('project-1', projects, ctx);

      expect(plan.projectId).toBe('project-1');
      expect(plan.worktreeId).toBeNull(); // Falls back to main
      expect(plan.warnings).toContain('Last worktree no longer exists: invalid-wt');
    });

    it('should handle invalid activeChatId', () => {
      const projects = {
        'project-1': {
          lastWorktreeId: null,
          activeView: 'chats' as const,
          worktrees: {
            [MAIN_WORKTREE_KEY]: {
              ...createDefaultWorktreeState(),
              activeChatId: 'invalid-chat',
            },
          },
        },
      };

      const plan = buildRestorationPlan('project-1', projects, ctx);

      expect(plan.chatId).toBeNull();
      expect(plan.warnings).toContain('Last active chat no longer exists: invalid-chat');
    });

    it('should sanitize viewers in plan', () => {
      const projects = {
        'project-1': {
          lastWorktreeId: null,
          activeView: 'chats' as const,
          worktrees: {
            [MAIN_WORKTREE_KEY]: {
              ...createDefaultWorktreeState(),
              openViewers: [
                { type: 'worktrees' as const, title: 'Worktrees' },
                { type: 'browser' as const, title: 'Browser', browserTabId: 'invalid-tab' },
              ],
              activeViewerIndex: 1,
            },
          },
        },
      };

      const plan = buildRestorationPlan('project-1', projects, ctx, {
        validBrowserTabIds: [],
      });

      // Browser viewer should be removed
      expect(plan.viewers).toHaveLength(1);
      expect(plan.viewers[0].type).toBe('worktrees');
      // Active viewer index should be adjusted
      expect(plan.activeViewerIndex).toBe(0);
    });
  });
});
