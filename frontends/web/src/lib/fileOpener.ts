/**
 * File opener utility - opens files in the viewer panel
 */

import { useMemo } from 'react';
import { useWorktreeStore, type Worktree } from '../store/worktreeStore';
import { useViewerStore } from '../store/viewerStore';
import { useProjectStore } from '../store/projectStore';
import { isElectron } from './constants';
import { logger } from './logger';
import type { ParsedFilePath } from './filePath';
import type { FileNode } from '../components/FileBrowser';

// ============================================================================
// Path Classification
// ============================================================================

export type PathClassification = 'current' | 'other-worktree' | 'project-only' | 'external';

export interface PathClassificationResult {
  classification: PathClassification;
  targetWorktreeId: string | null;
  matchedWorktree: Worktree | null;
  isClickable: boolean;
  tooltipMessage: string;
}

/**
 * Whether the backend will serve a file that lives outside the workspace.
 *
 * Only the desktop app will. There the server runs on the user's own machine
 * and serves that one user, so reading a file they explicitly named grants
 * them nothing they did not already have with a shell or Finder — the
 * read-only viewer RPCs accept an absolute host path
 * (internal/grpc/services/filesystem.go, validateReadPath).
 *
 * A hosted deployment routes filesystem requests through a daemon
 * (FileSystemProxyService), which keeps every path confined to the workspace.
 * Marking such a link clickable there would render a button that looks live
 * and then fails with PermissionDenied, which is worse than saying up front
 * that it cannot be opened.
 */
function canOpenFilesOutsideWorkspace(): boolean {
  return isElectron();
}

/** The store state classification needs, so the core stays pure and testable. */
interface ClassificationContext {
  activeWorktrees: Worktree[];
  currentWorktreeId?: string;
  projectPath?: string;
}

/**
 * Classify a file path to determine how it should be displayed and handled.
 *
 * This is the single implementation. The hook and the non-hook wrapper below
 * both delegate here; they differ only in how they read the stores. They were
 * previously two hand-maintained copies that had already drifted apart (one
 * checked the worktree root with a second `if`, the other with an `||`), so
 * any future fix had to be applied twice to take effect.
 *
 * Classifications:
 * - 'current': Path is in the current/context worktree
 * - 'other-worktree': Path is in a different active worktree
 * - 'project-only': Path is in the project but no worktree match
 * - 'external': Path is outside the project
 */
function classifyPathWithContext(
  parsedPath: ParsedFilePath | null,
  contextWorktreeId: string | undefined,
  { activeWorktrees, currentWorktreeId, projectPath }: ClassificationContext
): PathClassificationResult {
  const unopenable = (tooltipMessage: string): PathClassificationResult => ({
    classification: 'external',
    targetWorktreeId: null,
    matchedWorktree: null,
    isClickable: false,
    tooltipMessage,
  });

  if (!parsedPath) {
    return unopenable('Invalid file path');
  }

  const effectiveWorktreeId = contextWorktreeId || currentWorktreeId;
  const contextWorktree = effectiveWorktreeId
    ? activeWorktrees.find(w => w.id === effectiveWorktreeId)
    : activeWorktrees[0]; // Fallback to first active worktree

  // Resolve path to absolute if relative
  let absolutePath = parsedPath.path;
  if (!parsedPath.isAbsolute) {
    if (!contextWorktree?.path) {
      return unopenable(
        'Cannot open this file: the path is relative and there is no workspace open to resolve it against.'
      );
    }
    absolutePath = `${contextWorktree.path}/${absolutePath.replace(/^\.\//, '')}`;
  }

  // Sort by path length descending to match the most specific worktree first
  const sortedWorktrees = [...activeWorktrees].sort(
    (a, b) => (b.path?.length || 0) - (a.path?.length || 0)
  );

  const isWithin = (base: string | undefined): boolean =>
    !!base && (absolutePath === base || absolutePath.startsWith(base + '/'));

  for (const worktree of sortedWorktrees) {
    if (isWithin(worktree.path)) {
      const isCurrent = worktree.id === effectiveWorktreeId;
      return {
        classification: isCurrent ? 'current' : 'other-worktree',
        targetWorktreeId: worktree.id,
        matchedWorktree: worktree,
        isClickable: true,
        tooltipMessage: isCurrent
          ? `Click to open ${parsedPath.path}`
          : `Opens in ${worktree.name} workspace`,
      };
    }
  }

  if (isWithin(projectPath)) {
    return {
      classification: 'project-only',
      targetWorktreeId: null,
      matchedWorktree: null,
      isClickable: true,
      tooltipMessage: `Click to open ${parsedPath.path}`,
    };
  }

  // Outside the project entirely. The desktop app can still read it, so the
  // link stays live there; a hosted deployment cannot, and says why rather
  // than offering a button that would fail on click.
  if (canOpenFilesOutsideWorkspace()) {
    return {
      classification: 'external',
      targetWorktreeId: null,
      matchedWorktree: null,
      isClickable: true,
      tooltipMessage: `Click to open ${parsedPath.path} (outside this workspace, read-only)`,
    };
  }

  return unopenable(
    `Cannot open ${parsedPath.path}: it is outside this workspace, and files on the host are ` +
      `only readable in the desktop app. Open it in a project or workspace that contains it.`
  );
}

/**
 * Hook version of path classification. Runs at render time for visual styling.
 */
