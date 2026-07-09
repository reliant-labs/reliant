// Custom hooks
export { useFullscreen } from './useFullscreen';
export {
  useTitleBarChrome,
  type UseTitleBarChromeOptions,
  type UseTitleBarChromeResult,
} from './useTitleBarChrome';
export { useWindowContext } from './useWindowContext';
export { useKeyboardShortcuts, useAppKeyboardShortcuts } from './useKeyboardShortcuts';
export { useUnifiedProcessCounts, useCurrentWorkspaceRunningCount } from './useUnifiedProcesses';
export { useElectronIPC, type UseElectronIPCOptions } from './useElectronIPC';
export { useSidebarOverlay, type UseSidebarOverlayOptions, type UseSidebarOverlayReturn } from './useSidebarOverlay';
export { useCodexOAuth, type UseCodexOAuthReturn } from './useCodexOAuth';
export { useClaudeOAuth, type UseClaudeOAuthReturn } from './useClaudeOAuth';
export { useCopilotOAuth, type UseCopilotOAuthReturn, type CopilotOAuthPhase } from './useCopilotOAuth';
export { useOAuthAvailability, type UseOAuthAvailabilityReturn } from './useOAuthAvailability';