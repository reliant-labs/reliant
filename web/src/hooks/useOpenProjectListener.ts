import { useEffect, useRef, useCallback } from 'react';
import { useProjectStore } from '../store/projectStore';
import { logger } from '../lib/logger';

/**
 * Hook that listens for the 'open-project' IPC event from Electron.
 * When the CLI invokes `reliant <path>`, the main process sends this event.
 * This hook will:
 * - Find an existing project by path and select it, OR
 * - Create a new project if one doesn't exist for that path
 */
export function useOpenProjectListener() {
  const projects = useProjectStore((state) => state.projects);
  const isLoading = useProjectStore((state) => state.isLoading);
  const selectProject = useProjectStore((state) => state.selectProject);
  const createProject = useProjectStore((state) => state.createProject);

  // Queue for paths received before projects are loaded
  const pendingPathRef = useRef<string | null>(null);

  // Process a project path - find existing or create new
  const handleOpenProject = useCallback(async (projectPath: string) => {
    logger.info('[useOpenProjectListener] Received open-project event', { projectPath });

    // Normalize path (remove trailing slashes for consistent matching)
    const normalizedPath = projectPath.replace(/\/+$/, '');

    // Find existing project by path
    const existingProject = projects.find((p) => {
      const existingNormalized = p.path.replace(/\/+$/, '');
      return existingNormalized === normalizedPath;
    });

    if (existingProject) {
      logger.info('[useOpenProjectListener] Found existing project', {
        projectId: existingProject.id,
        name: existingProject.name
      });
      await selectProject(existingProject);
    } else {
      // Create a new project with name derived from path
      const pathParts = normalizedPath.split('/');
      const projectName = pathParts[pathParts.length - 1] || 'New Project';

      logger.info('[useOpenProjectListener] Creating new project', {
        name: projectName,
        path: normalizedPath
      });

      try {
        await createProject({
          name: projectName,
          path: normalizedPath,
        });
        // createProject automatically sets currentProject
      } catch (error) {
        logger.error('[useOpenProjectListener] Failed to create project', error);
      }
    }
  }, [projects, selectProject, createProject]);

  // Process pending path when projects finish loading
  useEffect(() => {
    if (!isLoading && pendingPathRef.current) {
      const pendingPath = pendingPathRef.current;
      pendingPathRef.current = null;
      handleOpenProject(pendingPath);
    }
  }, [isLoading, handleOpenProject]);

  // Register the IPC listener
  useEffect(() => {
    if (!window.electronAPI?.onOpenProject) {
      return;
    }

    const unsubscribe = window.electronAPI.onOpenProject((projectPath: string) => {
      // If projects are still loading, queue the path
      if (useProjectStore.getState().isLoading) {
        logger.info('[useOpenProjectListener] Projects still loading, queuing path', { projectPath });
        pendingPathRef.current = projectPath;
        return;
      }

      handleOpenProject(projectPath);
    });

    return unsubscribe;
  }, [handleOpenProject]);
}