export function usePathClassification(
  parsedPath: ParsedFilePath | null,
  contextWorktreeId?: string
): PathClassificationResult {
  const allWorktrees = useWorktreeStore((state) => state.worktrees);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const projectPath = useProjectStore((state) => state.currentProject?.path);

  // Filter out archived worktrees - fixes latent bug where archived worktrees were matched
  const activeWorktrees = useMemo(
    () => allWorktrees.filter(w => !w.deleted_at),
    [allWorktrees]
  );

  return useMemo(
    () =>
      classifyPathWithContext(parsedPath, contextWorktreeId, {
        activeWorktrees,
        currentWorktreeId: currentWorktree?.id,
        projectPath,
      }),
    [parsedPath, contextWorktreeId, currentWorktree?.id, activeWorktrees, projectPath]
  );
}

/**
 * Non-hook version of path classification for use in click handlers and callbacks.
 * Reads state directly from stores.
 */
export function classifyPath(
  parsedPath: ParsedFilePath | null,
  contextWorktreeId?: string
): PathClassificationResult {
  const worktreeState = useWorktreeStore.getState();
  const projectState = useProjectStore.getState();

  return classifyPathWithContext(parsedPath, contextWorktreeId, {
    activeWorktrees: worktreeState.worktrees.filter(w => !w.deleted_at),
    currentWorktreeId: worktreeState.currentWorktree?.id,
    projectPath: projectState.currentProject?.path,
  });
}

// ============================================================================
// File Opening
// ============================================================================

/**
 * Open a file in the viewer panel
 * Uses the viewerStore to open files in the TabbedViewerPanel
 */
export function openFile(
  parsedPath: ParsedFilePath,
  worktreeId?: string
): void {
  try {
    const worktreeState = useWorktreeStore.getState();
    const viewerState = useViewerStore.getState();
    const projectState = useProjectStore.getState();
    
    // Get current project ID
    const projectId = projectState.currentProject?.id;
    if (!projectId) {
      logger.warn('openFile', 'No project available');
      console.error('[fileOpener] No project available');
      return;
    }
    
    // Filter out archived worktrees
    const activeWorktrees = worktreeState.worktrees.filter(w => !w.deleted_at);
    
    // Get current worktree if not provided
    // Try multiple sources for worktree:
    // 1. Explicitly passed worktreeId
    // 2. Current worktree from store  
    // 3. First available active worktree (fallback for when user hasn't selected one)
    let targetWorktreeId = worktreeId || worktreeState.currentWorktree?.id;
    
    // Fallback: if no worktree is selected but active worktrees are available, use the first one
    if (!targetWorktreeId && activeWorktrees.length > 0) {
      targetWorktreeId = activeWorktrees[0].id;
    }
    
    // Resolve path to absolute if needed
    let { path } = parsedPath;
    const { line, isAbsolute } = parsedPath;
    
    if (!isAbsolute) {
      // Try to find worktree by ID, or use current worktree
      const worktree = targetWorktreeId 
        ? activeWorktrees.find(w => w.id === targetWorktreeId)
        : worktreeState.currentWorktree;
      
      const workingDir = worktree?.path;
      
      if (workingDir) {
        const cleanPath = path.replace(/^\.\//, '');
        path = `${workingDir}/${cleanPath}`;
      }
    }
    
    // IMPORTANT: Validate resolved absolute path against active worktrees only
    // This catches cases where a relative path was resolved using the wrong worktree's directory
    if (activeWorktrees.length > 0) {
      // Sort by path length descending to match most specific worktree first
      const sortedWorktrees = [...activeWorktrees].sort(
        (a, b) => (b.path?.length || 0) - (a.path?.length || 0)
      );
      const matchingWorktree = sortedWorktrees.find(wt => {
        // Use trailing slash to prevent /project matching /project2
        return wt.path && (path.startsWith(wt.path + '/') || path === wt.path);
      });
      
      if (matchingWorktree) {
        targetWorktreeId = matchingWorktree.id;
      }
    }
    
    // Create FileNode for viewerStore
    const fileName = path.split('/').pop() || path;
    const fileNode: FileNode = {
      name: fileName,
      path: path,
      type: 'file',
      // Include line navigation info from parsed path
      line: line,
      lineEnd: parsedPath.lineEnd,
      column: parsedPath.column,
    };
    
    // Open file viewer with worktree context and focus if already open
    viewerState.openFileViewer(fileNode, projectId, targetWorktreeId);
  } catch (error) {
    logger.error('openFile', 'Failed to open file', error);
  }
}

/**
 * Hook version for use in components
 * Resolves the correct worktree from context and opens the file
 */
export function useFileOpener() {
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const allWorktrees = useWorktreeStore((state) => state.worktrees);
  
  // Filter out archived worktrees
  const activeWorktrees = useMemo(
    () => allWorktrees.filter(w => !w.deleted_at),
    [allWorktrees]
  );
  
  return (parsedPath: ParsedFilePath, worktreeId?: string) => {
    // Try to get worktree from:
    // 1. Explicitly passed worktreeId
    // 2. Current worktree from store
    // 3. Validate against file path for absolute paths
    let targetWorktreeId = worktreeId || currentWorktree?.id;
    
    // IMPORTANT: For absolute paths, validate/override worktreeId by matching against file path
    // This handles cases where the chat has a stale/wrong worktree_id
    if (parsedPath.isAbsolute && activeWorktrees.length > 0) {
      // Sort by path length descending to match most specific worktree first
      const sortedWorktrees = [...activeWorktrees].sort(
        (a, b) => (b.path?.length || 0) - (a.path?.length || 0)
      );
      const matchingWorktree = sortedWorktrees.find(wt => {
        // Use trailing slash to prevent /project matching /project2
        return wt.path && (parsedPath.path.startsWith(wt.path + '/') || parsedPath.path === wt.path);
      });
      if (matchingWorktree) {
        targetWorktreeId = matchingWorktree.id;
      }
    }
    
    // Open the file
    openFile(parsedPath, targetWorktreeId);
  };
}
