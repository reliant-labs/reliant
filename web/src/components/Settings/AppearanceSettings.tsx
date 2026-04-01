import { useEffect, useState } from "react";
import { cn } from "../../lib/utils";
import { ColorSchemeSelector } from "./ColorSchemeSelector";
import { useEditorStore } from "../../store/editorStore";
import { useUIStore } from "../../store/uiStore";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { Toggle } from "../ui/Toggle";
import { LanguageServerSettingsCompact } from "./LanguageServerSettings";
import { ToolCallSettingsCompact } from "./ToolCallSettings";

import "./settings-range.css";

type FontSize = "xs" | "sm" | "md" | "lg" | "xl";

const FONT_SIZE_MAP: Record<FontSize, string> = {
  xs: "12px",
  sm: "13px",
  md: "14px",
  lg: "15px",
  xl: "16px",
};

const FONT_SIZE_LABELS: Record<FontSize, string> = {
  xs: "Extra Small",
  sm: "Small",
  md: "Medium",
  lg: "Large",
  xl: "Extra Large",
};

function readPref<T extends string>(key: string, fallback: T): T {
  return settingsSync.getSetting(key as any, fallback) as T;
}

// Note: Using shared Toggle component from ui/

function SettingToggle({
  label,
  description,
  checked,
  onChange,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: (checked: boolean) => void;
}) {
  return (
    <div className="flex items-center justify-between">
      <div>
        <label className="text-sm font-medium">{label}</label>
        <p className="text-xs text-muted-foreground">{description}</p>
      </div>
      <Toggle checked={checked} onChange={onChange} label={label} />
    </div>
  );
}

function SettingRange({
  label,
  value,
  min,
  max,
  step = 1,
  onChange,
}: {
  label: string;
  value: number;
  min: number;
  max: number;
  step?: number;
  onChange: (value: number) => void;
}) {
  return (
    <div className="space-y-2">
      <label className="text-sm font-medium">{label}</label>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(parseInt(e.target.value))}
        className="w-full"
      />
    </div>
  );
}

function SettingSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (value: string) => void;
}) {
  return (
    <div className="space-y-3">
      <label className="text-sm font-medium block">{label}</label>
      <select
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="block w-full px-4 py-3 bg-card border-2 border-border text-foreground font-mono rounded-lg text-sm transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-primary/60 focus:border-primary hover:border-primary/40 elevation-1"
      >
        {options.map((option) => (
          <option key={option.value} value={option.value}>
            {option.label}
          </option>
        ))}
      </select>
    </div>
  );
}

