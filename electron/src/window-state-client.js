/**
 * Window State Client
 * 
 * Reads and writes window state to a local JSON file at ~/.reliant/window-state.json
 */

const fs = require('fs');
const path = require('path');
const os = require('os');
const log = require('./logger');

const STATE_PATH = path.join(os.homedir(), '.reliant', 'window-state.json');

// Debounce timer for saving state
let saveDebounceTimer = null;
const SAVE_DEBOUNCE_MS = 500;

function getWindowState() {
  try {
    const data = fs.readFileSync(STATE_PATH, 'utf-8');
    return JSON.parse(data);
  } catch (error) {
    if (error.code !== 'ENOENT') {
      log.debug('[WindowStateClient] Failed to read window state:', error.message);
    }
    return null;
  }
}

function saveWindowState(state) {
  if (saveDebounceTimer) clearTimeout(saveDebounceTimer);
  saveDebounceTimer = setTimeout(() => {
    try {
      fs.mkdirSync(path.dirname(STATE_PATH), { recursive: true });
      fs.writeFileSync(STATE_PATH, JSON.stringify(state, null, 2));
      log.debug('[WindowStateClient] Window state saved');
    } catch (error) {
      log.warn('[WindowStateClient] Failed to save window state:', error.message);
    }
  }, SAVE_DEBOUNCE_MS);
}

function saveWindowStateImmediate(state) {
  if (saveDebounceTimer) { clearTimeout(saveDebounceTimer); saveDebounceTimer = null; }
  try {
    fs.mkdirSync(path.dirname(STATE_PATH), { recursive: true });
    fs.writeFileSync(STATE_PATH, JSON.stringify(state, null, 2));
    log.debug('[WindowStateClient] Window state saved immediately');
  } catch (error) {
    log.warn('[WindowStateClient] Failed to save window state:', error.message);
  }
}

function clearWindowState() {
  try {
    fs.unlinkSync(STATE_PATH);
    log.debug('[WindowStateClient] Window state cleared');
  } catch (error) {
    if (error.code !== 'ENOENT') {
      log.warn('[WindowStateClient] Failed to clear window state:', error.message);
    }
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
