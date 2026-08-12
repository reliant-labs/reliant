import { useState, useCallback } from 'react';
import { logger } from '../lib/logger';
import type { Project } from '../store/projectStore';

const DISMISSED_STORAGE_KEY = 'reliant.gitInit.dismissedProjectIds';

/**
 * Projects for which the user has declined git initialization.
 *
 * Persisted in localStorage and keyed by project id, so declining for one
 * project doesn't suppress the prompt for others. This is a UI preference,
 * not domain state — losing it on a cache clear just means the user is asked
 * once more, so it deliberately does not round-trip to the backend.
 */
function readDismissed(): Set<string> {
  try {
    const raw = localStorage.getItem(DISMISSED_STORAGE_KEY);
    if (!raw) return new Set();
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? new Set(parsed.filter((id) => typeof id === 'string')) : new Set();
  } catch (err) {
    logger.warn('[useGitInitialization] Failed to read dismissed projects', err);
    return new Set();
  }
}

function persistDismissed(ids: Set<string>): void {
  try {
    localStorage.setItem(DISMISSED_STORAGE_KEY, JSON.stringify([...ids]));
  } catch (err) {
    logger.warn('[useGitInitialization] Failed to persist dismissed projects', err);
  }
}

export interface GitInitializationState {
  showGitInitModal: boolean;
  gitInitProjectInfo: { id: string; name: string } | null;
}

export interface UseGitInitializationReturn extends GitInitializationState {
  checkGitInitialization: (project: Project) => boolean;
  handleOpenGitInitModal: () => void;
  /** Close without recording a decline — use after a successful init. */
  handleCloseGitInitModal: () => void;
  /** Close and remember the decline, so the prompt stops reappearing. */
  handleDismissGitInit: () => void;
}

/**
 * Hook to manage git initialization prompts for non-git projects
 *
 * This hook checks if a project is a git repository and shows
 * the InitializeGitModal if it is not — unless the user has already
 * declined for that project, which is remembered across sessions.
 */
export function useGitInitialization(): UseGitInitializationReturn {
  const [showGitInitModal, setShowGitInitModal] = useState(false);
  const [gitInitProjectInfo, setGitInitProjectInfo] = useState<{ id: string; name: string } | null>(null);
  const [dismissedProjectIds, setDismissedProjectIds] = useState<Set<string>>(readDismissed);

  /**
   * Check if a project needs git initialization
   * Returns true if project is a git repo or user dismissed, false if modal was shown
   */
  const checkGitInitialization = useCallback((project: Project): boolean => {
    // Check if project is already a git repo
    if (project.is_git_repo) {
      logger.info('[useGitInitialization] Project is already a git repo', { 
        projectId: project.id,
        projectName: project.name 
      });
      return true;
    }

    // Respect a previous decline. Without this the prompt reappears every time
    // the triggering effect re-runs, which includes daemon-status polling.
    if (dismissedProjectIds.has(project.id)) {
      logger.info('[useGitInitialization] Git init previously dismissed for project', {
        projectId: project.id,
        projectName: project.name
      });
      return true;
    }

    // Project is not a git repo - show initialization modal
    logger.info('[useGitInitialization] Project is not a git repo, showing modal', {
      projectId: project.id,
      projectName: project.name
    });
    
    setGitInitProjectInfo({ id: project.id, name: project.name });
    setShowGitInitModal(true);
    return false;
  }, [dismissedProjectIds]);

  const handleOpenGitInitModal = useCallback(() => {
    setShowGitInitModal(true);
  }, []);

  // Plain close, used after a successful init. The project is a git repo now,
  // so no decline is recorded and the prompt simply stops applying.
  const handleCloseGitInitModal = useCallback(() => {
    setShowGitInitModal(false);
    setGitInitProjectInfo(null);
  }, []);

  // Dismissing without initializing records the decline. Without this the
  // prompt returns on the next re-run of the triggering effect — which is
  // driven by a 5s daemon poll, so users see it "constantly".
  const handleDismissGitInit = useCallback(() => {
    setShowGitInitModal(false);
    setGitInitProjectInfo((info) => {
      if (info) {
        setDismissedProjectIds((prev) => {
          if (prev.has(info.id)) return prev;
          const next = new Set(prev).add(info.id);
          persistDismissed(next);
          return next;
        });
        logger.info('[useGitInitialization] Recorded git init dismissal', {
          projectId: info.id,
        });
      }
      return null;
    });
  }, []);

  return {
    showGitInitModal,
    gitInitProjectInfo,
    checkGitInitialization,
    handleOpenGitInitModal,
    handleCloseGitInitModal,
    handleDismissGitInit,
  };
}
