import { useEffect, useState } from "react";
import { RotateCcw } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { ColorSchemeSelector } from "./ColorSchemeSelector";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { FONT_SIZE_MAP, applyRootFontSize, DEFAULT_FONT_SIZE } from "../../lib/rootFontSize";
import { applyThemeMode, parseThemeMode, type ThemeMode } from "../../lib/themeMode";
import { CHAT_THREAD_MODE_CHOICES, parseChatThreadMode, type ChatThreadModePreference } from "../Chat/thread-views/useChatThreadMode";
import { SettingsPageHeader } from "./SettingsPageHeader";
import {
  ChoiceGroup,
  SettingToggle,
  persistPref,
  readPref,
  settingSelectClass,
  waitForSettings,
  type Choice,
} from "./settingsControls";

import "./settings-range.css";

type FontSize = "xs" | "sm" | "md" | "lg" | "xl";
type ChatTimelineVariant = "compact" | "card" | "minimal";

const THEME_CHOICES: readonly Choice<ThemeMode>[] = [
  { value: "light", label: "Light" },
  { value: "dark", label: "Dark" },
  { value: "system", label: "System", description: "Follows your device" },
];

const CHAT_TIMELINE_VARIANTS: readonly Choice<ChatTimelineVariant>[] = [
  { value: "compact", label: "Compact", description: "Balanced spacing for everyday chats" },
  { value: "card", label: "Card", description: "Stronger message grouping and separation" },
  { value: "minimal", label: "Minimal", description: "Reduced chrome for dense timelines" },
];

const CHAT_THREAD_CHOICES: readonly Choice<ChatThreadModePreference>[] = CHAT_THREAD_MODE_CHOICES;

const FONT_SIZE_LABELS: Record<FontSize, string> = {
  xs: "Extra small",
  sm: "Small",
  md: "Medium",
  lg: "Large",
  xl: "Extra large",
};

const DEFAULT_COLOR_SCHEME = "black";

/**
 * Settings → Appearance: how the app looks — theme, colour scheme, fonts,
 * text size and chat density. How the chat and the editor BEHAVE (streaming,
 * spawn and tool-call display, the workflow viewer, the file browser, the code
 * editor) is Settings → Chat & editor.
 *
 * Every control applies at once. "Reset to defaults" in the header resets
 * exactly what this page shows, and nothing on any other page.
 */
