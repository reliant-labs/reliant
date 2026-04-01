import { useState, useCallback } from 'react';
import { logger } from '../lib/logger';
import type { Project } from '../store/projectStore';

export interface GitInitializationState {
  showGitInitModal: boolean;
  gitInitProjectInfo: { id: string; name: string } | null;
}

export interface UseGitInitializationReturn extends GitInitializationState {
  checkGitInitialization: (project: Project) => boolean;
  handleOpenGitInitModal: () => void;
  handleCloseGitInitModal: () => void;
}

/**
 * Hook to manage git initialization prompts for non-git projects
 * 
 * This hook checks if a project is a git repository and shows
 * the InitializeGitModal if it is not.
 */
export function useGitInitialization(): UseGitInitializationReturn {
  const [showGitInitModal, setShowGitInitModal] = useState(false);
  const [gitInitProjectInfo, setGitInitProjectInfo] = useState<{ id: string; name: string } | null>(null);

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

    // Project is not a git repo - show initialization modal
    logger.info('[useGitInitialization] Project is not a git repo, showing modal', {
      projectId: project.id,
      projectName: project.name
    });
    
    setGitInitProjectInfo({ id: project.id, name: project.name });
    setShowGitInitModal(true);
    return false;
  }, []);

  const handleOpenGitInitModal = useCallback(() => {
    setShowGitInitModal(true);
  }, []);

  const handleCloseGitInitModal = useCallback(() => {
    setShowGitInitModal(false);
    setGitInitProjectInfo(null);
  }, []);

  return {
    showGitInitModal,
    gitInitProjectInfo,
    checkGitInitialization,
    handleOpenGitInitModal,
    handleCloseGitInitModal,
  };
}
