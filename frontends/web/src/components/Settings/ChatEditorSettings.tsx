import { useEffect, useState } from "react";
import { RotateCcw } from "lucide-react";
import Button from "@/components/forge-ui/button";
import { cn } from "../../lib/utils";
import { useEditorStore } from "../../store/editorStore";
import { useUIStore } from "../../store/uiStore";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import ToggleSwitch from "../forge-ui/toggle_switch";
import { DEFAULT_TOOL_COLLAPSE_SETTINGS, TOOL_COLLAPSE_SETTINGS_EVENT, ToolCallSettingsCompact } from "./ToolCallSettings";
import { getSpawnDisplayMode, setSpawnDisplayMode as saveSpawnDisplayMode, type SpawnDisplayMode } from "./SpawnDisplaySettings";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { usePreferences, useUpdatePreferences } from "../../hooks/settings-queries";
import {
  ChoiceGroup,
  SettingRange,
  SettingSelect,
  SettingToggle,
  persistPref,
  readPref,
  waitForSettings,
  type Choice,
} from "./settingsControls";

import "./settings-range.css";

type WorkflowViewerMode = "inline" | "side";

const SPAWN_DISPLAY_CHOICES: readonly Choice<SpawnDisplayMode>[] = [
  { value: "inline", label: "Full inline", description: "Every message of a sub-agent's thread, in the timeline" },
  { value: "preview", label: "Preview window", description: "A compact preview of its result in the tool call" },
];

/**
 * Settings → Chat & editor: how the chat transcript, the file browser and the
 * code editor behave. Split out of Appearance, which kept only what the app
 * looks like (theme, scheme, fonts, density); before, a user looking for
 * "response streaming" or "minimap" had to guess they were under Appearance →
 * Editor (advanced).
 *
 * "Reset to defaults" in the header resets every control on this page.
 */
