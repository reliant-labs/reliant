const { BrowserWindow, ipcMain, dialog, shell, app } = require('electron');
const path = require('path');
const log = require('./logger');
const windowConfig = require('./window-config');
const { shouldOpenExternally } = require('./navigation-policy');
const { APP_INDEX_URL } = require('./app-protocol');
// Note: Window state persistence is now handled in main.js via the backend API
// Each worktree stores its own window state in ./data/window-state.json

class WindowManager {
  constructor() {
    this.windows = new Map(); // Map of window ID to window metadata
    this.worktreeWindows = new Map(); // Map of worktree ID to window ID
    this.setupIpcHandlers();
  }

  async createWindow(options = {}) {
    const {
      worktreeId = null,
      projectId = null,
      projectPath = null,
      projectName = 'Reliant',
      width = 1400,
      height = 900,
      isNewWindow = false
    } = options;

    // Get current directory name for window title
    const currentDir = path.basename(process.cwd());
    const windowTitle = projectName === 'Reliant' ?
      `Reliant - ${currentDir}` :
      `${projectName} - ${currentDir}`;

    const window = new BrowserWindow({
      width,
      height,
      title: windowTitle,
      webPreferences: windowConfig.getWebPreferences(),
      ...windowConfig.getCommonWindowOptions(),
      titleBarStyle: windowConfig.getTitleBarStyle('hidden'),
      // Background color with slight opacity helps traffic lights stand out
      ...(process.platform === 'darwin' && {
        backgroundColor: '#f5f5f5'
      })
    });

    // Handle external links
    window.webContents.setWindowOpenHandler(({ url }) => {
      shell.openExternal(url);
      return { action: 'deny' };
    });

    window.webContents.on('will-navigate', (event, url) => {
      if (shouldOpenExternally(url, window.webContents.getURL())) {
        event.preventDefault();
        shell.openExternal(url);
      }
    });


    // Load the app
    if (!app.isPackaged) {
      const webPort = process.env.FRONTEND_PORT || process.env.WEB_PORT || 5173;

      // In development, try to load the URL with retries
      const loadDevURL = async () => {
        const maxRetries = 30;
        const retryDelay = 1000;

        for (let i = 0; i < maxRetries; i++) {
          try {
            await window.loadURL(`http://localhost:${webPort}`);
            log.info(`[WindowManager] Loaded development server on port ${webPort}`);
            window.webContents.openDevTools();
            // In development, show the window immediately after loading
            window.show();
            return;
          } catch (err) {
            log.debug(`[WindowManager] Waiting for Vite dev server on port ${webPort} (attempt ${i + 1}/${maxRetries})`);
            if (i < maxRetries - 1) {
              await new Promise(resolve => setTimeout(resolve, retryDelay));
            } else {
              throw new Error(`WindowManager: Failed to connect to development server on port ${webPort} after ${maxRetries} attempts`);
            }
          }
        }
      };

      await loadDevURL();
    } else {
      // Served over app:// for the same reason as the main window: the bundle's
      // root-absolute asset paths resolve against the filesystem root under
      // file://, which yields a blank window. See app-protocol.js.
      log.info('[WindowManager] Loading renderer from:', APP_INDEX_URL);
      await window.loadURL(APP_INDEX_URL);
    }

    // Store window metadata
    const windowId = window.id;
    this.windows.set(windowId, {
      id: windowId,
      worktreeId,
      projectId,
      projectPath,
      projectName,
      window,
      isNewWindow
    });

    // Note: Window state persistence is now handled in main.js via the backend API

    // Map worktree to window if provided
    if (worktreeId) {
      this.worktreeWindows.set(worktreeId, windowId);

      // Send initial worktree context to the window
      window.webContents.once('did-finish-load', () => {
        const contextData = {
          worktreeId,
          projectId,
          projectName
        };

        // Add a small delay to ensure the frontend is ready to receive the context
        setTimeout(() => {
          window.webContents.send('set-worktree-context', contextData);
        }, 1000);
      });
    } else if (!isNewWindow && projectPath) {
      // Send project info for existing project windows
      window.webContents.once('did-finish-load', () => {
        window.webContents.send('project-info', {
          projectPath,
          projectName,
          isWorktree: false
        });
      });
    }
    // For new windows (isNewWindow=true), don't send any project info
    // so the project picker will be shown

    // Force traffic light buttons to always be visible (even when window is unfocused)
    if (process.platform === 'darwin') {
      window.setWindowButtonVisibility(true);
    }

    window.once('ready-to-show', () => {
      window.show();
    });

    // Fallback: show window after a delay if ready-to-show doesn't fire
    setTimeout(() => {
      if (!window.isDestroyed() && !window.isVisible()) {
        window.show();
      }
    }, 3000);

    // Listen for fullscreen changes
    window.on('enter-full-screen', () => {
      // Update traffic light position in fullscreen to be at the edge
      if (process.platform === 'darwin') {
        window.setWindowButtonPosition({ x: 12, y: 12 });
      }
      window.webContents.send('fullscreen-changed', true);
    });

    window.on('leave-full-screen', () => {
      // Restore traffic light position
      if (process.platform === 'darwin') {
        window.setWindowButtonPosition({ x: 12, y: 12 });
      }
      window.webContents.send('fullscreen-changed', false);
    });

    window.on('closed', () => {
      const metadata = this.windows.get(windowId);
      if (metadata && metadata.worktreeId) {
        this.worktreeWindows.delete(metadata.worktreeId);
      }
      this.windows.delete(windowId);
      // Note: Window state persistence is handled in main.js via gracefulShutdown
    });

    return window;
  }