export function AppearanceSettings() {
  const showHiddenFiles = useUIStore((state) => state.showHiddenFiles);
  const setShowHiddenFiles = useUIStore((state) => state.setShowHiddenFiles);

  const [theme, setTheme] = useState<'light' | 'dark'>('light');
  const [font, setFont] = useState<string>("system");
  const [chatFont, setChatFont] = useState<string>("default");
  const [editorFont, setEditorFont] = useState<string>("default");
  const [fontSize, setFontSize] = useState<FontSize>("md");
  const [workflowViewerDefaultMode, setWorkflowViewerDefaultMode] = useState<'inline' | 'side'>('side');
  const [isLoaded, setIsLoaded] = useState(false);

  // Load settings from settingsSync on mount (wait for initialization)
  useEffect(() => {
    const loadSettings = async () => {
      // Wait for settingsSync to be initialized
      let attempts = 0;
      while (!settingsSync.isInitialized() && attempts < 50) {
        await new Promise(resolve => setTimeout(resolve, 100));
        attempts++;
      }
      
      // Now read settings from settingsSync (which has loaded from database)
      const savedTheme = settingsSync.getSetting(SETTINGS_KEYS.THEME, "");
      const themeValue = savedTheme === "dark" ? "dark" : savedTheme === "light" ? "light" : 
        (document.documentElement.classList.contains('dark') ? 'dark' : 'light');
      
      setTheme(themeValue);
      setFont(readPref(SETTINGS_KEYS.FONT, "system"));
      setChatFont(readPref(SETTINGS_KEYS.CHAT_FONT, "default"));
      setEditorFont(readPref(SETTINGS_KEYS.EDITOR_FONT, "default"));
      setFontSize(readPref(SETTINGS_KEYS.FONT_SIZE, "md"));
      setWorkflowViewerDefaultMode(readPref(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, "side"));
      setIsLoaded(true);
    };
    
    loadSettings();

    // Also listen for theme updates from useThemeInitialization
    const handleThemeApplied = () => {
      const savedTheme = settingsSync.getSetting(SETTINGS_KEYS.THEME, "");
      if (savedTheme === "dark" || savedTheme === "light") {
        setTheme(savedTheme as 'light' | 'dark');
      }
    };

    window.addEventListener('theme-applied', handleThemeApplied);
    return () => {
      window.removeEventListener('theme-applied', handleThemeApplied);
    };
  }, []);

  // Handle theme changes (only after settings are loaded)
  // Only apply if the theme actually changed (don't re-apply on initial load if already applied)
  useEffect(() => {
    if (!isLoaded) return;
    
    // Check if theme is already correctly applied to avoid unnecessary DOM updates
    const currentHasDark = document.documentElement.classList.contains('dark');
    const shouldHaveDark = theme === 'dark';
    
    if (currentHasDark !== shouldHaveDark) {
      if (shouldHaveDark) {
        document.documentElement.classList.add('dark');
      } else {
        document.documentElement.classList.remove('dark');
      }
    }
    
    // Only sync to database if this is a user-initiated change, not initial load
    // We check if the current DOM state matches what we want to set
    // If it already matches, this is likely initial load and settings were already applied by Root.tsx
    const needsSync = currentHasDark !== shouldHaveDark;
    if (needsSync) {
      settingsSync.setSetting(SETTINGS_KEYS.THEME, theme).catch(console.error);
      // Dispatch event for components that need to react to theme changes
      window.dispatchEvent(new CustomEvent('theme-applied'));
      window.dispatchEvent(new CustomEvent('appearance-updated'));
    }
  }, [theme, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    document.documentElement.dataset.font = font;
    // Sync to database
    settingsSync.setSetting(SETTINGS_KEYS.FONT, font).catch(console.error);
    // Force re-render by dispatching a custom event
    window.dispatchEvent(new CustomEvent("font-changed"));
  }, [font, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    document.documentElement.dataset.chatFont = chatFont;
    // Sync to database
    settingsSync.setSetting(SETTINGS_KEYS.CHAT_FONT, chatFont).catch(console.error);
    // Force re-render by dispatching a custom event
    window.dispatchEvent(new CustomEvent("font-changed"));
  }, [chatFont, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    document.documentElement.dataset.editorFont = editorFont;
    // Sync to database
    settingsSync.setSetting(SETTINGS_KEYS.EDITOR_FONT, editorFont).catch(console.error);
  }, [editorFont, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    document.documentElement.style.fontSize = FONT_SIZE_MAP[fontSize];
    // Sync to database
    settingsSync.setSetting(SETTINGS_KEYS.FONT_SIZE, fontSize).catch(console.error);
    // Force re-render by dispatching a custom event
    window.dispatchEvent(new CustomEvent("font-changed"));
  }, [fontSize, isLoaded]);

  useEffect(() => {
    if (!isLoaded) return;
    // Sync workflow viewer default mode to database
    // Use .catch() to prevent errors from breaking the app
    settingsSync.setSetting(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, workflowViewerDefaultMode)
      .catch((error) => {
        // Log but don't throw - settings sync failures shouldn't break the app
        console.error('[AppearanceSettings] Failed to save workflow viewer default mode:', error);
      });
    // Dispatch event for components that need to react to this change
    window.dispatchEvent(new CustomEvent('appearance-updated'));
  }, [workflowViewerDefaultMode, isLoaded]);


  // Don't render until settings are loaded
  if (!isLoaded) {
    return null;
  }

  return (
    <div className="space-y-8">
      <div data-onboarding="appearance-settings">
        <h2 className="text-lg font-semibold">Appearance</h2>
        <p className="text-xs text-muted-foreground">
          Customize the appearance of the application.
        </p>
      </div>

      {/* Theme Toggle */}
      <div className="space-y-4">
        <h3 className="text-sm font-semibold">Theme</h3>
        <div className="flex items-center gap-4 p-4 bg-card border-2 border-border rounded-lg">
          <button
            onClick={() => setTheme('light')}
            className={cn(
              "flex-1 px-4 py-3 rounded-md font-medium text-sm transition-all duration-200 border-2",
              theme === 'light'
                ? "border-primary text-foreground shadow-md"
                : "border-border text-muted-foreground hover:bg-muted/80"
            )}
            style={theme === 'light' ? {
              backgroundColor: 'hsl(var(--primary) / 0.1)',
              borderColor: 'hsl(var(--primary))',
              color: 'hsl(var(--foreground))'
            } : undefined}
          >
            Light
          </button>
          <button
            onClick={() => setTheme('dark')}
            className={cn(
              "flex-1 px-4 py-3 rounded-md font-medium text-sm transition-all duration-200 border-2",
              theme === 'dark'
                ? "border-primary text-foreground shadow-md"
                : "border-border text-muted-foreground hover:bg-muted/80"
            )}
            style={theme === 'dark' ? {
              backgroundColor: 'hsl(var(--primary) / 0.1)',
              borderColor: 'hsl(var(--primary))',
              color: 'hsl(var(--foreground))'
            } : undefined}
          >
            Dark
          </button>
        </div>
      </div>

      {/* Color Scheme Selector */}
      <ColorSchemeSelector />

      {/* File Browser Settings */}
      <div className="border-t border-border/40 pt-6 space-y-4">
        <div>
          <h3 className="text-sm font-semibold">File Browser</h3>
          <p className="text-xs text-muted-foreground mt-1">
            Configure file browser display options
          </p>
        </div>

        <div className="space-y-4">
          <SettingToggle
            label="Show Hidden Files"
            description="Display files and folders starting with . (e.g., .git, .env)"
            checked={showHiddenFiles}
            onChange={setShowHiddenFiles}
          />
        </div>
      </div>

      <div className="border-t border-border/40 pt-6">
        <div className="flex items-center justify-between mb-6">
          <h3 className="text-sm font-semibold">Font Settings</h3>
          <button
            onClick={() => {
              setFont("system");
              setChatFont("default");
              setEditorFont("default");
              setFontSize("md");
              setWorkflowViewerDefaultMode("side");
            }}
            className="text-xs text-muted-foreground hover:text-foreground underline"
          >
            Reset to defaults
          </button>
        </div>

        <div className="space-y-6">
          {/* System Font */}
          <div className="space-y-3">
            <label
              htmlFor="appearance-font"
              className="text-sm font-medium block"
            >
              System Font
            </label>
            <select
              id="appearance-font"
              value={font}
              onChange={(e) => setFont(e.target.value)}
              className="block w-full px-4 py-3 bg-card border-2 border-border text-foreground font-mono rounded-lg text-sm transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-primary/60 focus:border-primary hover:border-primary/40 elevation-1"
            >
              <option value="system">System Default (Default)</option>
              <option value="inter">Inter (Modern)</option>
              <option value="mono">JetBrains Mono</option>
              <option value="geist">Geist</option>
              <option value="comic">Comic Sans (Fun)</option>
            </select>
          </div>

          {/* Chat Input Font */}
          <div className="space-y-3">
            <label htmlFor="chat-font" className="text-sm font-medium block">
              Chat Input Font
            </label>
            <select
              id="chat-font"
              value={chatFont}
              onChange={(e) => setChatFont(e.target.value)}
              className="block w-full px-4 py-3 bg-card border-2 border-border text-foreground font-mono rounded-lg text-sm transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-primary/60 focus:border-primary hover:border-primary/40 elevation-1"
            >
              <option value="default">Monospace (Default)</option>
              <option value="mono">JetBrains Mono</option>
              <option value="inherit">Inherit from System</option>
              <option value="system">System Default</option>
              <option value="inter">Inter (Modern)</option>
              <option value="geist">Geist</option>
              <option value="comic">Comic Sans (Fun)</option>
            </select>
          </div>

          {/* Editor Font */}
          <div className="space-y-3">
            <label htmlFor="editor-font" className="text-sm font-medium block">
              File Editor Font
            </label>
            <select
              id="editor-font"
              value={editorFont}
              onChange={(e) => setEditorFont(e.target.value)}
              className="block w-full px-4 py-3 bg-card border-2 border-border text-foreground font-mono rounded-lg text-sm transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-primary/60 focus:border-primary hover:border-primary/40 elevation-1"
            >
              <option value="default">Monospace (Default)</option>
              <option value="mono">JetBrains Mono</option>
              <option value="inherit">Inherit from System</option>
              <option value="system">System Default</option>
              <option value="inter">Inter (Modern)</option>
              <option value="geist">Geist</option>
              <option value="comic">Comic Sans (Fun)</option>
            </select>
          </div>

          {/* Font Size */}
          <div className="space-y-3">
            <label htmlFor="font-size" className="text-sm font-medium block">
              Font Size
            </label>
            <select
              id="font-size"
              value={fontSize}
              onChange={(e) => setFontSize(e.target.value as FontSize)}
              className="block w-full px-4 py-3 bg-card border-2 border-border text-foreground font-mono rounded-lg text-sm transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-primary/60 focus:border-primary hover:border-primary/40 elevation-1"
            >
              {Object.entries(FONT_SIZE_LABELS).map(([key, label]) => (
                <option key={key} value={key}>
                  {label} ({FONT_SIZE_MAP[key as FontSize]})
                </option>
              ))}
            </select>
          </div>
        </div>
      </div>

      {/* Monaco Editor Settings */}
      <MonacoEditorSettings />

      {/* Tool Call Display Settings */}
      <div className="border-t border-border/40 pt-6 space-y-4">
        <div>
          <h3 className="text-sm font-semibold">Tool Call Display</h3>
          <p className="text-xs text-muted-foreground mt-1">
            Configure default collapse/expand behavior for tool calls in chat
          </p>
        </div>
        <ToolCallSettingsCompact />
      </div>

      {/* Workflow Viewer Settings */}
      <div className="border-t border-border/40 pt-6 space-y-4">
        <div>
          <h3 className="text-sm font-semibold">Workflow Viewer</h3>
          <p className="text-xs text-muted-foreground mt-1">
            Configure default view mode for workflow graph visualization
          </p>
        </div>
        <div className="space-y-3">
          <label className="text-sm font-medium block">Default View Mode</label>
          <select
            value={workflowViewerDefaultMode}
            onChange={(e) => setWorkflowViewerDefaultMode(e.target.value as 'inline' | 'side')}
            className="block w-full px-4 py-3 bg-card border-2 border-border text-foreground font-mono rounded-lg text-sm transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-primary/60 focus:border-primary hover:border-primary/40 elevation-1"
          >
            <option value="side">Side Panel (beside chat)</option>
            <option value="inline">Inline (above chat)</option>
          </select>
          <p className="text-xs text-muted-foreground">
            {workflowViewerDefaultMode === 'side' 
              ? 'Workflow graphs will open in a side panel by default. You can toggle between modes using the button in the viewer.'
              : 'Workflow graphs will open inline above the chat by default. You can toggle between modes using the button in the viewer.'}
          </p>
        </div>
      </div>

    </div>
  );
}

