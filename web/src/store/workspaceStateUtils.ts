/**
 * Workspace State Utilities
 *
 * Helper functions for validating, sanitizing, and migrating workspace state.
 * Used during state restoration to ensure persisted state is still valid.
 */

import { logger } from "../lib/logger";
import {
  type WorktreeState,
  type ProjectWorkspaceState,
  type SerializedViewer,
  createDefaultWorktreeState,
  MAIN_WORKTREE_KEY,
} from "./workspaceStateStore";

// ============================================================================
// Validation Types
// ============================================================================

export interface ValidationContext {
  /** List of valid project IDs */
  validProjectIds: string[];
  /** Map of projectId -> list of valid worktree IDs */
  validWorktreeIds: Record<string, string[]>;
  /** Map of projectId -> list of valid chat IDs */
  validChatIds: Record<string, string[]>;
}

export interface ValidationResult {
  isValid: boolean;
  errors: string[];
  warnings: string[];
}

// ============================================================================
// Validation Functions
// ============================================================================

/**
 * Validate that a project ID exists
 */
export function validateProjectId(
  projectId: string | null,
  ctx: ValidationContext
): boolean {
  if (!projectId) return true; // null is valid (no project selected)
  return ctx.validProjectIds.includes(projectId);
}

/**
 * Validate that a worktree ID exists for a given project
 */
export function validateWorktreeId(
  projectId: string,
  worktreeId: string | null,
  ctx: ValidationContext
): boolean {
  if (!worktreeId) return true; // null is valid (main branch)
  const validIds = ctx.validWorktreeIds[projectId] ?? [];
  return validIds.includes(worktreeId);
}

/**
 * Validate that a chat ID exists for a given project
 */
export function validateChatId(
  projectId: string,
  chatId: string | null,
  ctx: ValidationContext
): boolean {
  if (!chatId) return true; // null is valid (no chat selected)
  const validIds = ctx.validChatIds[projectId] ?? [];
  return validIds.includes(chatId);
}

/**
 * Validate a worktree state and return sanitized version
 */
export function validateWorktreeState(
  state: WorktreeState,
  projectId: string,
  ctx: ValidationContext
): { state: WorktreeState; result: ValidationResult } {
  const result: ValidationResult = {
    isValid: true,
    errors: [],
    warnings: [],
  };

  const validChats = ctx.validChatIds[projectId] ?? [];

  // Validate activeChatId
  let activeChatId = state.activeChatId;
  if (activeChatId && !validChats.includes(activeChatId)) {
    result.warnings.push(`Invalid activeChatId: ${activeChatId}`);
    activeChatId = null;
  }

  // Filter chat queue to only valid chats
  const chatQueue = state.chatQueue.filter((id) => {
    if (!validChats.includes(id)) {
      result.warnings.push(`Removed invalid chat from queue: ${id}`);
      return false;
    }
    return true;
  });

  // Filter scroll positions to only valid chats
  const scrollPositions: Record<string, number> = {};
  for (const [chatId, pos] of Object.entries(state.scrollPositions)) {
    if (validChats.includes(chatId)) {
      scrollPositions[chatId] = pos;
    }
  }

  // Filter UI states to only valid chats
  const showTasksPanel: Record<string, boolean> = {};
  const showRecentChanges: Record<string, boolean> = {};

  for (const [chatId, value] of Object.entries(state.showTasksPanel)) {
    if (validChats.includes(chatId)) {
      showTasksPanel[chatId] = value;
    }
  }
  for (const [chatId, value] of Object.entries(state.showRecentChanges)) {
    if (validChats.includes(chatId)) {
      showRecentChanges[chatId] = value;
    }
  }

  // Viewers are validated separately (may need async file checks)
  // For now, keep them as-is

  return {
    state: {
      ...state,
      activeChatId,
      chatQueue,
      scrollPositions,
      showTasksPanel,
      showRecentChanges,
    },
    result,
  };
}

/**
 * Validate a project workspace state
 */
