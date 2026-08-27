/**
 * Mobile-native "Appearance" panel.
 *
 * Reads and writes the exact same `settingsSync` keys as desktop
 * `AppearanceSettings` (`THEME`, `FONT`, `EDITOR_FONT`, `FONT_SIZE`,
 * `CHAT_TIMELINE_VARIANT`, `WORKFLOW_VIEWER_DEFAULT_MODE`,
 * `SPAWN_DISPLAY_MODE` via `SpawnDisplaySettings`) plus `useUIStore`'s
 * `showHiddenFiles`, so a change here is the same persisted preference
 * desktop shows — only the control shape (rows/sheets vs. selects/grids)
 * differs.
 *
 * Font size: this shows the user's PREFERENCE STEP (xs/sm/md/lg/xl), never
 * a resolved pixel value. `FONT_SIZE_MAP` is imported only for its labels
 * (the desktop-scale px, purely informational) — the actual root size is
 * applied by `applyRootFontSize`, which resolves the step against
 * `MOBILE_FONT_SIZE_MAP` internally because it reads `window.location.pathname`
 * (see `rootFontSize.ts`). This file never reads `MOBILE_FONT_SIZE_MAP`
 * directly and never hardcodes a px value, so the picker always reflects the
 * step the user chose, matching desktop's stored value exactly.
 *
 * Omitted vs. desktop: the collapsed-by-default "Editor (advanced)" section
 * (Monaco display/behavior/diff settings, language servers, tool-call
 * display defaults). That section only matters once you open a file in the
 * Monaco editor, which mobile deliberately doesn't ship (see
 * `MobileFilePreview`'s module comment on `shouldPreloadMonaco`) — shipping
 * the settings for an editor mobile never renders would be dead-end config.
 */

import { useEffect, useState } from "react";
import { Moon, Sun } from "lucide-react";
import { ColorSchemeSelector } from "../Settings/ColorSchemeSelector";
import { useUIStore } from "../../store/uiStore";
import { settingsSync, SETTINGS_KEYS, type SettingKey } from "../../services/settingsSync";
import {
  getSpawnDisplayMode,
  setSpawnDisplayMode as saveSpawnDisplayMode,
  type SpawnDisplayMode,
} from "../Settings/SpawnDisplaySettings";
import { FONT_SIZE_MAP, applyRootFontSize, DEFAULT_FONT_SIZE } from "../../lib/rootFontSize";
import {
  MobileSegmentedRow,
  MobileSelectRow,
  MobileSettingsSectionTitle,
  MobileToggleRow,
} from "./MobileSettingsRow";

type FontSize = "xs" | "sm" | "md" | "lg" | "xl";
type ChatTimelineVariant = "compact" | "card" | "minimal";

const FONT_OPTIONS = [
  { value: "default", label: "Inter (Default)" },
  { value: "system", label: "System Default" },
  { value: "mono", label: "JetBrains Mono" },
  { value: "geist", label: "Geist" },
  { value: "comic", label: "Comic Sans (Fun)" },
];

const CODE_FONT_OPTIONS = [
  { value: "mono", label: "JetBrains Mono" },
  { value: "inherit", label: "Inherit from System" },
  { value: "system", label: "System Default" },
  { value: "inter", label: "Inter (Modern)" },
  { value: "geist", label: "Geist" },
  { value: "comic", label: "Comic Sans (Fun)" },
];

const FONT_SIZE_OPTIONS: Array<{ value: FontSize; label: string }> = [
  { value: "xs", label: "Extra Small" },
  { value: "sm", label: "Small" },
  { value: "md", label: "Medium" },
  { value: "lg", label: "Large" },
  { value: "xl", label: "Extra Large" },
];

const CHAT_TIMELINE_OPTIONS: Array<{ value: ChatTimelineVariant; label: string }> = [
  { value: "compact", label: "Compact" },
  { value: "card", label: "Card" },
  { value: "minimal", label: "Minimal" },
];

function readPref<T extends string>(key: string, fallback: T): T {
  return settingsSync.getSetting(key as any, fallback) as T;
}