export function ChatEditorSettings() {
  const showHiddenFiles = useUIStore((state) => state.showHiddenFiles);
  const setShowHiddenFiles = useUIStore((state) => state.setShowHiddenFiles);
  const resetEditorSettings = useEditorStore((state) => state.resetSettings);
  const { data: preferences } = usePreferences();
  const updatePreferences = useUpdatePreferences();

  const [workflowViewerDefaultMode, setWorkflowViewerDefaultMode] = useState<WorkflowViewerMode>("side");
  const [spawnDisplayMode, setSpawnDisplayMode] = useState<SpawnDisplayMode>("preview");
  // Bumped by Reset so the tool-call panel re-reads its stored defaults.
  const [toolCallKey, setToolCallKey] = useState(0);
  const [isLoaded, setIsLoaded] = useState(false);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      await waitForSettings();
      if (cancelled) return;
      setWorkflowViewerDefaultMode(readPref(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, "side"));
      setSpawnDisplayMode(getSpawnDisplayMode());
      setIsLoaded(true);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!isLoaded) return;
    if (persistPref(SETTINGS_KEYS.WORKFLOW_VIEWER_DEFAULT_MODE, workflowViewerDefaultMode)) {
      window.dispatchEvent(new CustomEvent("appearance-updated"));
    }
  }, [workflowViewerDefaultMode, isLoaded]);

  const chooseSpawnDisplay = (mode: SpawnDisplayMode) => {
    setSpawnDisplayMode(mode);
    saveSpawnDisplayMode(mode).catch(console.error);
    window.dispatchEvent(new CustomEvent("appearance-updated"));
  };

  const resetToDefaults = () => {
    if (!preferences.streamingEnabled) {
      void updatePreferences.mutateAsync({ streamingEnabled: true }).catch(() => {});
    }
    chooseSpawnDisplay("preview");
    setWorkflowViewerDefaultMode("side");
    setShowHiddenFiles(false);
    settingsSync.setJSONSetting(SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS, DEFAULT_TOOL_COLLAPSE_SETTINGS).catch(console.error);
    window.dispatchEvent(new CustomEvent(TOOL_COLLAPSE_SETTINGS_EVENT));
    setToolCallKey((key) => key + 1);
    resetEditorSettings();
  };

  if (!isLoaded) {
    return null;
  }

  return (
    <div className="space-y-4">
      <SettingsPageHeader
        title="Chat & editor"
        description="How replies, tool calls and sub-agents show in a chat, and how the file browser and code editor behave."
        actions={
          <Button variant="outline" size="sm" onClick={resetToDefaults} leftIcon={<RotateCcw className="h-3.5 w-3.5" />}>
            Reset to defaults
          </Button>
        }
      />

      <ResponseStreamingSetting />

      <section aria-labelledby="chat-editor-tool-calls" className="rounded-lg border border-border bg-card p-4 space-y-4">
        <div>
          <h3 id="chat-editor-tool-calls" className="text-sm font-semibold">
            Tool calls
          </h3>
          <p className="text-xs text-muted-foreground mt-1">Whether each kind of tool call starts collapsed or expanded in a chat.</p>
        </div>
        <ToolCallSettingsCompact key={toolCallKey} />
      </section>

      <section aria-labelledby="chat-editor-spawn" className="rounded-lg border border-border bg-card p-4 space-y-3">
        <div>
          <h3 id="chat-editor-spawn" className="text-sm font-semibold">
            Sub-agents
          </h3>
          <p className="text-xs text-muted-foreground mt-1">How a sub-agent's conversation appears in the timeline.</p>
        </div>
        <ChoiceGroup
          label="Sub-agent display"
          value={spawnDisplayMode}
          choices={SPAWN_DISPLAY_CHOICES}
          onChange={chooseSpawnDisplay}
          className="sm:grid-cols-2"
        />
      </section>

      <section aria-labelledby="chat-editor-workflow-viewer" className="rounded-lg border border-border bg-card p-4 space-y-4">
        <h3 id="chat-editor-workflow-viewer" className="text-sm font-semibold">
          Workflow viewer
        </h3>
        <SettingSelect
          label="Default view mode"
          value={workflowViewerDefaultMode}
          options={[
            { value: "side", label: "Side panel (beside chat)" },
            { value: "inline", label: "Inline (above chat)" },
          ]}
          onChange={(value) => setWorkflowViewerDefaultMode(value as WorkflowViewerMode)}
          hint={
            workflowViewerDefaultMode === "side"
              ? "Workflow graphs open in a side panel. You can switch modes with the button in the viewer."
              : "Workflow graphs open inline above the chat. You can switch modes with the button in the viewer."
          }
        />
      </section>

      <section aria-labelledby="chat-editor-files" className="rounded-lg border border-border bg-card p-4 space-y-4">
        <h3 id="chat-editor-files" className="text-sm font-semibold">
          File browser
        </h3>
        <SettingToggle
          label="Show hidden files"
          description="Display files and folders starting with . (e.g., .git, .env)"
          checked={showHiddenFiles}
          onChange={setShowHiddenFiles}
        />
      </section>

      <MonacoEditorSettings />
    </div>
  );
}