export function validateProjectState(
  state: ProjectWorkspaceState,
  projectId: string,
  ctx: ValidationContext
): { state: ProjectWorkspaceState; result: ValidationResult } {
  const result: ValidationResult = {
    isValid: true,
    errors: [],
    warnings: [],
  };

  const validWorktrees = ctx.validWorktreeIds[projectId] ?? [];

  // Validate lastWorktreeId
  let lastWorktreeId = state.lastWorktreeId;
  if (lastWorktreeId && !validWorktrees.includes(lastWorktreeId)) {
    result.warnings.push(`Invalid lastWorktreeId: ${lastWorktreeId}`);
    lastWorktreeId = null;
  }

  // Validate each worktree state
  const worktrees: Record<string, WorktreeState> = {};
  for (const [worktreeKey, worktreeState] of Object.entries(state.worktrees)) {
    // Skip invalid worktrees (except main)
    if (
      worktreeKey !== MAIN_WORKTREE_KEY &&
      !validWorktrees.includes(worktreeKey)
    ) {
      result.warnings.push(`Removed invalid worktree state: ${worktreeKey}`);
      continue;
    }

    const { state: validatedState, result: worktreeResult } =
      validateWorktreeState(worktreeState, projectId, ctx);

    worktrees[worktreeKey] = validatedState;
    result.warnings.push(...worktreeResult.warnings);
    result.errors.push(...worktreeResult.errors);
  }

  // Ensure main worktree always exists
  if (!worktrees[MAIN_WORKTREE_KEY]) {
    worktrees[MAIN_WORKTREE_KEY] = createDefaultWorktreeState();
  }

  return {
    state: {
      ...state,
      lastWorktreeId,
      worktrees,
    },
    result,
  };
}

// ============================================================================
// Viewer Sanitization
// ============================================================================

/**
 * Types of viewers that are always safe to restore (don't reference external resources)
 */
const ALWAYS_SAFE_VIEWER_TYPES = new Set([
  "worktrees",
  "projects",
  "agents",
  "sandbox",
  // Note: "workflows" and "settings" removed - they are now full-screen mode, not tabs
]);

/**
 * Check if a viewer type is always safe to restore
 */
export function isAlwaysSafeViewer(viewer: SerializedViewer): boolean {
  return ALWAYS_SAFE_VIEWER_TYPES.has(viewer.type);
}

/**
 * Sanitize viewers by removing those that reference potentially missing resources.
 * This is a synchronous quick-check. For file viewers, we may need async validation.
 */
export function sanitizeViewers(
  viewers: SerializedViewer[],
  options: {
    /** Remove file/diff viewers (they need async validation) */
    removeFileViewers?: boolean;
    /** Browser tab IDs that are known to be valid */
    validBrowserTabIds?: string[];
  } = {}
): SerializedViewer[] {
  const { removeFileViewers = false, validBrowserTabIds } = options;

  return viewers.filter((viewer) => {
    // Always keep safe viewers
    if (isAlwaysSafeViewer(viewer)) {
      return true;
    }

    // Optionally remove file/diff viewers
    if (removeFileViewers && (viewer.type === "file" || viewer.type === "diff")) {
      logger.debug("[WorkspaceUtils] Removing file viewer for validation", {
        type: viewer.type,
        path: viewer.filePath || viewer.diffPath,
      });
      return false;
    }

    // Validate browser viewers
    if (viewer.type === "browser") {
      if (!viewer.browserTabId) {
        return false;
      }
      if (validBrowserTabIds && !validBrowserTabIds.includes(viewer.browserTabId)) {
        logger.debug("[WorkspaceUtils] Removing invalid browser viewer", {
          browserTabId: viewer.browserTabId,
        });
        return false;
      }
    }

    // Keep commands viewer (they're recreatable)
    if (viewer.type === "commands") {
      return true;
    }

    return true;
  });
}

/**
 * Async validation for file viewers - check if files exist
 * Returns list of viewers that should be kept
 */
export async function validateFileViewers(
  viewers: SerializedViewer[],
  checkFileExists: (path: string) => Promise<boolean>
): Promise<SerializedViewer[]> {
  const results = await Promise.all(
    viewers.map(async (viewer) => {
      if (viewer.type === "file" && viewer.filePath) {
        const exists = await checkFileExists(viewer.filePath);
        if (!exists) {
          logger.debug("[WorkspaceUtils] File no longer exists", {
            path: viewer.filePath,
          });
          return null;
        }
      }
      // Diff viewers are trickier - the diff may be stale
      // For now, we'll skip restoring diff viewers
      if (viewer.type === "diff") {
        logger.debug("[WorkspaceUtils] Skipping diff viewer restoration", {
          path: viewer.diffPath,
        });
        return null;
      }
      return viewer;
    })
  );

  return results.filter((v): v is SerializedViewer => v !== null);
}

// ============================================================================
// Migration Helpers
// ============================================================================

/**
 * Migrate workspace state from one version to another
 * Add migration logic here as the schema evolves
 */
export function migrateWorkspaceState(
  state: unknown,
  fromVersion: number,
  toVersion: number
): unknown {
  let current = state;
  let version = fromVersion;

  while (version < toVersion) {
    logger.info("[WorkspaceUtils] Migrating workspace state", {
      from: version,
      to: version + 1,
    });

    switch (version) {
      case 0:
        // Migration from v0 to v1
        // v1 is the initial version, so this handles pre-existing state
        current = migrateV0ToV1(current);
        break;

      // Add future migrations here:
      // case 1:
      //   current = migrateV1ToV2(current);
      //   break;

      default:
        logger.warn("[WorkspaceUtils] Unknown migration version", { version });
        break;
    }

    version++;
  }

  return current;
}

