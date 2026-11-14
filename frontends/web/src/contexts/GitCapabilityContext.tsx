import { createContext, useContext } from 'react';
import type { ReactNode } from 'react';
import { useProjectStore } from '../store/projectStore';

interface GitCapabilityContextValue {
  isGitAvailable: boolean;
  projectId: string | null;
}

const GitCapabilityContext = createContext<GitCapabilityContextValue>({
  isGitAvailable: false,
  projectId: null,
});

export function GitCapabilityProvider({ children }: { children: ReactNode }) {
  const currentProject = useProjectStore((state) => state.currentProject);
  
  const value: GitCapabilityContextValue = {
    isGitAvailable: currentProject?.is_git_repo ?? false,
    projectId: currentProject?.id ?? null,
  };

  return (
    <GitCapabilityContext.Provider value={value}>
      {children}
    </GitCapabilityContext.Provider>
  );
}

export function useGitCapability() {
  return useContext(GitCapabilityContext);
}

export function useGitAvailable() {
  const { isGitAvailable } = useGitCapability();
  return isGitAvailable;
}

export function useRequiresGit(featureName?: string) {
  const { isGitAvailable, projectId } = useGitCapability();
  
  return {
    isAvailable: isGitAvailable,
    projectId,
    canUseFeature: isGitAvailable,
    reason: isGitAvailable 
      ? null 
      : `${featureName || 'This feature'} requires a git repository. Please initialize git for this project.`,
  };
}
