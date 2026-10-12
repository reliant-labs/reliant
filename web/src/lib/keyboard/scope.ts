/**
 * What every shortcut does when the workspace is not on screen.
 *
 * The keyboard host (components/Shortcuts/ShortcutHost) runs on every
 * authenticated desktop route — Settings, Inbox, Workflows, Runs, Forge — but
 * most handlers belong to ModernApp, which mounts only on `/` and
 * `/project/$projectId`. A handler that is simply missing off those routes
 * would leave the key unclaimed, which is fine for a chord the browser has its
 * own meaning for and wrong for everything else: a desktop menu item that
 * delivers the shortcut by id would silently do nothing.
 *
 * So every handler in config/shortcuts.yaml declares its reach here, and
 * scope.test.ts fails when one does not. The rule this table encodes: no
 * shortcut may silently do nothing. Each either works where you are, takes you
 * into the workspace and acts there, or deliberately leaves the key to the
 * browser because that meaning is the better one off the workspace.
 */

export type OffWorkspace =
  /** App-level: the host implements it, and it runs wherever you are. */
  | "here"
  /**
   * Acts on the composer on screen — a run's chat, the builder's chat — or,
   * with none on screen, on the workspace's after going there.
   */
  | "composer"
  /** Goes into the workspace, then runs there. */
  | "workspace"
  /**
   * Goes into the workspace and runs `reveal` instead. For toggles: arriving
   * from another page, "toggle the terminal" means "show me the terminal",
   * and toggling one that was already open would land on a workspace with no
   * terminal in it — a shortcut that looked like it did nothing.
   */
  | { reveal: string }
  /** Closes the page you are on, back to the workspace. */
  | "leave"
  /** Not registered: the key keeps its browser or native meaning. */
  | "native"
  /** Exists only inside the workflow builder, which registers it while mounted. */
  | "builder";

export const OFF_WORKSPACE: Readonly<Record<string, OffWorkspace>> = {
  // ── App-level ────────────────────────────────────────────────────────────
  onCommandPalette: "here",
  // Searches chat history off the workspace: there is no open conversation to
  // search in, and a hit opens that chat in the workspace.
  onChatSearch: "here",
  onSwitchChat: "here",
  onToggleSettings: "here",
  onOpenWorkflows: "here",
  onOpenProject: "here",
  onSwitchProject: "here",
  // Passthrough entries that only exist to claim a chord inside an editing
  // context (Monaco's own find, native selection). Nothing to do anywhere.
  onEditorFind: "here",
  onNativeSelection: "here",

  // ── The composer on screen ───────────────────────────────────────────────
  onChangeModel: "composer",
  onChangeThinkingLevel: "composer",
  onChangeWorkflow: "composer",
  onEditWorkflowParams: "composer",
  onSlashCommands: "composer",
  onDismissSlashMenu: "composer",
  onComposerNewline: "composer",
  // In the workspace it also brings the chat back over an open file — which
  // is the workspace's own handler, run there when no composer is on screen.
  onFocusChat: "composer",

  // ── Into the workspace, then act ─────────────────────────────────────────
  onNewChat: "workspace",
  onNextChat: "workspace",
  onPrevChat: "workspace",
  onNextAttentionChat: "workspace",
  onSwitchWorktree: "workspace",
  onNextProject: "workspace",
  onPrevProject: "workspace",
  onQuickFileOpen: "workspace",
  onFindInFiles: "workspace",
  onFindReplace: "workspace",
  onNextFileTab: "workspace",
  onPrevFileTab: "workspace",
  onReopenLastClosedFile: "workspace",
  onFocusTranscript: "workspace",
  onFocusTerminal: "workspace",
  onFocusLeftSidebar: "workspace",
  onFocusRightSidebar: "workspace",
  onFocusFileEditor: "workspace",
  onNextRightSidebarTab: "workspace",
  onPrevRightSidebarTab: "workspace",
  onSidebarFiles: "workspace",
  onSidebarChanges: "workspace",
  onSidebarTasks: "workspace",
  onSidebarProcesses: "workspace",
  onSidebarBrowser: "workspace",
  onRunPackageCommand: "workspace",
  onNewTerminal: "workspace",
  onClearTerminal: "workspace",
  onToggleTerminal: { reveal: "onFocusTerminal" },
  onToggleFileBrowser: { reveal: "onFocusRightSidebar" },
  onToggleSidebar: { reveal: "onFocusLeftSidebar" },

  // ── Close this page ──────────────────────────────────────────────────────
  // Settings, the Inbox and the Workflows area sit over the workspace the way
  // a tab does, so closing "the tab" off the workspace closes the page. It must
  // never reach into the workspace to close a file the user cannot see, nor
  // close the desktop window from a settings page.
  onCloseTab: "leave",
  // "Send the cursor back where it came from" — which, off the workspace, is
  // the workspace.
  onFocusPrevious: "leave",

  // ── The key keeps its own meaning ────────────────────────────────────────
  // Escape belongs to every dialog, menu and field on those pages; pausing a
  // chat the user cannot see from one of them would be a hazard.
  onStopStreaming: "native",
  // Approving tool calls nobody is looking at is never what Cmd+Enter meant on
  // a settings form.
  onApproveToolRequests: "native",
  // Off the workspace, each dialog closes itself on Escape. Registering this
  // there would claim Escape ahead of them and close nothing.
  onDismissModal: "native",
  // Bound in the `file-tree` context, which exists only in the workspace.
  onCutFile: "native",
  onCopyFile: "native",
  onPasteFile: "native",

  // ── Workflow builder ─────────────────────────────────────────────────────
  onOpenStepPalette: "builder",
  onConnectSelectedSteps: "builder",
};

/** True for a reach that takes the user into the workspace to act there. */
export function goesToWorkspace(reach: OffWorkspace): boolean {
  return reach === "workspace" || typeof reach === "object";
}
