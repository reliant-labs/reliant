import { useEffect, useState, useCallback } from 'react';
import { useWorktreeStore } from '../store/worktreeStore';
import { useProjectStore } from '../store/projectStore';
import { resolveWorktree, type Worktree } from './worktree-queries';
import { fetchProjects, useProjects } from "./project-queries";

interface WindowContext {
  worktreeId: string | null;
  projectId: string | null;
  projectName: string | null;
}

interface ContextData {
  worktreeId?: string;
  projectId?: string;
  projectName?: string;
}

// WindowContext interface for this specific hook
// ElectronAPI interface is already defined in types/electron.d.ts

// Remove duplicate Window interface declaration - use the one from types/electron.d.ts

export function useWindowContext() {
  const [windowContext, setWindowContext] = useState<WindowContext | null>(null);
  const [isElectron, setIsElectron] = useState(false);
  const selectWorktree = useWorktreeStore((state) => state.selectWorktree);
  const selectProject = useProjectStore((state) => state.selectProject);
  const { projects } = useProjects();

  useEffect(() => {
    // Check if running in Electron
    setIsElectron(!!window.electronAPI);

    if (window.electronAPI) {
      let cancelled = false;

      // Get initial window context
      window.electronAPI.getWindowContext().then(async (context) => {
        if (cancelled || !context) return;

        const windowCtx: WindowContext = {
          worktreeId: context.worktreeId || null,
          projectId: context.projectId || null,
          projectName: context.projectName || null
        };
        setWindowContext(windowCtx);

        // Load projects first
        const currentProjects = await fetchProjects();
        if (cancelled) return;

        // Set the project in store based on window context. A window is a
        // view of every project (the sidebar's filter, not the window, picks
        // which); its saved project is only the context it reopens on. A new
        // window has none and opens on the last project used, like a cold
        // start — never on a picker.
        const contextProject = context.projectId
          ? currentProjects.find(p => p.id === context.projectId)
          : undefined;
        if (!contextProject) {
          await useProjectStore.getState().restoreLastProject();
          return;
        }
        if (context.projectId) {
          const project = contextProject;
          if (project) {
            // Use skipClear since electron context provides specific worktree to use
            await selectProject(project, { skipClear: true });
            if (cancelled) return;

            // Select the window's worktree, fetched by id: the project's full
            // worktree list is not needed for that.
            if (context.worktreeId) {
              const worktree = await resolveWorktree(context.worktreeId);
              if (cancelled) return;
              if (worktree) {
                selectWorktree(worktree);
              }
            }
          }
        }
      });

      // Listen for context changes
      const unsubContextChanged = window.electronAPI.onWorktreeContextChanged(async (data: ContextData) => {
        const windowCtx: WindowContext = {
          worktreeId: data.worktreeId || null,
          projectId: data.projectId || null,
          projectName: data.projectName || null
        };
        setWindowContext(windowCtx);

        // Load projects first
        const currentProjects = await fetchProjects();

        // Update stores
        if (data.projectId) {
          const project = currentProjects.find(p => p.id === data.projectId);
          if (project) {
            // Use skipClear since electron context provides specific worktree to use
            await selectProject(project, { skipClear: true });

            // Select the window's worktree, fetched by id: the project's full
            // worktree list is not needed for that.
            if (data.worktreeId) {
              const worktree = await resolveWorktree(data.worktreeId);
              if (worktree) {
                selectWorktree(worktree);
              }
            }
          }
        }
      });

      // Listen for initial context set
      const unsubSetContext = window.electronAPI.onSetWorktreeContext(async (data: ContextData) => {
        const windowCtx: WindowContext = {
          worktreeId: data.worktreeId || null,
          projectId: data.projectId || null,
          projectName: data.projectName || null
        };
        setWindowContext(windowCtx);

        // Load projects first
        const currentProjects = await fetchProjects();

        // Update stores
        if (data.projectId) {
          const project = currentProjects.find(p => p.id === data.projectId);
          if (project) {
            // Use skipClear since electron context provides specific worktree to use
            await selectProject(project, { skipClear: true });

            // Select the window's worktree, fetched by id: the project's full
            // worktree list is not needed for that.
            if (data.worktreeId) {
              const worktree = await resolveWorktree(data.worktreeId);
              if (worktree) {
                selectWorktree(worktree);
              }
            }
          }
        }
      });

      return () => {
        cancelled = true;
        unsubContextChanged();
        unsubSetContext();
      };
    }
  }, [selectWorktree, selectProject]); // Note: intentionally not including worktrees/projects to avoid infinite loops

  const openInNewWindow = useCallback(async (worktreeData: Partial<Worktree>) => {
    if (!window.electronAPI) {
      console.warn('Not running in Electron, cannot open new window');
      return false;
    }

    try {
      const result = await window.electronAPI.openWorktreeWindow(worktreeData as Worktree);
      return result.success;
    } catch (error) {
      console.error('Failed to open worktree in new window:', error);
      return false;
    }
  }, []);

  const switchToWorktree = useCallback(async (worktreeData: Partial<Worktree>) => {
    if (!window.electronAPI) {
      // In non-Electron environment, just update the stores
      selectWorktree(worktreeData as Worktree);
      if (worktreeData.project_id) {
        const project = projects.find(p => p.id === worktreeData.project_id);
        if (project) {
          // Use skipClear since we're setting a specific worktree
          await selectProject(project, { skipClear: true });
        }
      }
      return true;
    }

    try {
      const result = await window.electronAPI.switchWorktree(worktreeData as Worktree);
      return result.success;
    } catch (error) {
      console.error('Failed to switch worktree:', error);
      return false;
    }
  }, [selectWorktree, selectProject, projects]);

  const getAllWindows = useCallback(async () => {
    if (!window.electronAPI) {
      return [];
    }

    try {
      return await window.electronAPI.getAllWindows();
    } catch (error) {
      console.error('Failed to get all windows:', error);
      return [];
    }
  }, []);

  const focusWindow = useCallback(async (windowId: number) => {
    if (!window.electronAPI) {
      return false;
    }

    try {
      const result = await window.electronAPI.focusWindow(windowId.toString());
      return result.success;
    } catch (error) {
      console.error('Failed to focus window:', error);
      return false;
    }
  }, []);

  return {
    windowContext,
    isElectron,
    openInNewWindow,
    switchToWorktree,
    getAllWindows,
    focusWindow
  };
}