export function AppearanceSettings() {
  const [theme, setTheme] = useState<ThemeMode>("system");
  const [font, setFont] = useState<string>("default");
  const [chatFont, setChatFont] = useState<string>("default");
  const [editorFont, setEditorFont] = useState<string>("default");
  // Whether the user wants a distinct monospace font for code, vs. letting the
  // editor follow the app font. Derived from a non-default persisted EDITOR_FONT.
  const [useCodeFont, setUseCodeFont] = useState<boolean>(false);
  const [fontSize, setFontSize] = useState<FontSize>(DEFAULT_FONT_SIZE as FontSize);
  const [chatTimelineVariant, setChatTimelineVariant] = useState<ChatTimelineVariant>("compact");
  const [chatThreadMode, setChatThreadModeState] = useState<ChatThreadModePreference>("auto");
  // Bumped by Reset so the colour-scheme picker re-reads the stored scheme.
  const [schemeKey, setSchemeKey] = useState(0);
  const [isLoaded, setIsLoaded] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      await waitForSettings();
      if (cancelled) return;
      setTheme(parseThemeMode(settingsSync.getSetting(SETTINGS_KEYS.THEME, "")));
      setFont(readPref(SETTINGS_KEYS.FONT, "default"));
      setChatFont(readPref(SETTINGS_KEYS.CHAT_FONT, "default"));
      const savedEditorFont = readPref(SETTINGS_KEYS.EDITOR_FONT, "default");
      setEditorFont(savedEditorFont);
      setUseCodeFont(savedEditorFont !== "default");
      setFontSize(readPref(SETTINGS_KEYS.FONT_SIZE, DEFAULT_FONT_SIZE) as FontSize);
      setChatTimelineVariant(readPref(SETTINGS_KEYS.CHAT_TIMELINE_VARIANT, "compact"));
      setChatThreadModeState(parseChatThreadMode(readPref(SETTINGS_KEYS.CHAT_THREAD_MODE, "auto")));
      setIsLoaded(true);
    })();

    // A theme change made elsewhere (the Electron menu's toggle, the phone).
    const handleThemeApplied = () => setTheme(parseThemeMode(settingsSync.getSetting(SETTINGS_KEYS.THEME, "")));
    window.addEventListener("theme-applied", handleThemeApplied);
    return () => {
      cancelled = true;
      window.removeEventListener("theme-applied", handleThemeApplied);
    };
  }, []);

  // Written by the handler, not an effect on `theme`: an unset theme reads as
  // "system", and an effect would persist that on every visit.
  const chooseTheme = (next: ThemeMode) => {
    setTheme(next);
    applyThemeMode(next);
    settingsSync.setSetting(SETTINGS_KEYS.THEME, next).catch(console.error);
    window.dispatchEvent(new CustomEvent("theme-applied"));
    window.dispatchEvent(new CustomEvent("appearance-updated"));
  };

  useEffect(() => {
    if (!isLoaded) return;
    document.documentElement.dataset.font = font;
    if (persistPref(SETTINGS_KEYS.FONT, font)) {
      window.dispatchEvent(new CustomEvent("font-changed"));
    }
  }, [font, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    document.documentElement.dataset.chatFont = chatFont;
    if (persistPref(SETTINGS_KEYS.CHAT_FONT, chatFont)) {
      window.dispatchEvent(new CustomEvent("font-changed"));
    }
  }, [chatFont, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    document.documentElement.dataset.editorFont = editorFont;
    persistPref(SETTINGS_KEYS.EDITOR_FONT, editorFont);
  }, [editorFont, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    applyRootFontSize(fontSize);
    if (persistPref(SETTINGS_KEYS.FONT_SIZE, fontSize)) {
      window.dispatchEvent(new CustomEvent("font-changed"));
    }
  }, [fontSize, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    if (persistPref(SETTINGS_KEYS.CHAT_TIMELINE_VARIANT, chatTimelineVariant)) {
      window.dispatchEvent(new CustomEvent("appearance-updated"));
    }
  }, [chatTimelineVariant, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    // Unset already means "auto"; don't write the default just for opening the page.
    if (chatThreadMode === "auto" && !settingsSync.getSetting(SETTINGS_KEYS.CHAT_THREAD_MODE, "")) return;
    if (persistPref(SETTINGS_KEYS.CHAT_THREAD_MODE, chatThreadMode)) {
      window.dispatchEvent(new CustomEvent("appearance-updated"));
    }
  }, [chatThreadMode, isLoaded]);

  const resetToDefaults = () => {
    chooseTheme("system");
    settingsSync.setSetting(SETTINGS_KEYS.COLOR_SCHEME, DEFAULT_COLOR_SCHEME).catch(console.error);
    setSchemeKey((key) => key + 1);
    setFont("default");
    setChatFont("default");
    setEditorFont("default");
    setUseCodeFont(false);
    setFontSize(DEFAULT_FONT_SIZE as FontSize);
    setChatTimelineVariant("compact");
    setChatThreadModeState("auto");
  };

  // Don't render until settings are loaded
  if (!isLoaded) {
    return null;
  }

  return (
    <div className="space-y-4">
      <div data-onboarding="appearance-settings">
        <SettingsPageHeader
          title="Appearance"
          description="Theme, colour scheme, fonts and density. Changes apply immediately and are remembered on this device."
          actions={
            <Button variant="outline" size="sm" onClick={resetToDefaults} leftIcon={<RotateCcw className="h-3.5 w-3.5" />}>
              Reset to defaults
            </Button>
          }
        />
      </div>

      <section aria-labelledby="appearance-theme" className="rounded-lg border border-border bg-card p-4 space-y-3">
        <h3 id="appearance-theme" className="text-sm font-semibold">
          Theme
        </h3>
        <ChoiceGroup label="Theme" value={theme} choices={THEME_CHOICES} onChange={chooseTheme} className="grid-cols-3" />
      </section>

      <div className="rounded-lg border border-border bg-card p-4">
        <ColorSchemeSelector key={schemeKey} />
      </div>

      <section aria-labelledby="appearance-fonts" className="rounded-lg border border-border bg-card p-4">
        <h3 id="appearance-fonts" className="mb-6 text-sm font-semibold">
          Fonts and size
        </h3>

        <div className="space-y-6">
          {/* App Font — drives the system font; chat input follows it. */}
          <div className="space-y-3">
            <label htmlFor="appearance-font" className="text-sm font-medium block">
              App font
            </label>
            <select
              id="appearance-font"
              // "geist" was its own option before Geist became the default;
              // a stored "geist" renders the same face, so show it as Default.
              value={font === "geist" ? "default" : font}
              onChange={(e) => setFont(e.target.value)}
              className={settingSelectClass}
            >
              <option value="default">Geist (Default)</option>
              <option value="inter">Inter</option>
              <option value="system">System Default</option>
              <option value="mono">JetBrains Mono</option>
              <option value="comic">Comic Sans (Fun)</option>
            </select>
          </div>

          {/* Separate monospace for code */}
          <div className="space-y-3">
            <SettingToggle
              label="Separate monospace font for code"
              description="Use a distinct font in the file editor instead of the app font."
              checked={useCodeFont}
              onChange={(checked) => {
                setUseCodeFont(checked);
                // ON: seed a real monospace choice; OFF: editor follows the app font.
                setEditorFont(checked ? "mono" : "default");
              }}
            />
            {useCodeFont && (
              <select
                id="editor-font"
                aria-label="Code font"
                value={editorFont === "default" ? "mono" : editorFont}
                onChange={(e) => setEditorFont(e.target.value)}
                className={settingSelectClass}
              >
                <option value="mono">JetBrains Mono</option>
                <option value="inherit">Inherit from System</option>
                <option value="system">System Default</option>
                <option value="inter">Inter (Modern)</option>
                <option value="geist">Geist Mono</option>
                <option value="comic">Comic Sans (Fun)</option>
              </select>
            )}
          </div>

          <div className="space-y-3">
            <label htmlFor="font-size" className="text-sm font-medium block">
              Text size
            </label>
            <select
              id="font-size"
              value={fontSize}
              onChange={(e) => setFontSize(e.target.value as FontSize)}
              className={settingSelectClass}
            >
              {Object.entries(FONT_SIZE_LABELS).map(([key, label]) => (
                <option key={key} value={key}>
                  {label} ({FONT_SIZE_MAP[key as FontSize]})
                </option>
              ))}
            </select>
          </div>

          <div className="space-y-3">
            <span id="appearance-density" className="text-sm font-medium block">
              Chat density
            </span>
            <ChoiceGroup
              label="Chat density"
              value={chatTimelineVariant}
              choices={CHAT_TIMELINE_VARIANTS}
              onChange={setChatTimelineVariant}
              className="sm:grid-cols-3"
            />
          </div>

          <div className="space-y-3">
            <span id="appearance-chat-thread" className="text-sm font-medium block">
              Chat thread
            </span>
            <ChoiceGroup
              label="Chat thread"
              value={chatThreadMode}
              choices={CHAT_THREAD_CHOICES}
              onChange={setChatThreadModeState}
              className="sm:grid-cols-3"
            />
          </div>
        </div>
      </section>
    </div>
  );
}
