export interface BackendStatus {
  isRunning: boolean;
  port: number;
  pid: number | null;
  restartAttempts: number;
}

export interface UpdateStatus {
  type: 'checking' | 'available' | 'not-available' | 'downloaded' | 'download-progress' | 'error';
  version?: string;
  progress?: {
    percent: number;
    transferred: number;
    total: number;
    bytesPerSecond: number;
  };
  error?: string;
  releaseNotes?: string;
}

export interface NotificationData {
  type: 'info' | 'warning' | 'error';
  message: string;
}

export interface ElectronAPI {
  getConfig: () => ReliantConfig | null;
  getVersion: () => Promise<string>;
  getBackendPort: () => Promise<number>;
  getWebPort: () => Promise<number>;
  getBackendStatus: () => Promise<BackendStatus>;
  restartBackend: () => Promise<{ success: boolean; error?: string }>;
  showErrorDialog: (title: string, content: string) => Promise<void>;
  selectDirectory: () => Promise<string | null>;
  openProjectDirectory: (path: string) => Promise<{ success: boolean; error?: string }>;
  openTerminal: (path: string) => Promise<{ success: boolean; error?: string }>;
  openExternal: (url: string) => Promise<void>;
  installCLI: () => Promise<{ success: boolean; error?: string; message?: string }>;
  getCliStatus: () => Promise<{ installed: boolean; path: string | null; embeddedPath: string | null }>;
  showNotification: (options: any) => Promise<{ success: boolean; muted?: boolean; error?: string }>;
  getNotificationMuteStatus: () => Promise<{ muted: boolean; mutedUntil: number | null; preset: 'unmuted' | 'one_hour' | 'until_tomorrow' | 'custom' }>;
  setTrayStatus: (status: { agentStatus: 'idle' | 'running' | 'error'; activeWorkflows: number; hasChats: boolean; canCreateChat: boolean; currentProjectName: string | null; currentWorktreeName: string | null; currentWorktreeId: string; activeChatId: string | null; activeChatTitle: string | null; recentChats: Array<{ id: string; title: string; activity: string; needsRecovery: boolean }>; workspaces: Array<{ id: string; name: string; isMain: boolean }>; workflows: Array<{ name: string; source: 'builtin' | 'user' | 'project' }>; lastActivityAt: string | null }) => Promise<{ success: boolean; error?: string }>;
  onNotification: (callback: (data: NotificationData) => void) => void;

  // Mock driver configuration
  getMockDriverConfig: () => Promise<any>;
  setMockDriverConfig: (config: any) => Promise<void>;
  browseMockFile: () => Promise<string | null>;

  // Window management
  getWindowContext: () => Promise<{ worktreeId?: string; projectId?: string; projectName?: string } | null>;
  openWorktreeWindow: (worktreeData: { id: string; name?: string; path?: string; projectId?: string }) => Promise<{ success: boolean; windowId?: string; error?: string }>;
  switchWorktree: (worktreeData: { id: string; name?: string; path?: string; projectId?: string }) => Promise<{ success: boolean; error?: string }>;
  setWindowProject: (projectData: { projectPath: string; projectName?: string; projectId?: string }) => Promise<{ success: boolean; error?: string }>;
  getAllWindows: () => Promise<{ id: string; worktreeId?: string; projectId?: string }[]>;
  focusWindow: (windowId: string) => Promise<{ success: boolean; error?: string }>;
  createNewWindow: () => Promise<{ success: boolean; windowId?: string; error?: string }>;
  createNewTab: () => Promise<{ success: boolean; error?: string }>;
  closeCurrentTab: () => Promise<{ success: boolean; error?: string }>;
  closeWindowIfNoTabs: (tabCount: number) => Promise<{ success: boolean; error?: string }>;

  // Window controls
  minimizeWindow: () => Promise<{ success: boolean; error?: string }>;
  maximizeWindow: () => Promise<{ success: boolean; error?: string }>;
  closeWindow: () => Promise<{ success: boolean; error?: string }>;
  getFullscreenStatus: () => Promise<boolean>;
  toggleFullscreen: () => Promise<{ success: boolean; isFullScreen?: boolean; error?: string }>;

  // Auto-updater
  checkForUpdates: () => Promise<{ success?: boolean; error?: string; updateInfo?: UpdateStatus }>;
  downloadUpdate: () => Promise<{ success?: boolean; error?: string }>;
  installUpdate: () => Promise<{ success?: boolean; error?: string }>;
  onUpdateStatus: (callback: (status: UpdateStatus) => void) => () => void;