/**
 * Migration from v0 (no version / legacy) to v1
 */
function migrateV0ToV1(state: unknown): unknown {
  // If state is null/undefined, return fresh state
  if (!state) {
    return {
      version: 1,
      lastProjectId: null,
      projects: {},
    };
  }

  // If state is already v1 format, return as-is
  if (
    typeof state === "object" &&
    state !== null &&
    "version" in state &&
    (state as { version: number }).version === 1
  ) {
    return state;
  }

  // Otherwise, assume it's legacy/corrupted and return fresh state
  logger.warn("[WorkspaceUtils] Resetting corrupted workspace state");
  return {
    version: 1,
    lastProjectId: null,
    projects: {},
  };
}

// ============================================================================
// Restoration Helpers
// ============================================================================

/**
 * Create a restoration plan from persisted state
 * This determines what can be safely restored vs what needs defaults
 */
export interface RestorationPlan {
  /** Project to restore (null if should show picker) */
  projectId: string | null;
  /** Worktree to restore within project (null = main) */
  worktreeId: string | null;
  /** Viewers to restore (already sanitized) */
  viewers: SerializedViewer[];
  /** Active viewer index */
  activeViewerIndex: number | null;
  /** Chat to restore */
  chatId: string | null;
  /** Panel states (per-worktree) */
  panels: {
    fileBrowser: boolean;
    terminalOpen: boolean;
  };
  /** Global left sidebar expanded state */
  leftSidebarExpanded: boolean;
  /** Navigation view for tab state */
  activeView: string;
  /** Warnings encountered during planning */
  warnings: string[];
}

/**
 * Create a restoration plan with defaults
 */
export function createDefaultRestorationPlan(): RestorationPlan {
  return {
    projectId: null,
    worktreeId: null,
    viewers: [],
    activeViewerIndex: null,
    chatId: null,
    panels: {
      fileBrowser: true,
      terminalOpen: false,
    },
    leftSidebarExpanded: true,
    activeView: "chats",
    warnings: [],
  };
}

/**
 * Build a restoration plan from workspace state store
 */
export function buildRestorationPlan(
  lastProjectId: string | null,
  projects: Record<string, ProjectWorkspaceState>,
  ctx: ValidationContext,
  options: {
    validBrowserTabIds?: string[];
  } = {}
): RestorationPlan {
  const plan = createDefaultRestorationPlan();

  // Validate project
  if (!lastProjectId || !validateProjectId(lastProjectId, ctx)) {
    if (lastProjectId) {
      plan.warnings.push(`Last project no longer exists: ${lastProjectId}`);
    }
    return plan;
  }

  plan.projectId = lastProjectId;
  const projectState = projects[lastProjectId];

  if (!projectState) {
    return plan;
  }

  // Validate worktree
  const worktreeId = projectState.lastWorktreeId;
  if (!validateWorktreeId(lastProjectId, worktreeId, ctx)) {
    if (worktreeId) {
      plan.warnings.push(`Last worktree no longer exists: ${worktreeId}`);
    }
    // Continue with main branch
  } else {
    plan.worktreeId = worktreeId;
  }

  // Get worktree state
  const worktreeKey = plan.worktreeId ?? MAIN_WORKTREE_KEY;
  const worktreeState =
    projectState.worktrees[worktreeKey] ?? createDefaultWorktreeState();

  // Set active view for navigation tabs
  plan.activeView = projectState.activeView;

  // Validate and sanitize viewers
  plan.viewers = sanitizeViewers(worktreeState.openViewers, {
    validBrowserTabIds: options.validBrowserTabIds,
  });

  // Adjust active viewer index if viewers were removed
  if (
    worktreeState.activeViewerIndex !== null &&
    worktreeState.activeViewerIndex < plan.viewers.length
  ) {
    plan.activeViewerIndex = worktreeState.activeViewerIndex;
  } else if (plan.viewers.length > 0) {
    plan.activeViewerIndex = 0;
  }

  // Validate chat
  if (validateChatId(lastProjectId, worktreeState.activeChatId, ctx)) {
    plan.chatId = worktreeState.activeChatId;
  } else if (worktreeState.activeChatId) {
    plan.warnings.push(
      `Last active chat no longer exists: ${worktreeState.activeChatId}`
    );
  }

  // Panel states (per-worktree)
  plan.panels = {
    fileBrowser: worktreeState.rightPanelState.fileBrowser,
    terminalOpen: worktreeState.terminalOpen,
  };
  // Note: leftSidebarExpanded is global, not from worktree state
  // It will use the default value from createDefaultRestorationPlan

  return plan;
}
