/**
 * Window State Client
 * 
 * Fetches and saves window state via the backend API.
 * This ensures window state is stored locally per-worktree in ./data/window-state.json
 */

const http = require('http');
const https = require('https');
const log = require('./logger');

// Debounce timer for saving state
let saveDebounceTimer = null;
const SAVE_DEBOUNCE_MS = 500;

/**
 * Makes an HTTP/HTTPS request to the backend
 * @param {string} method - HTTP method
 * @param {number} port - Backend port
 * @param {string} path - API path
 * @param {object} [data] - Request body for POST/PUT
 * @param {boolean} [useTLS=true] - Whether to use HTTPS
 * @returns {Promise<object>} Response data
 */
function makeRequest(method, port, path, data = null, useTLS = true) {
  return new Promise((resolve, reject) => {
    const options = {
      hostname: 'localhost',
      port: port,
      path: `/api/v2${path}`,
      method: method,
      headers: {
        'Content-Type': 'application/json',
      },
      timeout: 5000,
      rejectUnauthorized: false, // Accept self-signed certificates
    };

    const protocol = useTLS ? https : http;
    const req = protocol.request(options, (res) => {
      let body = '';
      res.on('data', chunk => { body += chunk; });
      res.on('end', () => {
        try {
          const parsed = JSON.parse(body);
          if (res.statusCode >= 200 && res.statusCode < 300) {
            resolve(parsed);
          } else {
            reject(new Error(parsed.message || `Request failed with status ${res.statusCode}`));
          }
        } catch (e) {
          if (res.statusCode >= 200 && res.statusCode < 300) {
            resolve({});
          } else {
            reject(new Error(`Request failed with status ${res.statusCode}`));
          }
        }
      });
    });

    req.on('error', reject);
    req.on('timeout', () => {
      req.destroy();
      reject(new Error('Request timeout'));
    });

    if (data) {
      req.write(JSON.stringify(data));
    }
    req.end();
  });
}

/**
 * Fetches window state from the backend
 * @param {number} port - Backend port
 * @param {boolean} [useTLS=true] - Whether to use HTTPS
 * @returns {Promise<object|null>} Window state or null if not found
 */
async function getWindowState(port, useTLS = true) {
  try {
    const response = await makeRequest('GET', port, '/window-state', null, useTLS);
    return response.state || null;
  } catch (error) {
    log.debug('[WindowStateClient] Failed to get window state:', error.message);
    return null;
  }
}

/**
 * Saves window state to the backend (debounced)
 * @param {number} port - Backend port
 * @param {object} state - Window state to save
 * @param {boolean} [useTLS=true] - Whether to use HTTPS
 */
function saveWindowState(port, state, useTLS = true) {
  // Clear any pending save
  if (saveDebounceTimer) {
    clearTimeout(saveDebounceTimer);
  }

  // Debounce the save
  saveDebounceTimer = setTimeout(async () => {
    try {
      await makeRequest('POST', port, '/window-state', state, useTLS);
      log.debug('[WindowStateClient] Window state saved');
    } catch (error) {
      log.warn('[WindowStateClient] Failed to save window state:', error.message);
    }
  }, SAVE_DEBOUNCE_MS);
}

/**
 * Saves window state immediately (bypasses debounce)
 * @param {number} port - Backend port
 * @param {object} state - Window state to save
 * @param {boolean} [useTLS=true] - Whether to use HTTPS
 * @returns {Promise<void>}
 */
async function saveWindowStateImmediate(port, state, useTLS = true) {
  // Clear any pending debounced save
  if (saveDebounceTimer) {
    clearTimeout(saveDebounceTimer);
    saveDebounceTimer = null;
  }

  try {
    await makeRequest('POST', port, '/window-state', state, useTLS);
    log.debug('[WindowStateClient] Window state saved immediately');
  } catch (error) {
    log.warn('[WindowStateClient] Failed to save window state:', error.message);
  }
}

/**
 * Clears window state from the backend
 * @param {number} port - Backend port
 * @param {boolean} [useTLS=true] - Whether to use HTTPS
 * @returns {Promise<void>}
 */
async function clearWindowState(port, useTLS = true) {
  try {
    await makeRequest('DELETE', port, '/window-state', null, useTLS);
    log.debug('[WindowStateClient] Window state cleared');
  } catch (error) {
    log.warn('[WindowStateClient] Failed to clear window state:', error.message);
  }
}

/**
 * Extracts window state from a BrowserWindow
 * @param {BrowserWindow} window - Electron BrowserWindow
 * @returns {object} Window state
 */
function getStateFromWindow(window) {
  if (!window || window.isDestroyed()) {
    return null;
  }

  const bounds = window.getBounds();
  return {
    x: bounds.x,
    y: bounds.y,
    width: bounds.width,
    height: bounds.height,
    isMaximized: window.isMaximized(),
    isFullScreen: window.isFullScreen(),
  };
}

/**
 * Applies window state to a BrowserWindow
 * @param {BrowserWindow} window - Electron BrowserWindow
 * @param {object} state - Window state to apply
 */
function applyStateToWindow(window, state) {
  if (!window || window.isDestroyed() || !state) {
    return;
  }

  // Apply bounds
  if (state.width && state.height) {
    window.setBounds({
      x: state.x,
      y: state.y,
      width: state.width,
      height: state.height,
    });
  }

  // Apply maximized/fullscreen state
  if (state.isMaximized) {
    window.maximize();
  } else if (state.isFullScreen) {
    window.setFullScreen(true);
  }
}

module.exports = {
  getWindowState,
  saveWindowState,
  saveWindowStateImmediate,
  clearWindowState,
  getStateFromWindow,
  applyStateToWindow,
};
