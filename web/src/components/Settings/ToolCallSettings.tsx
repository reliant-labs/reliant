/**
 * Tool Call Settings Component
 * 
 * Allows users to configure default collapse/expand behavior for different tool types
 * in the chat interface.
 */

import { useState, useEffect } from "react";
import { Toggle } from "../ui/Toggle";
import {
  Eye,
  FileEdit,
  Search,
  Terminal,
  ListTodo,
  Puzzle,
  Bot,
  RotateCcw,
} from "lucide-react";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";

/**
 * Interface for tool collapse defaults settings
 * true = collapsed by default, false = expanded by default
 */
export interface ToolCollapseDefaults {
  fileView: boolean;    // view, read, read_files
  fileWrite: boolean;   // write, edit, patch
  searchRead: boolean;  // grep, glob, ls, find_files, websearch, fetch, diagnostics
  execution: boolean;   // shell, run_command
  planning: boolean;    // create_plan, update_plan, update_task, add_task, list_tasks, get_plan
  mcp: boolean;         // any tool with '/' in the name (MCP tools)
  agent: boolean;       // agent, sub-agent spawning tools
}

/**
 * Default collapse settings - matches existing behavior
 */
export const DEFAULT_TOOL_COLLAPSE_SETTINGS: ToolCollapseDefaults = {
  fileView: true,       // collapsed by default (view-only tools)
  fileWrite: false,     // expanded by default (show diffs)
  searchRead: true,     // collapsed by default (can expand to see results)
  execution: false,     // expanded by default (show command output)
  planning: true,       // collapsed by default
  mcp: true,            // collapsed by default
  agent: true,          // collapsed by default
};

/**
 * Get the current tool collapse defaults from settings
 */
export function getToolCollapseDefaults(): ToolCollapseDefaults {
  return settingsSync.getJSONSetting<ToolCollapseDefaults>(
    SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS,
    DEFAULT_TOOL_COLLAPSE_SETTINGS
  );
}

/**
 * Determine which category a tool belongs to based on its name
 */
export function getToolCategory(toolName: string): keyof ToolCollapseDefaults | null {
  const nameLower = toolName.toLowerCase();
  
  // MCP tools have '/' in their name (e.g., "mcp_server/tool_name")
  if (toolName.includes('/') || nameLower.startsWith('mcp_')) {
    return 'mcp';
  }
  
  // Agent tools
  if (nameLower === 'agent' || nameLower.includes('subagent') || nameLower.includes('sub_agent')) {
    return 'agent';
  }
  
  // File view tools
  if (['view', 'read', 'read_files'].includes(nameLower)) {
    return 'fileView';
  }
  
  // File write tools
  if (['write', 'edit', 'patch', 'find_replace', 'move_code'].includes(nameLower)) {
    return 'fileWrite';
  }
  
  // Search/read tools
  if (['grep', 'glob', 'ls', 'find_files', 'websearch', 'fetch', 'diagnostics'].includes(nameLower)) {
    return 'searchRead';
  }

  // Execution tools
  if (['bash', 'powershell', 'run_command', 'worktree'].includes(nameLower)) {
    return 'execution';
  }
  
  // Planning tools
  if (['create_plan', 'update_plan', 'get_plan', 'update_task', 'add_task', 'list_tasks'].includes(nameLower)) {
    return 'planning';
  }
  
  // Unknown tool - return null to use fallback behavior
  return null;
}

/**
 * Get whether a tool should be collapsed by default based on settings
 * Returns true if collapsed, false if expanded
 */
export function shouldToolBeCollapsed(toolName: string): boolean {
  const defaults = getToolCollapseDefaults();
  const category = getToolCategory(toolName);
  
  if (category) {
    return defaults[category];
  }
  
  // Fallback: unknown tools are collapsed by default
  return true;
}

interface ToolCategoryToggleProps {
  icon: React.ReactNode;
  label: string;
  description: string;
  examples: string;
  collapsed: boolean;
  onChange: (collapsed: boolean) => void;
}