// Monaco Editor Settings Component
function MonacoEditorSettings() {
  const { settings, updateSettings, resetSettings } = useEditorStore();

  return (
    <section aria-label="Code editor" className="rounded-lg border border-border bg-card p-4 space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h3 className="text-sm font-semibold">Code editor</h3>
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
            label="Line numbers"
            description="Show line numbers"
            checked={settings.lineNumbers}
            onChange={(checked) => updateSettings({ lineNumbers: checked })}
          />
          <SettingToggle
            label="Word wrap"
            description="Wrap long lines"
            checked={settings.wordWrap}
            onChange={(checked) => updateSettings({ wordWrap: checked })}
          />
          <SettingToggle
            label="Show whitespace"
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
            label={`Font size: ${settings.fontSize}px`}
            value={settings.fontSize}
            min={10}
            max={20}
            onChange={(value) => updateSettings({ fontSize: value })}
          />
          <SettingRange
            label={`Tab size: ${settings.tabSize} spaces`}
            value={settings.tabSize}
            min={2}
            max={8}
            step={2}
            onChange={(value) => updateSettings({ tabSize: value })}
          />
          <SettingToggle
            label="Smooth cursor"
            description="Smooth cursor movement"
            checked={settings.cursorSmoothCaretAnimation}
            onChange={(checked) =>
              updateSettings({ cursorSmoothCaretAnimation: checked })
            }
          />
          <SettingToggle
            label="Auto save"
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
                Auto save delay: {settings.autoSaveDelay}ms
              </label>
              <input
                aria-label="Auto save delay"
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
            label="Rainbow brackets"
            description="Colorize bracket pairs"
            checked={settings.bracketPairColorization}
            onChange={(checked) =>
              updateSettings({ bracketPairColorization: checked })
            }
          />
          <SettingToggle
            label="Indentation guides"
            description="Show indent lines"
            checked={settings.guides}
            onChange={(checked) => updateSettings({ guides: checked })}
          />
          <SettingToggle
            label="Quick suggestions"
            description="Auto-complete"
            checked={settings.quickSuggestions}
            onChange={(checked) =>
              updateSettings({ quickSuggestions: checked })
            }
          />

          <SettingSelect
            label="Cursor style"
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
            label="Line highlight"
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
        <h4 className="text-xs font-semibold text-foreground">Diff viewer</h4>

        <div className="space-y-4">
          {/* Diff View Mode */}
          <div className="space-y-3">
            <label htmlFor="diff-view-mode" className="text-sm font-medium block">View mode</label>
            <select
              id="diff-view-mode"
              value={settings.diffSideBySide ? "side-by-side" : "inline"}
              onChange={(e) => updateSettings({ diffSideBySide: e.target.value === "side-by-side" })}
              className="block w-full px-3 py-2 bg-background border border-border text-foreground rounded-md text-sm focus:outline-none focus:ring-2 focus:ring-ring focus:border-primary hover:border-border/80"
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
            label="Enable collapse controls"
            description="Show collapse buttons for unchanged regions"
            checked={settings.diffHideUnchanged}
            onChange={(checked) => updateSettings({ diffHideUnchanged: checked })}
          />
        </div>
      </div>

      {/* Language Servers
          Removed rather than left presenting toggles the product does not act
          on: the panel here wrote to editorStore.languageServers, which had no
          consumer anywhere outside the panel itself — no client ever launched
          a language server from it. Reliant's real language-server support is
          an MCP server (Settings -> MCP, or `.reliant/mcp.json` with
          `dirScoped` + `requiresFiles`), so the honest pointer is below rather
          than a second, fake control surface. */}
      <div className="space-y-4">
        <h4 className="text-xs font-medium text-muted-foreground">
          Language servers
        </h4>
        <p className="text-xs text-muted-foreground">
          Language servers run as MCP servers. Add one under Settings &rarr; MCP
          (or in <code>.reliant/mcp.json</code>) with <code>dirScoped: true</code> so each
          project gets its own index, and <code>requiresFiles</code> so it only starts
          in projects it can actually serve.
        </p>
      </div>
    </section>
  );
}

/** Whether replies appear as they are generated or all at once when done. */
function ResponseStreamingSetting() {
  const { data, isPending } = usePreferences();
  const update = useUpdatePreferences();
  const enabled = data.streamingEnabled;
  return (
    <div className="rounded-lg border border-border bg-card p-4">
      <div className="flex items-center justify-between gap-4">
        <div>
          <label htmlFor="streaming-toggle" className="text-sm font-semibold">
            Response streaming
          </label>
          <p className="mt-1 text-xs text-muted-foreground">
            Show replies as they are written. Off, each reply arrives all at once when it is done, which can be
            faster for short replies.
          </p>
        </div>
        <ToggleSwitch
          id="streaming-toggle"
          checked={enabled}
          // The switch flips at once and back if the server refuses.
          onChange={(next) => void update.mutateAsync({ streamingEnabled: next }).catch(() => {})}
          disabled={isPending}
          ariaLabel={`${enabled ? "Disable" : "Enable"} response streaming`}
          size="lg"
          className="forge-ui"
        />
      </div>
    </div>
  );
}
