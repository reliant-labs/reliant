// Simple theme manager - only font settings remain
import { SETTINGS_KEYS } from "../services/settingsSync";

import { FONT_SIZE_MAP, applyRootFontSize, DEFAULT_FONT_SIZE } from "./rootFontSize";

function safeGet(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) || fallback;
  } catch {
    return fallback;
  }
}

export function loadFont() {
  // "default" (bundled Inter), matching every other read site. This path was
  // left on "system" when the default typeface changed, so which face a new
  // user saw depended on which code path applied settings first.
  const font = safeGet(SETTINGS_KEYS.FONT, "default");
  const chatFont = safeGet(SETTINGS_KEYS.CHAT_FONT, "mono");
  const editorFont = safeGet(SETTINGS_KEYS.EDITOR_FONT, "mono");
  const fontSize = safeGet(SETTINGS_KEYS.FONT_SIZE, DEFAULT_FONT_SIZE);

  document.documentElement.dataset.font = font;
  document.documentElement.dataset.chatFont = chatFont;
  document.documentElement.dataset.editorFont = editorFont;

  if (FONT_SIZE_MAP[fontSize]) {
    applyRootFontSize(fontSize);
  }
}

// Extend Window interface for theme tracking
declare global {
  interface Window {
    __THEME_APPLIED__?: boolean;
  }
}

// Initialize on load
(function init() {
  // On first import, apply fonts
  if (!window.__THEME_APPLIED__) {
    loadFont();
    window.__THEME_APPLIED__ = true;
  }
})();

export { FONT_SIZE_MAP }; // side-effect module