function ToolCategoryToggle({
  icon,
  label,
  description,
  examples,
  collapsed,
  onChange,
}: ToolCategoryToggleProps) {
  return (
    <div className="flex items-start justify-between gap-4 px-4 py-4">
      <div className="flex items-start gap-3">
        <div className="mt-0.5 text-muted-foreground">{icon}</div>
        <div className="space-y-1">
          <label className="text-sm font-medium">{label}</label>
          <p className="text-xs text-muted-foreground">{description}</p>
          {examples && <p className="text-xs text-muted-foreground/70 font-mono">{examples}</p>}
        </div>
      </div>
      <div className="flex items-center gap-2 shrink-0">
        <span className="text-xs text-muted-foreground">
          {collapsed ? "Collapsed" : "Expanded"}
        </span>
        <Toggle
          checked={!collapsed}
          onChange={(checked) => onChange(!checked)}
          label={`${label} default state`}
        />
      </div>
    </div>
  );
}

export function ToolCallSettings() {
  const [settings, setSettings] = useState<ToolCollapseDefaults>(DEFAULT_TOOL_COLLAPSE_SETTINGS);
  const [isLoaded, setIsLoaded] = useState(false);

  // Load settings from settingsSync on mount
  useEffect(() => {
    const loadSettings = async () => {
      // Wait for settingsSync to be initialized
      let attempts = 0;
      while (!settingsSync.isInitialized() && attempts < 50) {
        await new Promise(resolve => setTimeout(resolve, 100));
        attempts++;
      }
      
      const saved = getToolCollapseDefaults();
      setSettings(saved);
      setIsLoaded(true);
    };
    
    loadSettings();
  }, []);

  const updateSetting = (key: keyof ToolCollapseDefaults, value: boolean) => {
    const updated = { ...settings, [key]: value };
    setSettings(updated);
    // Save to settingsSync (which saves to localStorage AND database)
    settingsSync.setJSONSetting(SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS, updated).catch(console.error);
  };

  const resetToDefaults = () => {
    setSettings(DEFAULT_TOOL_COLLAPSE_SETTINGS);
    settingsSync.setJSONSetting(SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS, DEFAULT_TOOL_COLLAPSE_SETTINGS).catch(console.error);
  };

  if (!isLoaded) {
    return null;
  }

  return (
    <div className="space-y-8">
      <div>
        <h2 className="text-lg font-semibold">Tool Call Display</h2>
        <p className="text-xs text-muted-foreground">
          Configure how tool calls are displayed in the chat. Toggle whether each
          tool category should be collapsed or expanded by default.
        </p>
      </div>

      <div className="space-y-4">
        <div className="flex items-center justify-between">
          <h3 className="text-sm font-semibold">Default Display State</h3>
          <button
            onClick={resetToDefaults}
            className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
          >
            <RotateCcw className="w-3 h-3" />
            Reset to defaults
          </button>
        </div>

        <div className="border border-border rounded-lg divide-y divide-border">
          <ToolCategoryToggle
            icon={<Eye className="w-4 h-4" />}
            label="File View Tools"
            description="Tools that read and display file contents"
            examples="view, read, read_files"
            collapsed={settings.fileView}
            onChange={(v) => updateSetting("fileView", v)}
          />

          <ToolCategoryToggle
            icon={<FileEdit className="w-4 h-4" />}
            label="File Write Tools"
            description="Tools that create or modify files (shows diffs)"
            examples="write, edit, patch, find_replace"
            collapsed={settings.fileWrite}
            onChange={(v) => updateSetting("fileWrite", v)}
          />

          <ToolCategoryToggle
            icon={<Search className="w-4 h-4" />}
            label="Search & Read Tools"
            description="Tools that search or fetch data"
            examples="grep, glob, ls, websearch, fetch, diagnostics"
            collapsed={settings.searchRead}
            onChange={(v) => updateSetting("searchRead", v)}
          />

          <ToolCategoryToggle
            icon={<Terminal className="w-4 h-4" />}
            label="Execution Tools"
            description="Tools that run commands or scripts"
            examples="shell, run_command"
            collapsed={settings.execution}
            onChange={(v) => updateSetting("execution", v)}
          />

          <ToolCategoryToggle
            icon={<ListTodo className="w-4 h-4" />}
            label="Planning Tools"
            description="Tools for task and plan management"
            examples="create_plan, update_task, add_task"
            collapsed={settings.planning}
            onChange={(v) => updateSetting("planning", v)}
          />

          <ToolCategoryToggle
            icon={<Puzzle className="w-4 h-4" />}
            label="MCP Tools"
            description="Tools from MCP (Model Context Protocol) servers"
            examples="mcp_server/tool_name"
            collapsed={settings.mcp}
            onChange={(v) => updateSetting("mcp", v)}
          />

          <ToolCategoryToggle
            icon={<Bot className="w-4 h-4" />}
            label="Agent Tools"
            description="Tools that spawn sub-agents or delegate work"
            examples="agent, sub_agent"
            collapsed={settings.agent}
            onChange={(v) => updateSetting("agent", v)}
          />
        </div>

        <p className="text-xs text-muted-foreground">
          Note: You can always manually expand or collapse individual tool calls
          in the chat, regardless of these defaults.
        </p>
      </div>
    </div>
  );
}

