/**
 * Shared command registry.
 *
 * One list of "things the user can do", consumed by three surfaces:
 *   - the command palette (Cmd+Shift+P)
 *   - the slash menu in the composer ("/")
 *   - keyboard shortcuts, via the handler names in config/shortcuts.yaml
 *
 * Keeping them in one place is what stops the three from drifting: adding an
 * action here makes it reachable from all of them, and a command that maps to a
 * shortcut shows that shortcut's current binding without hardcoding it.
 */

export type CommandCategory =
  | "navigation"
  | "chat"
  | "panels"
  | "params"
  | "settings"
  | "actions";

export interface CommandDefinition {
  id: string;
  title: string;
  description: string;
  category: CommandCategory;
  keywords?: string[];
  /**
   * Handler name from config/shortcuts.yaml, when this command is also bound to
   * a key. Lets a surface display the live binding rather than a stale literal.
   */
  handler?: string;
  /** Hide from the composer's slash menu (too niche, or needs a mouse). */
  hiddenFromSlash?: boolean;
}

/**
 * Commands surfaced in the slash menu, in display order.
 *
 * Deliberately short: the slash menu is for the handful of actions worth
 * reaching mid-sentence, not a mirror of the whole palette. The palette stays
 * the exhaustive surface.
 */
export const SLASH_COMMAND_IDS = [
  // Composer-scoped actions first: these are the ones worth reaching for
  // mid-sentence, and several have no keyboard shortcut at all.
  "compact",
  "prompt",
  "attach",
  "branch",
  "copy-transcript",
  // Then the shortcut-backed actions, so the menu also teaches the keyboard.
  "new-chat",
  "switch-chat",
  "switch-project",
  "search-chats",
  "change-model",
  "change-thinking",
  "change-workflow",
  "toggle-terminal",
  "show-changes",
  "settings",
] as const;

export const COMMANDS: CommandDefinition[] = [
  // Composer-scoped actions.
  //
  // These are the reason the slash menu is worth having on top of the command
  // palette: they act on the message you are composing or the conversation you
  // are in, so they only make sense from the composer and deliberately carry no
  // global keyboard shortcut.
  {
    id: "compact",
    title: "Compact Conversation",
    description: "Summarize earlier turns to free up context",
    category: "chat",
    keywords: ["summarize", "context", "shrink", "condense", "token"],
  },
  {
    id: "prompt",
    title: "Insert Prompt",
    description: "Insert one of your saved prompts",
    category: "chat",
    keywords: ["template", "snippet", "saved", "reuse"],
  },
  {
    id: "attach",
    title: "Attach File",
    description: "Attach a file or image to this message",
    category: "chat",
    keywords: ["upload", "image", "screenshot", "document"],
  },
  {
    id: "branch",
    title: "Branch Chat",
    description: "Fork this conversation into its own worktree",
    category: "chat",
    keywords: ["fork", "worktree", "split", "copy"],
  },
  {
    id: "copy-transcript",
    title: "Copy Transcript",
    description: "Copy the conversation to the clipboard as Markdown",
    category: "chat",
    keywords: ["export", "share", "markdown", "clipboard"],
  },

  // Navigation
  {
    id: "new-chat",
    title: "New Chat",
    description: "Start a new conversation",
    category: "chat",
    keywords: ["create", "start", "tab"],
    handler: "onNewChat",
  },
  {
    id: "switch-chat",
    title: "Switch Chat",
    description: "Jump to another chat by name",
    category: "chat",
    keywords: ["go to", "find", "jump", "conversation"],
    handler: "onSwitchChat",
  },
  {
    id: "switch-project",
    title: "Switch Project",
    description: "Jump to another project",
    category: "navigation",
    keywords: ["go to", "repo", "repository", "workspace"],
    handler: "onSwitchProject",
  },
  {
    id: "search-chats",
    title: "Search Chats",
    description: "Search this conversation and chat history",
    category: "chat",
    keywords: ["find", "grep", "message"],
    handler: "onChatSearch",
  },
  {
    id: "quick-open",
    title: "Open File",
    description: "Open a file by name",
    category: "navigation",
    keywords: ["file", "fuzzy", "goto"],
    handler: "onQuickFileOpen",
  },

  // Parameters
  {
    id: "change-model",
    title: "Change Model",
    description: "Pick the model for this chat",
    category: "params",
    keywords: ["llm", "provider", "claude", "gpt"],
    handler: "onChangeModel",
  },
  {
    id: "change-thinking",
    title: "Change Thinking Level",
    description: "Adjust reasoning effort",
    category: "params",
    keywords: ["reasoning", "effort", "extended"],
    handler: "onChangeThinkingLevel",
  },
  {
    id: "change-workflow",
    title: "Change Workflow",
    description: "Pick the workflow the next message runs",
    category: "params",
    keywords: ["agent", "preset", "mode"],
    handler: "onChangeWorkflow",
  },
  {
    id: "edit-workflow-params",
    title: "Edit Workflow Parameters",
    description: "Open the parameter panel for the selected workflow",
    category: "params",
    keywords: ["inputs", "arguments", "config"],
    handler: "onEditWorkflowParams",
  },

  // Panels
  {
    id: "show-files",
    title: "Show Files",
    description: "Open the file browser",
    category: "panels",
    keywords: ["tree", "explorer", "sidebar"],
    handler: "onSidebarFiles",
  },
  {
    id: "show-changes",
    title: "Show Changes",
    description: "Open source control changes",
    category: "panels",
    keywords: ["git", "diff", "staged", "commit"],
    handler: "onSidebarChanges",
  },
  {
    id: "show-tasks",
    title: "Show Tasks",
    description: "Open the tasks panel",
    category: "panels",
    keywords: ["todo", "plan"],
    handler: "onSidebarTasks",
  },
  {
    id: "show-processes",
    title: "Show Processes & Packages",
    description: "Open running processes and package commands",
    category: "panels",
    keywords: ["npm", "run", "scripts", "server", "logs"],
    handler: "onSidebarProcesses",
  },
  {
    id: "run-package-command",
    title: "Run Package Command",
    description: "Run an npm, cargo, or poetry command",
    category: "actions",
    keywords: ["npm", "cargo", "poetry", "script", "build", "test"],
    handler: "onRunPackageCommand",
  },
  {
    id: "toggle-terminal",
    title: "Toggle Terminal",
    description: "Show or hide the terminal",
    category: "panels",
    keywords: ["shell", "console", "bash"],
    handler: "onToggleTerminal",
  },

  // Settings
  {
    id: "settings",
    title: "Settings",
    description: "Open settings",
    category: "settings",
    keywords: ["preferences", "config", "options"],
    handler: "onToggleSettings",
  },
  {
    id: "shortcuts",
    title: "Keyboard Shortcuts",
    description: "View and remap keyboard shortcuts",
    category: "settings",
    keywords: ["keys", "bindings", "hotkeys"],
  },
];

const BY_ID = new Map(COMMANDS.map((command) => [command.id, command]));

export function getCommand(id: string): CommandDefinition | undefined {
  return BY_ID.get(id);
}

/** Commands for the composer's slash menu, in the curated order. */
export function slashCommands(): CommandDefinition[] {
  return SLASH_COMMAND_IDS.map((id) => BY_ID.get(id)).filter(
    (command): command is CommandDefinition =>
      command !== undefined && !command.hiddenFromSlash,
  );
}