  // Event listeners
  onWorktreeContextChanged: (callback: (data: { worktreeId?: string; projectId?: string }) => void) => () => void;
  onSetWorktreeContext: (callback: (data: { worktreeId?: string; projectId?: string }) => void) => () => void;
  onCreateNewTab: (callback: () => void) => void;
  onResumeLastChat: (callback: () => void) => void;
  onTrayGoToChat: (callback: (payload: { chatId?: string }) => void) => void;
  onTrayGoToWorkflowHub: (callback: () => void) => void;
  onTrayOpenWorkflow: (callback: (payload: { workflowName?: string }) => void) => void;
  onTrayGoToSettings: (callback: () => void) => void;
  onTrayGoToProjectPicker: (callback: () => void) => void;
  onTraySwitchWorkspace: (callback: (payload: { workspaceId?: string }) => void) => void;
  onCloseCurrentTab: (callback: () => void) => void;
  onReopenLastTab: (callback: () => void) => void;
  onCloseAllTabs: (callback: () => void) => void;
  onNavigateTab: (callback: (direction: 'next' | 'previous') => void) => void;
  onFocusChatSearch: (callback: () => void) => void;
  onFocusGlobalSearch: (callback: () => void) => void;
  onProjectInfo: (callback: (data: { id?: string; name?: string; path?: string }) => void) => void;
  onFullscreenChanged: (callback: (isFullscreen: boolean) => void) => () => void;
  onOpenProject: (callback: (projectPath: string) => void) => () => void;

  // OAuth
  //
  // These two were declared here but never implemented in preload.js, so at
  // runtime they are `undefined` in every build that has ever shipped. They are
  // typed as optional so a call site cannot assume the main process provides
  // them — the OAuth redirect target now comes from getAppURL() instead.
  getOAuthRedirectUrl?: () => Promise<{ success: boolean; redirectUrl?: string; error?: string }>;
  onOAuthCallback?: (callback: (callbackUrl: string) => void) => void;

  // Auth storage
  authLoad: () => Promise<{ success: boolean; session?: any; error?: string }>;
  authSave: (session: any) => Promise<{ success: boolean; error?: string }>;
  authClear: () => Promise<{ success: boolean; error?: string }>;

  // Logging
  log: (level: string, ...args: unknown[]) => void;

  // Privacy settings
  updatePrivacySettings: (settings: { crashReportingEnabled: boolean; analyticsEnabled: boolean }) => Promise<{ success: boolean; requiresRestart?: boolean; error?: string }>;
  /**
   * Push effective keyboard bindings (shortcut id -> authored binding string)
   * so the native menu's accelerators match the user's remaps.
   */
  updateShortcutBindings?: (bindings: Record<string, string>) => Promise<{ success: boolean }>;
  getPrivacySettings: () => Promise<{ crashReportingEnabled: boolean; analyticsEnabled: boolean }>;

  // Analytics
  analyticsTrack: (payload: { eventName: string; metadata?: Record<string, unknown>; userID?: string }) => Promise<{ success: boolean; error?: string }>;

  // Browser API
  browser?: {
    createTab: (id: string, url: string) => Promise<{ success: boolean; error?: string }>;
    closeTab: (id: string) => Promise<{ success: boolean; error?: string }>;
    setActiveTab: (id: string) => Promise<{ success: boolean; error?: string }>;
    navigateTab: (id: string, url: string) => Promise<{ success: boolean; error?: string }>;
    goBack: (id: string) => Promise<{ success: boolean; error?: string }>;
    goForward: (id: string) => Promise<{ success: boolean; error?: string }>;
    reload: (id: string) => Promise<{ success: boolean; error?: string }>;
    setBounds: (bounds: { x: number; y: number; width: number; height: number }) => Promise<{ success: boolean; error?: string }>;
    onTabUpdate: (callback: (data: { id: string; title?: string; url?: string; favicon?: string; isLoading?: boolean; canGoBack?: boolean; canGoForward?: boolean }) => void) => () => void;
  };

  platform: string;
  isDevelopment: boolean;
}

/**
 * Canonical shape for the runtime config Electron's preload script injects
 * onto `window.RELIANT_CONFIG`. This is the single source of truth — do NOT
 * re-declare this shape elsewhere. The preload script (electron/src/preload.js)
 * builds this object and assigns it to the window.
 *
 * The Window property is optional because the config is injected asynchronously
 * after page load (and in plain-browser/dev contexts it may never be set).
 */
export interface ReliantConfig {
  grpcPort: number;
  grpcUrl: string;
  // Daemon-gateway URL, injected by the Electron preload's buildConfig() from
  // BackendManager.gatewayUrl. Empty string when the gateway is left to be
  // derived from the API URL. Consumers: cli-commands.ts, to render a daemon
  // start command that dials the gateway rather than the api-server.
  gatewayUrl?: string;
  isElectron: boolean;
  platform: string;
  isDev: boolean;
  useTLS: boolean;
  temporalUIPort?: number;
  controlPlaneURL?: string;
  // Port the local daemon is listening on, injected by the Electron
  // preload's buildConfig() from the get-backend-status IPC response.
  // null until the backend reports it; absent entirely in plain-browser
  // dev. Consumers (AppInitializer, ModernApp) read it only to detect
  // daemon readiness.
  daemonPort?: number | null;
}

declare global {
  interface Window {
    electronAPI: ElectronAPI;
    RELIANT_CONFIG?: ReliantConfig;
  }
}