// Compact version for embedding in Appearance settings
export function ToolCallSettingsCompact() {
  const [settings, setSettings] = useState<ToolCollapseDefaults>(DEFAULT_TOOL_COLLAPSE_SETTINGS);
  const [isLoaded, setIsLoaded] = useState(false);

  // Load settings from settingsSync on mount
  useEffect(() => {
    const loadSettings = async () => {
      // Wait for settingsSync to be initialized
      let attempts = 0;
      while (!settingsSync.isInitialized() && attempts < 50) {
        await new Promise(resolve => setTimeout(resolve, 100));
        attempts++;
      }
      
      const saved = getToolCollapseDefaults();
      setSettings(saved);
      setIsLoaded(true);
    };
    
    loadSettings();
  }, []);

  const updateSetting = (key: keyof ToolCollapseDefaults, value: boolean) => {
    const updated = { ...settings, [key]: value };
    setSettings(updated);
    settingsSync.setJSONSetting(SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS, updated).catch(console.error);
  };

  const resetToDefaults = () => {
    setSettings(DEFAULT_TOOL_COLLAPSE_SETTINGS);
    settingsSync.setJSONSetting(SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS, DEFAULT_TOOL_COLLAPSE_SETTINGS).catch(console.error);
  };

  if (!isLoaded) {
    return null;
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <span className="text-xs text-muted-foreground">Default display state</span>
        <button
          onClick={resetToDefaults}
          className="flex items-center gap-1.5 text-xs text-muted-foreground hover:text-foreground transition-colors"
        >
          <RotateCcw className="w-3 h-3" />
          Reset
        </button>
      </div>

      <div className="border border-border rounded-lg divide-y divide-border">
        <ToolCategoryToggle
          icon={<Eye className="w-4 h-4" />}
          label="File View Tools"
          description="view, read, read_files"
          examples=""
          collapsed={settings.fileView}
          onChange={(v) => updateSetting("fileView", v)}
        />
        <ToolCategoryToggle
          icon={<FileEdit className="w-4 h-4" />}
          label="File Write Tools"
          description="write, edit, patch"
          examples=""
          collapsed={settings.fileWrite}
          onChange={(v) => updateSetting("fileWrite", v)}
        />
        <ToolCategoryToggle
          icon={<Search className="w-4 h-4" />}
          label="Search & Read Tools"
          description="grep, glob, ls, websearch"
          examples=""
          collapsed={settings.searchRead}
          onChange={(v) => updateSetting("searchRead", v)}
        />
        <ToolCategoryToggle
          icon={<Terminal className="w-4 h-4" />}
          label="Execution Tools"
          description="shell, run_command"
          examples=""
          collapsed={settings.execution}
          onChange={(v) => updateSetting("execution", v)}
        />
        <ToolCategoryToggle
          icon={<ListTodo className="w-4 h-4" />}
          label="Planning Tools"
          description="create_plan, update_task"
          examples=""
          collapsed={settings.planning}
          onChange={(v) => updateSetting("planning", v)}
        />
        <ToolCategoryToggle
          icon={<Puzzle className="w-4 h-4" />}
          label="MCP Tools"
          description="MCP server tools"
          examples=""
          collapsed={settings.mcp}
          onChange={(v) => updateSetting("mcp", v)}
        />
        <ToolCategoryToggle
          icon={<Bot className="w-4 h-4" />}
          label="Agent Tools"
          description="agent, sub_agent"
          examples=""
          collapsed={settings.agent}
          onChange={(v) => updateSetting("agent", v)}
        />
      </div>
    </div>
  );
}
