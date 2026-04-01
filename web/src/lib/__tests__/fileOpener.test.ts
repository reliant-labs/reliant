/**
 * Tests for fileOpener path classification
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

// Create mock store state
let mockWorktreeState = {
  worktrees: [] as unknown[],
  currentWorktree: null as unknown,
};

let mockProjectState = {
  currentProject: null as unknown,
};

// Mock the stores before importing the module
vi.mock('../../store/worktreeStore', () => ({
  useWorktreeStore: Object.assign(
    (selector?: (state: unknown) => unknown) => {
      return selector ? selector(mockWorktreeState) : mockWorktreeState;
    },
    {
      getState: () => mockWorktreeState,
    }
  ),
}));

vi.mock('../../store/projectStore', () => ({
  useProjectStore: Object.assign(
    (selector?: (state: unknown) => unknown) => {
      return selector ? selector(mockProjectState) : mockProjectState;
    },
    {
      getState: () => mockProjectState,
    }
  ),
}));

vi.mock('../../store/viewerStore', () => ({
  useViewerStore: {
    getState: () => ({
      openFileViewer: vi.fn(),
    }),
  },
}));

// Import after mocks are set up
import { classifyPath } from '../fileOpener';
import type { ParsedFilePath } from '../filePath';

describe('classifyPath', () => {
  // Test fixtures
  const projectPath = '/home/user/my-project';
  
  const mainWorktree = {
    id: 'wt-main',
    name: 'main',
    path: '/home/user/my-project',
    branch: 'main',
    base_branch: 'main',
    is_main: true,
    deleted_at: null,
  };
  
  const featureWorktree = {
    id: 'wt-feature',
    name: 'feature-x',
    path: '/home/user/.reliant/worktrees/abc123/feature-x',
    branch: 'feature-x',
    base_branch: 'main',
    is_main: false,
    deleted_at: null,
  };
  
  const archivedWorktree = {
    id: 'wt-archived',
    name: 'old-feature',
    path: '/home/user/.reliant/worktrees/old123/old-feature',
    branch: 'old-feature',
    base_branch: 'main',
    is_main: false,
    deleted_at: '2024-01-01T00:00:00Z', // Archived
  };

  beforeEach(() => {
    vi.clearAllMocks();
    
    // Default mock state
    mockWorktreeState = {
      worktrees: [mainWorktree, featureWorktree, archivedWorktree],
      currentWorktree: mainWorktree,
    };
    
    mockProjectState = {
      currentProject: {
        id: 'proj-1',
        path: projectPath,
      },
    };
  });

  describe('absolute paths', () => {
    it('should classify path in current worktree as "current"', () => {
      const parsed: ParsedFilePath = {
        fullPath: '/home/user/my-project/src/index.ts',
        path: '/home/user/my-project/src/index.ts',
        isAbsolute: true,
      };
      
      const result = classifyPath(parsed, 'wt-main');
      
      expect(result.classification).toBe('current');
      expect(result.targetWorktreeId).toBe('wt-main');
      expect(result.isClickable).toBe(true);
    });

    it('should classify path in different active worktree as "other-worktree"', () => {
      const parsed: ParsedFilePath = {
        fullPath: '/home/user/.reliant/worktrees/abc123/feature-x/src/file.ts',
        path: '/home/user/.reliant/worktrees/abc123/feature-x/src/file.ts',
        isAbsolute: true,
      };
      
      // Context is main worktree, but path is in feature worktree
      const result = classifyPath(parsed, 'wt-main');
      
      expect(result.classification).toBe('other-worktree');
      expect(result.targetWorktreeId).toBe('wt-feature');
      expect(result.matchedWorktree?.name).toBe('feature-x');
      expect(result.isClickable).toBe(true);
      expect(result.tooltipMessage).toContain('feature-x');
    });

    it('should classify path in project but no worktree as "project-only"', () => {
      // Set up a project path that has no worktree
      mockProjectState = {
        currentProject: {
          id: 'proj-1',
          path: '/home/user/other-project',
        },
      };
      
      const parsed: ParsedFilePath = {
        fullPath: '/home/user/other-project/src/file.ts',
        path: '/home/user/other-project/src/file.ts',
        isAbsolute: true,
      };
      
      const result = classifyPath(parsed);
      
      expect(result.classification).toBe('project-only');
      expect(result.targetWorktreeId).toBeNull();
      expect(result.isClickable).toBe(true);
    });

    it('should classify completely external path as "external"', () => {
      const parsed: ParsedFilePath = {
        fullPath: '/etc/passwd',
        path: '/etc/passwd',
        isAbsolute: true,
      };
      
      const result = classifyPath(parsed);
      
      expect(result.classification).toBe('external');
      expect(result.targetWorktreeId).toBeNull();
      expect(result.isClickable).toBe(false);
      expect(result.tooltipMessage).toContain('outside');
    });

    it('should NOT match archived worktree - treat as external', () => {
      const parsed: ParsedFilePath = {
        fullPath: '/home/user/.reliant/worktrees/old123/old-feature/src/file.ts',
        path: '/home/user/.reliant/worktrees/old123/old-feature/src/file.ts',
        isAbsolute: true,
      };
      
      const result = classifyPath(parsed);
      
      // Should NOT be 'other-worktree' to the archived worktree
      expect(result.classification).not.toBe('other-worktree');
      expect(result.targetWorktreeId).not.toBe('wt-archived');
      // Should be external since archived worktrees are filtered out
      expect(result.classification).toBe('external');
    });

    it('should correctly distinguish similar paths (prefix match issue)', () => {
      // Path that starts with worktree path but is actually different
      const parsed: ParsedFilePath = {
        fullPath: '/home/user/my-project2/src/file.ts', // Note: my-project2, not my-project
        path: '/home/user/my-project2/src/file.ts',
        isAbsolute: true,
      };
      
      const result = classifyPath(parsed);
      
      // Should NOT match /home/user/my-project
      expect(result.classification).toBe('external');
      expect(result.targetWorktreeId).toBeNull();
    });
  });

  describe('relative paths', () => {
    it('should resolve relative path with valid worktree context', () => {
      const parsed: ParsedFilePath = {
        fullPath: './src/index.ts',
        path: './src/index.ts',
        isAbsolute: false,
      };
      
      const result = classifyPath(parsed, 'wt-main');
      
      expect(result.classification).toBe('current');
      expect(result.targetWorktreeId).toBe('wt-main');
      expect(result.isClickable).toBe(true);
    });

    it('should classify relative path with NO worktree context as external', () => {
      // No worktrees available
      mockWorktreeState = {
        worktrees: [],
        currentWorktree: null,
      };
      
      const parsed: ParsedFilePath = {
        fullPath: './src/file.ts',
        path: './src/file.ts',
        isAbsolute: false,
      };
      
      const result = classifyPath(parsed);
      
      expect(result.classification).toBe('external');
      expect(result.isClickable).toBe(false);
      expect(result.tooltipMessage).toContain('no workspace context');
    });

    it('should use first active worktree when contextWorktreeId is undefined', () => {
      const parsed: ParsedFilePath = {
        fullPath: './src/file.ts',
        path: './src/file.ts',
        isAbsolute: false,
      };
      
      // No contextWorktreeId passed, should use first active worktree
      const result = classifyPath(parsed);
      
      expect(result.classification).toBe('current');
      expect(result.targetWorktreeId).toBe('wt-main');
    });

    it('should treat archived worktree context as external for relative paths', () => {
      const parsed: ParsedFilePath = {
        fullPath: './src/file.ts',
        path: './src/file.ts',
        isAbsolute: false,
      };
      
      // Context is the archived worktree - per the plan, this should be external
      // because we cannot trust relative paths from archived worktrees
      const result = classifyPath(parsed, 'wt-archived');
      
      // Per plan: "worktreeId refers to archived worktree → 'external' (cannot trust)"
      expect(result.classification).toBe('external');
      expect(result.isClickable).toBe(false);
    });

    it('should classify relative path that resolves to different worktree as "other-worktree"', () => {
      // When a relative path is resolved from one worktree's context
      // but the resolved absolute path falls into a different worktree
      
      // Feature worktree at /home/user/.reliant/worktrees/abc123/feature-x
      // A relative path from main worktree that traverses up and into feature worktree
      // This simulates: ./../../.reliant/worktrees/abc123/feature-x/src/file.ts
      // which resolves to: /home/user/.reliant/worktrees/abc123/feature-x/src/file.ts
      
      const parsed: ParsedFilePath = {
        fullPath: '/home/user/.reliant/worktrees/abc123/feature-x/src/file.ts',
        path: '/home/user/.reliant/worktrees/abc123/feature-x/src/file.ts',
        isAbsolute: true, // After resolution, path becomes absolute
      };
      
      // Context is main worktree, but resolved path is in feature worktree
      const result = classifyPath(parsed, 'wt-main');
      
      // Should classify as other-worktree since the path is in feature worktree
      expect(result.classification).toBe('other-worktree');
      expect(result.targetWorktreeId).toBe('wt-feature');
      expect(result.matchedWorktree?.name).toBe('feature-x');
    });
  });

  describe('edge cases', () => {
    it('should return external for null parsed path', () => {
      const result = classifyPath(null);
      
      expect(result.classification).toBe('external');
      expect(result.isClickable).toBe(false);
      expect(result.tooltipMessage).toBe('Invalid file path');
    });

    it('should handle worktree root path exactly', () => {
      const parsed: ParsedFilePath = {
        fullPath: '/home/user/my-project',
        path: '/home/user/my-project',
        isAbsolute: true,
      };
      
      const result = classifyPath(parsed, 'wt-main');
      
      expect(result.classification).toBe('current');
      expect(result.targetWorktreeId).toBe('wt-main');
    });

    it('should prefer more specific worktree path (nested worktrees)', () => {
      // Add a nested worktree
      const nestedWorktree = {
        id: 'wt-nested',
        name: 'nested',
        path: '/home/user/my-project/packages/sub-package',
        branch: 'nested',
        base_branch: 'main',
        is_main: false,
        deleted_at: null,
      };
      
      mockWorktreeState = {
        worktrees: [mainWorktree, nestedWorktree],
        currentWorktree: mainWorktree,
      };
      
      const parsed: ParsedFilePath = {
        fullPath: '/home/user/my-project/packages/sub-package/src/file.ts',
        path: '/home/user/my-project/packages/sub-package/src/file.ts',
        isAbsolute: true,
      };
      
      const result = classifyPath(parsed, 'wt-main');
      
      // Should match the more specific nested worktree
      expect(result.classification).toBe('other-worktree');
      expect(result.targetWorktreeId).toBe('wt-nested');
    });
  });
});
