/**
 * useProjectRescan Hook
 * 
 * NOTE: Project rescan functionality is currently disabled.
 * This hook returns no-op functions to maintain API compatibility
 * while the feature is disabled.
 */

export interface UseProjectRescanReturn {
  showRescanModal: boolean;
  commitCount: number;
  checkForRescan: (projectId: string, projectPath: string) => Promise<void>;
  handleRescan: () => Promise<void>;
  handleDismissRescan: () => void;
  handleDismissForever: () => void;
}

// No-op async function
const noop = async () => {};
const noopSync = () => {};

export function useProjectRescan(): UseProjectRescanReturn {
  // Feature is disabled - return no-ops
  return {
    showRescanModal: false,
    commitCount: 0,
    checkForRescan: noop,
    handleRescan: noop,
    handleDismissRescan: noopSync,
    handleDismissForever: noopSync,
  };
}