/**
 * Persist a preference only when it differs from what is stored — the mobile
 * twin of the desktop panel's guard. Without it, opening this panel re-saves
 * every preference, and a stale read becomes the newest record, silently
 * reverting a user's choice. See settingsSync.setSettingIfChanged.
 */
function persistPref(key: SettingKey, value: string): boolean {
  if (settingsSync.getSetting(key, "") === value) return false;
  void settingsSync.setSettingIfChanged(key, value).catch(console.error);
  return true;
}

export function MobileAppearancePanel() {
  const showHiddenFiles = useUIStore((state) => state.showHiddenFiles);
  const setShowHiddenFiles = useUIStore((state) => state.setShowHiddenFiles);

  const [theme, setTheme] = useState<"light" | "dark">("light");
  const [font, setFont] = useState<string>("default");
  const [editorFont, setEditorFont] = useState<string>("default");
  const [useCodeFont, setUseCodeFont] = useState<boolean>(false);
  const [fontSize, setFontSize] = useState<FontSize>(DEFAULT_FONT_SIZE as FontSize);
  const [chatTimelineVariant, setChatTimelineVariant] = useState<ChatTimelineVariant>("compact");
  const [workflowViewerDefaultMode, setWorkflowViewerDefaultMode] = useState<"inline" | "side">("side");
  const [spawnDisplayMode, setSpawnDisplayMode] = useState<SpawnDisplayMode>("preview");
  const [isLoaded, setIsLoaded] = useState(false);

  useEffect(() => {
    const loadSettings = async () => {
      let attempts = 0;
      while (!settingsSync.isInitialized() && attempts < 50) {
        await new Promise((resolve) => setTimeout(resolve, 100));
        attempts++;
      }

      const savedTheme = settingsSync.getSetting(SETTINGS_KEYS.THEME, "");
      const themeValue =
        savedTheme === "dark"
          ? "dark"
          : savedTheme === "light"
            ? "light"
            : document.documentElement.classList.contains("dark")
              ? "dark"
              : "light";

      setTheme(themeValue);
      setFont(readPref(SETTINGS_KEYS.FONT, "default"));
      const savedEditorFont = readPref(SETTINGS_KEYS.EDITOR_FONT, "default");
      setEditorFont(savedEditorFont);
      setUseCodeFont(savedEditorFont !== "default");
      setFontSize(readPref(SETTINGS_KEYS.FONT_SIZE, DEFAULT_FONT_SIZE) as FontSize);
      setChatTimelineVariant(readPref(SETTINGS_KEYS.CHAT_TIMELINE_VARIANT, "compact"));
      setWorkflowViewerDefaultMode(readPref(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, "side"));
      setSpawnDisplayMode(getSpawnDisplayMode());
      setIsLoaded(true);
    };

    loadSettings();

    const handleThemeApplied = () => {
      const savedTheme = settingsSync.getSetting(SETTINGS_KEYS.THEME, "");
      if (savedTheme === "dark" || savedTheme === "light") {
        setTheme(savedTheme as "light" | "dark");
      }
    };
    window.addEventListener("theme-applied", handleThemeApplied);
    return () => window.removeEventListener("theme-applied", handleThemeApplied);
  }, []);

  useEffect(() => {
    if (!isLoaded) return;
    const currentHasDark = document.documentElement.classList.contains("dark");
    const shouldHaveDark = theme === "dark";
    if (currentHasDark !== shouldHaveDark) {
      document.documentElement.classList.toggle("dark", shouldHaveDark);
      settingsSync.setSetting(SETTINGS_KEYS.THEME, theme).catch(console.error);
      window.dispatchEvent(new CustomEvent("theme-applied"));
      window.dispatchEvent(new CustomEvent("appearance-updated"));
    }
  }, [theme, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    document.documentElement.dataset.font = font;
    if (persistPref(SETTINGS_KEYS.FONT, font)) {
      window.dispatchEvent(new CustomEvent("font-changed"));
    }
  }, [font, isLoaded]);

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
    if (persistPref(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, workflowViewerDefaultMode)) {
      window.dispatchEvent(new CustomEvent("appearance-updated"));
    }
  }, [workflowViewerDefaultMode, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    if (persistPref(SETTINGS_KEYS.CHAT_TIMELINE_VARIANT, chatTimelineVariant)) {
      window.dispatchEvent(new CustomEvent("appearance-updated"));
    }
  }, [chatTimelineVariant, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    saveSpawnDisplayMode(spawnDisplayMode).catch(console.error);
    window.dispatchEvent(new CustomEvent("appearance-updated"));
  }, [spawnDisplayMode, isLoaded]);

  if (!isLoaded) {
    return (
      <div className="p-4">
        <p className="text-sm text-muted-foreground">Loading…</p>
      </div>
    );
  }

  return (
    <div className="divide-y divide-border">
      <MobileSegmentedRow
        label="Theme"
        value={theme}
        onChange={(v) => setTheme(v as "light" | "dark")}
        options={[
          { value: "light", label: "Light", icon: <Sun className="h-4 w-4" /> },
          { value: "dark", label: "Dark", icon: <Moon className="h-4 w-4" /> },
        ]}
      />

      <div className="p-4">
        <ColorSchemeSelector />
      </div>

      <MobileSettingsSectionTitle title="File browser" />
      <MobileToggleRow
        label="Show hidden files"
        description="Display files and folders starting with . (e.g., .git, .env)"
        checked={showHiddenFiles}
        onChange={setShowHiddenFiles}
      />

      <div className="flex items-center justify-between px-4 pt-4 pb-1">
        <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Fonts
        </h3>
        <button
          type="button"
          onClick={() => {
            setFont("default");
            setUseCodeFont(false);
            setEditorFont("default");
            setFontSize("md");
            setChatTimelineVariant("compact");
            setWorkflowViewerDefaultMode("side");
          }}
          className="flex min-h-[44px] items-center text-xs text-muted-foreground underline active:text-foreground"
        >
          Reset to defaults
        </button>
      </div>
      <MobileSelectRow
        label="App font"
        value={font}
        options={FONT_OPTIONS}
        onChange={setFont}
      />
      <MobileToggleRow
        label="Separate monospace font for code"
        description="Use a distinct font in the file preview instead of the app font."
        checked={useCodeFont}
        onChange={(checked) => {
          setUseCodeFont(checked);
          setEditorFont(checked ? "mono" : "default");
        }}
      />
      {useCodeFont && (
        <MobileSelectRow
          label="Code font"
          value={editorFont === "default" ? "mono" : editorFont}
          options={CODE_FONT_OPTIONS}
          onChange={setEditorFont}
        />
      )}
      <MobileSelectRow
        label="Font size"
        value={fontSize}
        options={FONT_SIZE_OPTIONS.map((o) => ({
          value: o.value,
          label: o.label,
          description: FONT_SIZE_MAP[o.value],
        }))}
        onChange={(v) => setFontSize(v as FontSize)}
      />

      <MobileSettingsSectionTitle
        title="Chat timeline density"
        description="How messages are spaced and grouped in the timeline."
      />
      <MobileSegmentedRow
        value={chatTimelineVariant}
        onChange={(v) => setChatTimelineVariant(v as ChatTimelineVariant)}
        options={CHAT_TIMELINE_OPTIONS}
      />

      <MobileSettingsSectionTitle
        title="Spawn display"
        description="How spawned sub-agent threads appear in the timeline."
      />
      <MobileSegmentedRow
        value={spawnDisplayMode}
        onChange={(v) => setSpawnDisplayMode(v as SpawnDisplayMode)}
        options={[
          { value: "inline", label: "Full Inline" },
          { value: "preview", label: "Preview Window" },
        ]}
      />

      <MobileSettingsSectionTitle
        title="Workflow viewer"
        description="Default view mode for the workflow graph."
      />
      <MobileSegmentedRow
        value={workflowViewerDefaultMode}
        onChange={(v) => setWorkflowViewerDefaultMode(v as "inline" | "side")}
        options={[
          { value: "side", label: "Side Panel" },
          { value: "inline", label: "Inline" },
        ]}
      />

      <p className="p-4 text-xs text-muted-foreground">
        Code editor, language server, and tool-call display settings aren&apos;t
        available here. Manage those on desktop.
      </p>
    </div>
  );
}
