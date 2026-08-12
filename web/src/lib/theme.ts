// Simple theme manager - only font settings remain
import { SETTINGS_KEYS } from "../services/settingsSync";

import { FONT_SIZE_MAP, applyRootFontSize } from "./rootFontSize";

function safeGet(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) || fallback;
  } catch {
    return fallback;
  }
}

export function loadFont() {
  const font = safeGet(SETTINGS_KEYS.FONT, "system");
  const chatFont = safeGet(SETTINGS_KEYS.CHAT_FONT, "mono");
  const editorFont = safeGet(SETTINGS_KEYS.EDITOR_FONT, "mono");
  const fontSize = safeGet(SETTINGS_KEYS.FONT_SIZE, "md");

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
