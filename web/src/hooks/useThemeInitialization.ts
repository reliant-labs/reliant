/**
 * Hook to initialize theme from database settings
 * 
 * This hook waits for settingsSync to be initialized from the database,
 * then applies all appearance settings (theme, color scheme, fonts).
 * 
 * Note: index.html applies settings from localStorage immediately to prevent flash.
 * This hook re-applies from database after sync to ensure consistency.
 */
import { useEffect } from "react";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import { logger } from "../lib/logger";
import { applyRootFontSize } from "../lib/rootFontSize";


const COLOR_SCHEME_MAP: Record<string, string> = {
  blue: "professional-blue",
  neutral: "refined-neutral",
  teal: "modern-teal",
  slate: "slate",
  forest: "forest",
  purple: "purple-classic",
  pink: "vibrant-pink",
  orange: "energetic-orange",
  red: "bold-red",
  black: "pure-black",
};

export function useThemeInitialization() {
  useEffect(() => {
    let cancelled = false;
    
    const initTheme = async () => {
      // Poll for settingsSync initialization with timeout
      const maxAttempts = 50; // 5 seconds max (50 * 100ms)
      let attempts = 0;
      
      while (!settingsSync.isInitialized() && attempts < maxAttempts) {
        await new Promise(resolve => setTimeout(resolve, 100));
        attempts++;
        if (cancelled) return;
      }
      
      if (!settingsSync.isInitialized()) {
        logger.warn("[Theme] Timeout waiting for settingsSync, using localStorage values");
        // Settings from localStorage were already applied by index.html
        return;
      }

      // Check if settings were already applied by Root.tsx
      // Root.tsx applies settings immediately after settingsSync.initialize()
      if (settingsSync.hasAppliedSettingsToDOM()) {
        logger.debug("[Theme] Settings already applied by Root.tsx, skipping re-application");
        return;
      }

      logger.info("[Theme] Settings sync initialized, applying database settings (fallback)");

      // Apply theme mode (light/dark)
      const theme = settingsSync.getSetting(SETTINGS_KEYS.THEME, "");
      if (theme === "dark") {
        document.documentElement.classList.add("dark");
        logger.debug("[Theme] Applied dark theme from database");
      } else if (theme === "light") {
        document.documentElement.classList.remove("dark");
        logger.debug("[Theme] Applied light theme from database");
      }
      // If no theme saved, keep whatever index.html set (system preference or default)
      
      // Apply color scheme
      const colorScheme = settingsSync.getSetting(SETTINGS_KEYS.COLOR_SCHEME, "black");
      document.documentElement.setAttribute(
        "data-color-scheme", 
        COLOR_SCHEME_MAP[colorScheme] || "pure-black"
      );
      logger.debug("[Theme] Applied color scheme from database:", colorScheme);
      
      // Apply font settings
      const font = settingsSync.getSetting(SETTINGS_KEYS.FONT, "system");
      const chatFont = settingsSync.getSetting(SETTINGS_KEYS.CHAT_FONT, "default");
      const editorFont = settingsSync.getSetting(SETTINGS_KEYS.EDITOR_FONT, "default");
      const fontSize = settingsSync.getSetting(SETTINGS_KEYS.FONT_SIZE, "md");
      
      document.documentElement.dataset.font = font;
      document.documentElement.dataset.chatFont = chatFont;
      document.documentElement.dataset.editorFont = editorFont;
      applyRootFontSize(fontSize);
      
      logger.debug("[Theme] Applied font settings from database:", { font, chatFont, editorFont, fontSize });
      
      // Dispatch event for components that need to react to theme changes
      window.dispatchEvent(new CustomEvent("theme-applied"));
      window.dispatchEvent(new CustomEvent("appearance-updated"));
    };

    initTheme();
    
    return () => {
      cancelled = true;
    };
  }, []);
}