// Monaco Editor Settings Component
function MonacoEditorSettings() {
  const { settings, updateSettings, resetSettings } = useEditorStore();

  return (
    <div className="border-t border-border/40 pt-6 space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-semibold">Code Editor</h3>
          <p className="text-xs text-muted-foreground mt-1">
            Configure Monaco Editor (VS Code) settings
          </p>
        </div>
        <button
          onClick={resetSettings}
          className="text-xs text-muted-foreground hover:text-foreground underline"
        >
          Reset to defaults
        </button>
      </div>

      {/* Display Settings */}
      <div className="space-y-4">
        <h4 className="text-xs font-semibold text-foreground">
          Display
        </h4>

        <div className="space-y-4">
          <SettingToggle
            label="Minimap"
            description="Show code overview"
            checked={settings.minimap}
            onChange={(checked) => updateSettings({ minimap: checked })}
          />
          <SettingToggle
            label="Line Numbers"
            description="Show line numbers"
            checked={settings.lineNumbers}
            onChange={(checked) => updateSettings({ lineNumbers: checked })}
          />
          <SettingToggle
            label="Word Wrap"
            description="Wrap long lines"
            checked={settings.wordWrap}
            onChange={(checked) => updateSettings({ wordWrap: checked })}
          />
          <SettingToggle
            label="Show Whitespace"
            description="Render spaces/tabs"
            checked={settings.renderWhitespace}
            onChange={(checked) =>
              updateSettings({ renderWhitespace: checked })
            }
          />
        </div>
      </div>

      {/* Behavior Settings */}
      <div className="space-y-4">
        <h4 className="text-xs font-semibold text-foreground">
          Behavior
        </h4>

        <div className="space-y-4">
          <SettingRange
            label={`Font Size: ${settings.fontSize}px`}
            value={settings.fontSize}
            min={10}
            max={20}
            onChange={(value) => updateSettings({ fontSize: value })}
          />
          <SettingRange
            label={`Tab Size: ${settings.tabSize} spaces`}
            value={settings.tabSize}
            min={2}
            max={8}
            step={2}
            onChange={(value) => updateSettings({ tabSize: value })}
          />
          <SettingToggle
            label="Smooth Cursor"
            description="Smooth cursor movement"
            checked={settings.cursorSmoothCaretAnimation}
            onChange={(checked) =>
              updateSettings({ cursorSmoothCaretAnimation: checked })
            }
          />
          <SettingToggle
            label="Auto Save"
            description="Automatically save changes after typing"
            checked={settings.autoSave}
            onChange={(checked) => updateSettings({ autoSave: checked })}
          />

          {/* Auto Save Delay - Smooth transition */}
          <div
            className={cn(
              "overflow-hidden transition-all duration-300 ease-in-out",
              settings.autoSave ? "max-h-24 opacity-100" : "max-h-0 opacity-0"
            )}
          >
            <div className="space-y-2 pt-1">
              <label className="text-sm font-medium">
                Auto Save Delay: {settings.autoSaveDelay}ms
              </label>
              <input
                type="range"
                min="500"
                max="3000"
                step="100"
                value={settings.autoSaveDelay}
                onChange={(e) =>
                  updateSettings({ autoSaveDelay: parseInt(e.target.value) })
                }
                className="w-full"
              />
              <p className="text-xs text-muted-foreground">
                Time to wait after typing stops before saving
              </p>
            </div>
          </div>
        </div>
      </div>

      {/* Advanced Settings */}
      <div className="space-y-4">
        <h4 className="text-xs font-semibold text-foreground">
          Advanced
        </h4>

        <div className="space-y-4">
          <SettingToggle
            label="Rainbow Brackets"
            description="Colorize bracket pairs"
            checked={settings.bracketPairColorization}
            onChange={(checked) =>
              updateSettings({ bracketPairColorization: checked })
            }
          />
          <SettingToggle
            label="Indentation Guides"
            description="Show indent lines"
            checked={settings.guides}
            onChange={(checked) => updateSettings({ guides: checked })}
          />
          <SettingToggle
            label="Quick Suggestions"
            description="Auto-complete"
            checked={settings.quickSuggestions}
            onChange={(checked) =>
              updateSettings({ quickSuggestions: checked })
            }
          />

          <SettingSelect
            label="Cursor Style"
            value={settings.cursorBlinking}
            options={[
              { value: "blink", label: "Blink" },
              { value: "smooth", label: "Smooth" },
              { value: "phase", label: "Phase" },
              { value: "expand", label: "Expand" },
              { value: "solid", label: "Solid" },
            ]}
            onChange={(value) =>
              updateSettings({
                cursorBlinking: value as
                  | "blink"
                  | "smooth"
                  | "phase"
                  | "expand"
                  | "solid",
              })
            }
          />

          <SettingSelect
            label="Line Highlight"
            value={settings.renderLineHighlight}
            options={[
              { value: "none", label: "None" },
              { value: "gutter", label: "Gutter" },
              { value: "line", label: "Line" },
              { value: "all", label: "All" },
            ]}
            onChange={(value) =>
              updateSettings({
                renderLineHighlight: value as
                  | "none"
                  | "gutter"
                  | "line"
                  | "all",
              })
            }
          />
        </div>
      </div>

      {/* Diff Viewer Settings */}
      <div className="space-y-4">
        <h4 className="text-xs font-semibold text-foreground">Diff Viewer</h4>

        <div className="space-y-4">
          {/* Diff View Mode */}
          <div className="space-y-3">
            <label className="text-sm font-medium block">View Mode</label>
            <select
              value={settings.diffSideBySide ? "side-by-side" : "inline"}
              onChange={(e) => updateSettings({ diffSideBySide: e.target.value === "side-by-side" })}
              className="block w-full px-4 py-3 bg-card border-2 border-border text-foreground font-mono rounded-lg text-sm transition-all duration-200 focus:outline-none focus:ring-2 focus:ring-primary/60 focus:border-primary hover:border-primary/40 elevation-1"
            >
              <option value="side-by-side">Side-by-side</option>
              <option value="inline">Inline</option>
            </select>
            <p className="text-xs text-muted-foreground">
              {settings.diffSideBySide ? "Two panels (best for wide screens)" : "Single view with +/- indicators"}
            </p>
          </div>

          {/* Collapse Unchanged */}
          <SettingToggle
            label="Enable Collapse Controls"
            description="Show collapse buttons for unchanged regions"
            checked={settings.diffHideUnchanged}
            onChange={(checked) => updateSettings({ diffHideUnchanged: checked })}
          />
        </div>
      </div>

      {/* Language Servers */}
      <div className="space-y-4">
        <h4 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">
          Language Servers
        </h4>
        <p className="text-xs text-muted-foreground">
          Enable language servers for Go to Definition, hover info, and autocomplete in non-TypeScript languages.
        </p>
        <LanguageServerSettingsCompact />
      </div>
    </div>
  );
}