  registerWindow(window, metadata) {
    const windowId = window.id;
    
    // Check if window is already registered to avoid duplicate event listeners
    if (this.windows.has(windowId)) {
      log.debug(`[WindowManager] Window ${windowId} already registered, updating metadata`);
      const existing = this.windows.get(windowId);
      existing.worktreeId = metadata.worktreeId;
      existing.projectId = metadata.projectId;
      existing.projectName = metadata.projectName;
      return;
    }
    
    this.windows.set(windowId, {
      id: windowId,
      window,
      worktreeId: metadata.worktreeId,
      projectId: metadata.projectId,
      projectName: metadata.projectName
    });

    // Set up window cleanup - only once per window
    window.on('closed', () => {
      if (metadata.worktreeId) {
        this.worktreeWindows.delete(metadata.worktreeId);
      }
      this.windows.delete(windowId);
      // Note: Window state persistence is handled in main.js via gracefulShutdown
    });
  }

  async openWorktreeWindow(worktreeData) {
    const { id: worktreeId, project_id: projectId, name, branch } = worktreeData;

    // Check if window already exists for this worktree
    if (this.worktreeWindows.has(worktreeId)) {
      const windowId = this.worktreeWindows.get(worktreeId);
      const metadata = this.windows.get(windowId);
      if (metadata && metadata.window && !metadata.window.isDestroyed()) {
        metadata.window.focus();
        return metadata.window;
      }
    }

    // Create new window for worktree
    const windowOptions = {
      worktreeId,
      projectId,
      projectName: `${name} (${branch})`
    };
    return await this.createWindow(windowOptions);
  }

  getWindowForWorktree(worktreeId) {
    const windowId = this.worktreeWindows.get(worktreeId);
    if (!windowId) return null;

    const metadata = this.windows.get(windowId);
    return metadata ? metadata.window : null;
  }

  getAllWindows() {
    return Array.from(this.windows.values());
  }

  getWindowMetadata(windowId) {
    return this.windows.get(windowId);
  }

  broadcastToAllWindows(channel, data) {
    this.windows.forEach(metadata => {
      if (metadata.window && !metadata.window.isDestroyed()) {
        metadata.window.webContents.send(channel, data);
      }
    });
  }

