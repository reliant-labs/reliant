const { app } = require('electron');
const path = require('path');

/**
 * Shared window configuration to ensure consistency across all BrowserWindow instances.
 * This prevents bugs where settings (like webviewTag) are added to one window creation
 * path but not another.
 */

/**
 * Get the preload script path based on whether the app is packaged
 */
function getPreloadPath() {
  return app.isPackaged
    ? path.join(process.resourcesPath, 'preload.js')
    : path.join(__dirname, 'preload.js');
}

/**
 * Common webPreferences for all BrowserWindows
 * These settings are security-critical and should be consistent across all windows
 */
function getWebPreferences() {
  return {
    nodeIntegration: false,
    contextIsolation: true,
    spellcheck: true,
    enableRemoteModule: false,
    webviewTag: true, // Required for browser embedding feature
    preload: getPreloadPath(),
  };
}

/**
 * Common window options shared across all windows
 */
function getCommonWindowOptions() {
  return {
    minWidth: 800,
    minHeight: 600,
    icon: path.join(__dirname, '../build/icon.png'),
    show: false, // Windows start hidden until ready
    frame: false,
    trafficLightPosition: { x: 12, y: 12 },
  };
}

/**
 * Get platform-specific title bar style
 * @param {string} variant - 'inset' for hiddenInset (main window), 'hidden' for hidden (other windows)
 */
function getTitleBarStyle(variant = 'hidden') {
  if (process.platform === 'darwin') {
    return variant === 'inset' ? 'hiddenInset' : 'hidden';
  }
  return 'hidden';
}

module.exports = {
  getPreloadPath,
  getWebPreferences,
  getCommonWindowOptions,
  getTitleBarStyle,
};
