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

  // Ask whether the daemon is connected RIGHT NOW.
  //
  // Pairs with onDaemonConnected: the event is the fast path for a renderer
  // that is already listening, and this is how a renderer that mounted late
  // (the post-sign-in reload) finds out without waiting for a poll.
  isDaemonConnected: () => ipcRenderer.invoke('daemon:is-connected'),

  // Fires when the local daemon's gateway stream comes up.
  //
  // Consumers should treat this as a REFETCH TRIGGER, not as data: the daemon
  // becomes listable via ListDaemons at registration, which was measured 1.1s
  // before the connected event. Acting on the event alone would race.
  onDaemonConnected: (callback) => {
    const listener = (_event, payload) => callback(payload);
    ipcRenderer.on('daemon-connected', listener);
    return () => ipcRenderer.removeListener('daemon-connected', listener);
  },

  // Start the RFC 8252 loopback listener and return the redirect_uri the
  // provider should send the code to.
  //
  // This line is the whole desktop OAuth flow. main.js has handled
  // `oauth:start-redirect` and oauth-loopback.js has had the listener, but
  // this exposure was never written — so `window.electronAPI.startOAuthRedirect`
  // was `undefined` in EVERY build, packaged and dev alike. The renderer's
  // `bridge?.startOAuthRedirect` guard then skipped silently (its warn is
  // inside the branch it never entered), fell back to the hosted callback, and
  // `opensExternally()` — reading the same absent key — stayed false, so
  // Electron navigated ITSELF to the provider. navigation-policy.js
  // externalized that to the system browser, the code landed in a browser tab,
  // and the renderer holding the PKCE verifier was stranded on the old route.
  // Sign-in could not complete on the desktop app at all.
  //
  // electron.d.ts declares this `startOAuthRedirect?:` — OPTIONAL, so the
  // compiler could never catch its absence. That is the same trap that made
  // `onOAuthCallback` inert before it (see main.js's handler comment); the
  // optional marker is load-bearing for old shipped builds, so the guard
  // against a third occurrence has to be a test, not a type.
  //
  // Rejects rather than resolving falsy when the listener cannot start: the
  // renderer's fallback lives in one catch block, and a falsy resolve would
  // route past it (electron.d.ts states this contract).
  startOAuthRedirect: async () => {
    const result = await ipcRenderer.invoke('oauth:start-redirect');
    if (!result?.success || !result.redirectUri) {
      throw new Error(result?.error || 'OAuth loopback listener failed to start');
    }
    return { redirectUri: result.redirectUri };
  },

  // Provider sign-in (Claude Code / Codex).
  //
  // Two calls, not one, and that split IS the fix. This used to be a single
  // DaemonService/StartOAuthFlow RPC that stayed open across the user's whole
  // browser round trip; it failed at ~15s with "Failed to fetch", and tearing
  // down the request tore down the listener, so the redirect landed on a
  // closed port. Start binds the port and returns immediately; wait spans
  // human time over IPC, where no proxy or gateway can time it out.
  //
  // Optional in electron.d.ts like every bridge here, so the renderer keeps a
  // working fallback on builds that predate it.
  startProviderOAuth: async (authorizeUrlTemplate) => {
    const result = await ipcRenderer.invoke('oauth:provider-login-start', authorizeUrlTemplate);
    if (!result?.success) {
      throw new Error(result?.error || 'Could not start the sign-in listener');
    }
    return { flowId: result.flowId, redirectUri: result.redirectUri };
  },

  waitForProviderOAuth: async (flowId) => {
    const result = await ipcRenderer.invoke('oauth:provider-login-wait', flowId);
    if (!result?.success) {
      throw new Error(result?.error || 'Sign-in did not complete');
    }
    return { code: result.code, state: result.state, redirectUri: result.redirectUri };
  },

  cancelProviderOAuth: (flowId) => ipcRenderer.invoke('oauth:provider-login-cancel', flowId),

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

  // Keyboard shortcuts — push effective bindings so the native menu's
  // accelerators track the user's remaps instead of silently overriding them.
  updateShortcutBindings: (bindings) => ipcRenderer.invoke('shortcuts:update', bindings),

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
  log('warn', '[Preload] injectReliantConfig called', { atMs: Date.now() });

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
      // apiUrl         = daemon --server URL (admin-server in cloud-dev, the
      //                  build-config-injected hosted endpoint in packaged
      //                  commercial builds). Renderer code that reads
      //                  RELIANT_CONFIG.apiUrl for non-grpc purposes still
      //                  expects this. The `||` is a defensive fallback only —
      //                  status.apiUrl is virtually always set by BackendManager;
      //                  default it to NEUTRAL localhost (not a hosted host) so
      //                  the OSS build ships no Reliant-hosted config.
      // rendererApiUrl = where the renderer's MAIN Connect transport hits for
      //                  reliant.v1.* services (ProjectService, ChatService, …).
      //                  In prod the two collapse; in cloud-dev they're distinct
      //                  processes on different ports. getGRPCBaseURL() in
      //                  web/src/api/grpc-client.ts reads window.RELIANT_CONFIG.grpcUrl.
      const apiUrl = status?.apiUrl || 'http://localhost:8080';
      const rendererApiUrl = status?.rendererApiUrl || apiUrl;
      return {
        grpcUrl: rendererApiUrl,
        apiUrl: apiUrl,
        gatewayUrl: status?.gatewayUrl || '',
        // controlPlaneURL = the admin-server origin (billing, coupons,
        // /api/proxy-session). getControlPlaneURL() in web/src/lib/constants.ts
        // prefers THIS over the build-time VITE_CONTROL_PLANE_API_URL, so
        // injecting it at runtime lets a config change reach the app without
        // rebuilding the renderer. It was never set, which is why coupons threw
        // "Control plane API URL not configured" in the packaged build.
        controlPlaneURL: status?.controlPlaneUrl || '',
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

      // Defensive fallback only — backendStatus.apiUrl is set by BackendManager.
      // Default to NEUTRAL localhost (not a hosted host) per the OSS-clean
      // contract; the hosted endpoint reaches the renderer via backendStatus.
      const apiUrl = backendStatus?.apiUrl || 'http://localhost:8080';
      const rendererApiUrl = backendStatus?.rendererApiUrl || apiUrl;
      reliantConfig = {
        grpcUrl: rendererApiUrl,
        apiUrl: apiUrl,
        gatewayUrl: backendStatus?.gatewayUrl || '',
        controlPlaneURL: backendStatus?.controlPlaneUrl || '',
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