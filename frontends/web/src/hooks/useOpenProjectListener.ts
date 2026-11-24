import { useEffect, useRef, useCallback } from 'react';
import { chooseProject, useProjectStore } from '../store/projectStore';
import { useProjects } from "./project-queries";
import { logger } from '../lib/logger';
import { beginWorkspaceOpen } from '../lib/openChatInWorkspace';

/**
 * Hook that listens for the 'open-project' IPC event from Electron.
 * When the CLI invokes `reliant <path>`, the main process sends this event.
 * This hook will:
 * - Find an existing project by path and select it, OR
 * - Create a new project if one doesn't exist for that path
 */
export function useOpenProjectListener() {
  const { projects, isLoading } = useProjects();
  const createProject = useProjectStore((state) => state.createProject);

  // Queue for paths received before projects are loaded
  const pendingPathRef = useRef<string | null>(null);
  const isLoadingRef = useRef(isLoading);
  isLoadingRef.current = isLoading;

  // Process a project path - find existing or create new
  const handleOpenProject = useCallback(async (projectPath: string) => {
    logger.info('[useOpenProjectListener] Received open-project event', { projectPath });

    // One generation covers creation, navigation and workspace restoration.
    const isCurrent = beginWorkspaceOpen();

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
      await chooseProject(existingProject.id, { isCurrent });
    } else {
      // Create a new project with name derived from path
      const pathParts = normalizedPath.split('/');
      const projectName = pathParts[pathParts.length - 1] || 'New Project';

      logger.info('[useOpenProjectListener] Creating new project', {
        name: projectName,
        path: normalizedPath
      });

      try {
        const created = await createProject({
          name: projectName,
          path: normalizedPath,
        }, { isCurrent });
        // A superseded creation still keeps its row, but must not navigate
        // or select over a newer workspace action.
        if (!isCurrent()) return;
        // createProject made it current, except over a chat's page, whose
        // chat the URL keeps: show the new project's chats.
        await chooseProject(created.id, { isCurrent });
      } catch (error) {
        logger.error('[useOpenProjectListener] Failed to create project', error);
      }
    }
  }, [projects, createProject]);

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
      if (isLoadingRef.current) {
        logger.info('[useOpenProjectListener] Projects still loading, queuing path', { projectPath });
        pendingPathRef.current = projectPath;
        return;
      }

      handleOpenProject(projectPath);
    });

    return unsubscribe;
  }, [handleOpenProject]);
}
