const { contextBridge, ipcRenderer } = require('electron');

// Store config in preload context
let reliantConfig = null;

// Expose protected methods that allow the renderer process to use
// the ipcRenderer without exposing the entire object
contextBridge.exposeInMainWorld('electronAPI', {
  // Get the current config
  getConfig: () => reliantConfig,
  getVersion: () => ipcRenderer.invoke('get-version'),
  getBackendPort: () => ipcRenderer.invoke('get-backend-port'),
  getWebPort: () => ipcRenderer.invoke('get-web-port'),
  getBackendStatus: () => ipcRenderer.invoke('get-backend-status'),
  restartBackend: () => ipcRenderer.invoke('restart-backend'),
  showErrorDialog: (title, content) => ipcRenderer.invoke('show-error-dialog', title, content),
  selectDirectory: () => ipcRenderer.invoke('select-directory'),
  openProjectDirectory: (path) => ipcRenderer.invoke('open-project-directory', path),
  openTerminal: (path) => ipcRenderer.invoke('open-terminal', path),
  openExternal: (url) => ipcRenderer.invoke('open-external', url),

  installCLI: () => ipcRenderer.invoke('install-cli'),
  getCliStatus: () => ipcRenderer.invoke('get-cli-status'),

  // Mock driver configuration
  getMockDriverConfig: () => ipcRenderer.invoke('get-mock-driver-config'),
  setMockDriverConfig: (config) => ipcRenderer.invoke('set-mock-driver-config', config),
  browseMockFile: () => ipcRenderer.invoke('browse-mock-file'),

  // Window management
  getWindowContext: () => ipcRenderer.invoke('get-window-context'),
  openWorktreeWindow: (worktreeData) => ipcRenderer.invoke('open-worktree-window', worktreeData),
  switchWorktree: (worktreeData) => ipcRenderer.invoke('switch-worktree', worktreeData),
  setWindowProject: (projectData) => ipcRenderer.invoke('set-window-project', projectData),
  getAllWindows: () => ipcRenderer.invoke('get-all-windows'),
  focusWindow: (windowId) => ipcRenderer.invoke('focus-window', windowId),
  createNewWindow: () => ipcRenderer.invoke('create-new-window'),
  createNewTab: () => ipcRenderer.invoke('create-new-tab'),
  closeCurrentTab: () => ipcRenderer.invoke('close-current-tab'),
  closeWindowIfNoTabs: (tabCount) => ipcRenderer.invoke('close-window-if-no-tabs', tabCount),

  // Window controls
  minimizeWindow: () => ipcRenderer.invoke('minimize-window'),
  maximizeWindow: () => ipcRenderer.invoke('maximize-window'),
  closeWindow: () => ipcRenderer.invoke('close-window'),
  getFullscreenStatus: () => ipcRenderer.invoke('get-fullscreen-status'),
  toggleFullscreen: () => ipcRenderer.invoke('toggle-fullscreen'),

  // Auto-updater
  checkForUpdates: () => ipcRenderer.invoke('check-for-updates'),
  downloadUpdate: () => ipcRenderer.invoke('download-update'),
  installUpdate: () => ipcRenderer.invoke('install-update'),

  // Auth storage
  authLoad: () => ipcRenderer.invoke('auth:load'),
  authSave: (session) => ipcRenderer.invoke('auth:save', session),
  authClear: () => ipcRenderer.invoke('auth:clear'),

  // Notifications
  showNotification: (options) => ipcRenderer.invoke('show-notification', options),
  getNotificationMuteStatus: () => ipcRenderer.invoke('tray:get-notification-mute-status'),
  setTrayStatus: (status) => ipcRenderer.invoke('tray:set-status', status),
  onNotificationClick: (callback) => {
    const listener = (event, tag) => callback(tag);
    ipcRenderer.on('notification-click', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('notification-click', listener);
  },

  // Event listeners
  onNotification: (callback) => {
    ipcRenderer.on('notification', (event, data) => callback(data));
  },
  onWorktreeContextChanged: (callback) => {
    const listener = (event, data) => callback(data);
    ipcRenderer.on('worktree-context-changed', listener);
    return () => ipcRenderer.removeListener('worktree-context-changed', listener);
  },
  onSetWorktreeContext: (callback) => {
    const listener = (event, data) => callback(data);
    ipcRenderer.on('set-worktree-context', listener);
    return () => ipcRenderer.removeListener('set-worktree-context', listener);
  },
  onCreateNewTab: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('create-new-tab', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('create-new-tab', listener);
  },
  onResumeLastChat: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('resume-last-chat', listener);
    return () => ipcRenderer.removeListener('resume-last-chat', listener);
  },
  onTrayGoToChat: (callback) => {
    const listener = (event, payload) => callback(payload || {});
    ipcRenderer.on('tray:go-to-chat', listener);
    return () => ipcRenderer.removeListener('tray:go-to-chat', listener);
  },
  onTrayGoToWorkflowHub: (callback) => {
    const listener = () => callback();
    ipcRenderer.on('tray:go-to-workflow-hub', listener);
    return () => ipcRenderer.removeListener('tray:go-to-workflow-hub', listener);
  },
  onTrayOpenWorkflow: (callback) => {
    const listener = (event, payload) => callback(payload || {});
    ipcRenderer.on('tray:open-workflow', listener);
    return () => ipcRenderer.removeListener('tray:open-workflow', listener);
  },
  onTrayGoToSettings: (callback) => {
    const listener = () => callback();
    ipcRenderer.on('tray:go-to-settings', listener);
    return () => ipcRenderer.removeListener('tray:go-to-settings', listener);
  },
  onTrayGoToProjectPicker: (callback) => {
    const listener = () => callback();
    ipcRenderer.on('tray:go-to-project-picker', listener);
    return () => ipcRenderer.removeListener('tray:go-to-project-picker', listener);
  },
  onTraySwitchWorkspace: (callback) => {
    const listener = (event, payload) => callback(payload || {});
    ipcRenderer.on('tray:switch-workspace', listener);
    return () => ipcRenderer.removeListener('tray:switch-workspace', listener);
  },
  onCloseCurrentTab: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('close-current-tab', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('close-current-tab', listener);
  },
  onReopenLastTab: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('reopen-last-tab', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('reopen-last-tab', listener);
  },
  onCloseAllTabs: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('close-all-tabs', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('close-all-tabs', listener);
  },
  onNavigateTab: (callback) => {
    const listener = (event, direction) => callback(direction);
    ipcRenderer.on('navigate-tab', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('navigate-tab', listener);
  },
  onFocusChatSearch: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('focus-chat-search', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('focus-chat-search', listener);
  },
  onFocusGlobalSearch: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('focus-global-search', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('focus-global-search', listener);
  },
  onProjectInfo: (callback) => {
    const listener = (event, data) => callback(data);
    ipcRenderer.on('project-info', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('project-info', listener);
  },
  onFullscreenChanged: (callback) => {
    const listener = (event, isFullscreen) => callback(isFullscreen);
    ipcRenderer.on('fullscreen-changed', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('fullscreen-changed', listener);
  },
  onToggleTerminal: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('toggle-terminal', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('toggle-terminal', listener);
  },
  onNewTerminal: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('new-terminal', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('new-terminal', listener);
  },
  onClearTerminal: (callback) => {
    const listener = (event) => callback();
    ipcRenderer.on('clear-terminal', listener);
    // Return cleanup function
    return () => ipcRenderer.removeListener('clear-terminal', listener);
  },

  // Privacy settings
  updatePrivacySettings: (settings) => ipcRenderer.invoke('update-privacy-settings', settings),
  getPrivacySettings: () => ipcRenderer.invoke('get-privacy-settings'),

  // Analytics
  analyticsTrack: (payload) => ipcRenderer.invoke('analytics:track', payload),

  // Browser API
  browser: {
    createTab: (tabId, url, paneId) => ipcRenderer.invoke('browser:create-tab', tabId, url, paneId),
    closeTab: (tabId) => ipcRenderer.invoke('browser:close-tab', tabId),
    setActiveTab: (tabId) => ipcRenderer.invoke('browser:set-active-tab', tabId),
    navigateTab: (tabId, url) => ipcRenderer.invoke('browser:navigate-tab', tabId, url),
    goBack: (tabId) => ipcRenderer.invoke('browser:go-back', tabId),
    goForward: (tabId) => ipcRenderer.invoke('browser:go-forward', tabId),
    reload: (tabId) => ipcRenderer.invoke('browser:reload', tabId),
    setBounds: (bounds, paneId) => ipcRenderer.invoke('browser:set-bounds', bounds, paneId),
    hideAll: () => ipcRenderer.invoke('browser:hide-all'),
    onTabUpdate: (callback) => {
      const listener = (event, data) => callback(data);
      ipcRenderer.on('browser-tab-update', listener);
      // Return cleanup function
      return () => ipcRenderer.removeListener('browser-tab-update', listener);
    },
  },

  // Platform information
  platform: process.platform,
  // isDevelopment is dynamically set from appInfo.isPackaged after IPC call
  // This static value is a fallback - the real value comes from RELIANT_CONFIG.isDev
  isDevelopment: false,

  // Logging from renderer to main process
  log: (level, ...args) => ipcRenderer.send('log-from-renderer', level, ...args),

  // Listen for backend port updates
  onBackendPort: (callback) => {
    const listener = (event, port) => callback(port);
    ipcRenderer.on('backend-port', listener);
    return () => ipcRenderer.removeListener('backend-port', listener);
  },

  // Listen for open project events
  onOpenProject: (callback) => {
    const listener = (event, projectPath) => callback(projectPath);
    ipcRenderer.on('open-project', listener);
    return () => ipcRenderer.removeListener('open-project', listener);
  },

  // Listen for auto-updater status updates
  onUpdateStatus: (callback) => {
    const listener = (event, status) => callback(status);
    ipcRenderer.on('update-status', listener);
    return () => ipcRenderer.removeListener('update-status', listener);
  },


});

// Helper to log to main process
const log = (level, ...args) => {
  ipcRenderer.send('log-from-renderer', level, '[Preload]', ...args);
};

// Inject global configuration for the web app
const injectReliantConfig = async () => {
  log('info', 'injectReliantConfig called');

  try {
    log('info', 'Getting daemon status via IPC...');
    const [daemonPort, appInfo, backendStatus] = await Promise.all([
      ipcRenderer.invoke('get-backend-port'),
      ipcRenderer.invoke('get-app-info'),
      ipcRenderer.invoke('get-backend-status')
    ]);

    log('info', 'Daemon port received:', daemonPort, 'type:', typeof daemonPort);
    log('info', 'App info received:', appInfo);
    log('info', 'Backend status received:', backendStatus);

    /**
     * Build the reliantConfig from backend status.
     * The frontend connects to the hosted API URL (not localhost) since
     * the Electron app now runs a local daemon that bridges to the cloud.
     */
    const buildConfig = (status, info) => {
      // The API URL comes from the daemon's configuration (hosted API)
      const apiUrl = status?.apiUrl || 'https://reliantapi.com';
      return {
        grpcUrl: apiUrl,
        apiUrl: apiUrl,
        gatewayUrl: status?.gatewayUrl || '',
        daemonPort: status?.daemonPort || null,
        isElectron: true,
        platform: info.platform,
        isDev: !info.isPackaged
      };
    };

    // Check if the daemon is running (has a valid port)
    if (typeof daemonPort === 'number' && daemonPort > 0) {
      // Daemon is ready, store config in preload context
      reliantConfig = buildConfig(backendStatus, appInfo);

      // Notify renderer that config is ready via postMessage (works with context isolation)
      window.postMessage({ type: 'reliant-config-ready', config: reliantConfig }, '*');
      log('info', 'Config ready message posted');

      // Listen for daemon restart events
      ipcRenderer.on('backend-port', async (event, newPort) => {
        const latestStatus = await ipcRenderer.invoke('get-backend-status');
        reliantConfig = buildConfig(latestStatus, appInfo);
        reliantConfig.daemonPort = newPort;

        // Notify renderer of config change via postMessage
        window.postMessage({ type: 'backend-port-changed', port: newPort }, '*');
      });
    } else {
      log('info', 'Daemon not ready yet (port:', daemonPort, '), waiting for backend-port event...');
      // Daemon not ready, wait for it via event
      ipcRenderer.once('backend-port', async (event, port) => {
        log('info', 'Received backend-port event with port:', port);
        if (typeof port === 'number' && port > 0) {
          const [latestAppInfo, latestStatus] = await Promise.all([
            ipcRenderer.invoke('get-app-info'),
            ipcRenderer.invoke('get-backend-status')
          ]);

          reliantConfig = buildConfig(latestStatus, latestAppInfo);
          reliantConfig.daemonPort = port;

          log('info', 'Config stored from event:', reliantConfig);

          // Notify renderer via postMessage
          window.postMessage({ type: 'reliant-config-ready', config: reliantConfig }, '*');
          log('info', 'reliant-config-ready event dispatched from event handler');
        } else {
          log('error', 'Invalid port received from backend-port event:', port);
        }

        // Set up ongoing listener for daemon restarts
        ipcRenderer.on('backend-port', async (event, newPort) => {
          const latestStatus = await ipcRenderer.invoke('get-backend-status');
          reliantConfig = buildConfig(latestStatus, appInfo);
          reliantConfig.daemonPort = newPort;

          window.dispatchEvent(new CustomEvent('backend-port-changed', {
            detail: { port: newPort }
          }));
        });
      });
    }
  } catch (error) {
    log('error', 'Error in config injection:', error);
    log('error', 'Error stack:', error.stack);

    // Still set up listener for backend-port event as fallback
    log('info', 'Setting up fallback listener for backend-port event...');
    ipcRenderer.once('backend-port', async (event, port) => {
      log('info', 'Fallback: Received backend-port event with port:', port);
      const [appInfo, backendStatus] = await Promise.all([
        ipcRenderer.invoke('get-app-info'),
        ipcRenderer.invoke('get-backend-status')
      ]);

      const apiUrl = backendStatus?.apiUrl || 'https://reliantapi.com';
      reliantConfig = {
        grpcUrl: apiUrl,
        apiUrl: apiUrl,
        gatewayUrl: backendStatus?.gatewayUrl || '',
        daemonPort: port,
        isElectron: true,
        platform: appInfo.platform,
        isDev: !appInfo.isPackaged
      };

      log('info', 'Fallback: Config stored:', reliantConfig);

      window.postMessage({ type: 'reliant-config-ready', config: reliantConfig }, '*');
      log('info', 'Fallback: reliant-config-ready event dispatched');
    });
  }
};

// Add debug to track config state
log('info', 'Script loaded, reliantConfig initial state:', reliantConfig);

// Inject configuration immediately AND when DOM is ready (for reloads)
log('info', 'Calling injectReliantConfig() immediately...');
injectReliantConfig().catch(err => {
  console.error('[Preload] Failed to inject config:', err);
});

window.addEventListener('DOMContentLoaded', () => {
  log('info', 'DOMContentLoaded event fired, calling injectReliantConfig() again...');
  log('info', 'Current reliantConfig state:', reliantConfig);
  injectReliantConfig().catch(err => {
    console.error('[Preload] Failed to inject config on DOMContentLoaded:', err);
  });
});