  setupIpcHandlers() {
    // Open new window for worktree
    ipcMain.handle('open-worktree-window', async (event, worktreeData) => {
      try {
        const window = await this.openWorktreeWindow(worktreeData);
        return { success: true, windowId: window.id };
      } catch (error) {
        log.error('[WindowManager] Failed to create worktree window:', error);
        return { success: false, error: error.message };
      }
    });

    // Switch worktree in current window
    ipcMain.handle('switch-worktree', (event, worktreeData) => {
      const windowId = BrowserWindow.fromWebContents(event.sender).id;
      const metadata = this.windows.get(windowId);

      if (metadata) {
        // Update metadata
        const oldWorktreeId = metadata.worktreeId;
        metadata.worktreeId = worktreeData.id;
        metadata.projectId = worktreeData.project_id;
        metadata.projectName = `${worktreeData.name} (${worktreeData.branch})`;

        // Update window title
        if (metadata.window && !metadata.window.isDestroyed()) {
          metadata.window.setTitle(`${metadata.projectName} - Reliant`);
        }

        // Update worktree mapping
        if (oldWorktreeId) {
          this.worktreeWindows.delete(oldWorktreeId);
        }
        if (worktreeData.id) {
          this.worktreeWindows.set(worktreeData.id, windowId);
        }

        // Notify window of context change
        event.sender.send('worktree-context-changed', {
          worktreeId: worktreeData.id,
          projectId: worktreeData.project_id,
          projectName: metadata.projectName
        });
      }

      return { success: true };
    });

    // Get all windows info
    ipcMain.handle('get-all-windows', () => {
      return this.getAllWindows().map(metadata => ({
        id: metadata.id,
        worktreeId: metadata.worktreeId,
        projectId: metadata.projectId,
        projectName: metadata.projectName,
        isFocused: metadata.window && metadata.window.isFocused()
      }));
    });

    // Focus a specific window
    ipcMain.handle('focus-window', (event, windowId) => {
      const metadata = this.windows.get(windowId);
      if (metadata && metadata.window && !metadata.window.isDestroyed()) {
        metadata.window.focus();
        return { success: true };
      }
      return { success: false, error: 'Window not found' };
    });

    // Create new window
    ipcMain.handle('create-new-window', async (event) => {
      try {
        const window = await this.createWindow({ isNewWindow: true });
        return { success: true, windowId: window.id };
      } catch (error) {
        log.error('[WindowManager] Failed to create new window:', error);
        return { success: false, error: error.message };
      }
    });

    // Set project for current window (called when user selects a project)
    ipcMain.handle('set-window-project', (event, projectData) => {
      const window = BrowserWindow.fromWebContents(event.sender);
      if (!window) {
        return { success: false, error: 'Window not found' };
      }

      const windowId = window.id;
      const metadata = this.windows.get(windowId);

      if (metadata) {
        // Update metadata
        metadata.projectPath = projectData.projectPath;
        metadata.projectName = projectData.projectName || projectData.projectPath;
        metadata.projectId = projectData.projectId;

        // Update window title
        if (!window.isDestroyed()) {
          const currentDir = require('path').basename(metadata.projectPath);
          window.setTitle(`${metadata.projectName} - ${currentDir}`);
        }

        log.debug('[WindowManager] Updated window project:', metadata.projectPath);
      }

      return { success: true };
    });
  }

  // Clean up all windows
  async closeAllWindows() {
    const promises = [];
    this.windows.forEach(metadata => {
      if (metadata.window && !metadata.window.isDestroyed()) {
        promises.push(new Promise((resolve) => {
          // Set a timeout in case the window doesn't close
          const timeout = setTimeout(() => {
            log.debug(`[WindowManager] Window ${metadata.id} close timeout, forcing destroy`);
            if (!metadata.window.isDestroyed()) {
              metadata.window.destroy();
            }
            resolve();
          }, 5000);

          metadata.window.once('closed', () => {
            clearTimeout(timeout);
            resolve();
          });

          // Force close without preventing
          metadata.window.destroy();
        }));
      }
    });

    // Wait for all windows to close with a maximum timeout
    return Promise.race([
      Promise.all(promises),
      new Promise(resolve => setTimeout(() => {
        log.debug('[WindowManager] Window close timeout reached, continuing');
        resolve();
      }, 6000))
    ]);
  }
}

module.exports = WindowManager;