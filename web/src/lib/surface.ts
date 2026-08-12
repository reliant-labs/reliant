/**
 * Surface capabilities — which product features exist on which client.
 *
 * Reliant renders through more than one surface, and they are deliberately not
 * feature-equivalent:
 *
 *   - `desktop` — the full ADE (Electron or desktop browser). Everything.
 *   - `mobile`  — the phone web app under `/m/*`. Monitor runs, chat with
 *     existing workflows, trigger work. NOT an authoring environment.
 *   - `embed`   — a white-labeled chat/workflow widget hosted inside someone
 *     else's app. Narrower still, and the host owns navigation and auth.
 *
 * Why a capability map instead of `if (isMobile)` at each call site:
 *
 *   1. **The omissions are the design.** "No workflow authoring on mobile" is a
 *      product decision that should be legible in one place, not reconstructed
 *      by grepping for viewport checks.
 *   2. **Scope control.** A mobile surface accretes features one reasonable-
 *      sounding request at a time. Adding a capability here is a visible,
 *      reviewable diff.
 *   3. **The embed needs the same switch.** An embedded widget hides project
 *      switching and settings for reasons that have nothing to do with screen
 *      size — so the axis must be *surface*, not viewport width.
 *
 * This is NOT responsive design. Use Tailwind breakpoints for layout that
 * adapts; use this for functionality that is absent. A capability that is
 * merely laid out differently does not belong here.
 */

/** Client surface. Distinct from viewport size — see the module comment. */
export type Surface = "desktop" | "mobile" | "embed";

/**
 * Every gated capability. Adding a key here forces every surface to declare a
 * position on it, which is the point — new features should not silently
 * default into the mobile or embed surface.
 */
export interface SurfaceCapabilities {
  // ─── Chat ─────────────────────────────────────────────────────────
  /** Send messages in an existing chat. The core of every surface. */
  chatSend: boolean;
  /** Start a new chat from a workflow starter. */
  chatCreate: boolean;
  /** Approve/deny tool-approval requests inline. */
  chatApprovals: boolean;
  /** Attach files to a message. Needs an OS picker + upload; deferred on mobile. */
  chatAttachments: boolean;
  /** Edit workflow params mid-chat. Authoring-adjacent; desktop only. */
  chatWorkflowParams: boolean;
  /** Pick which daemon a chat runs on. Power-user affordance. */
  chatDaemonSelection: boolean;
  /** Branch a chat into a new worktree from the chat header. */
  chatBranching: boolean;
  /** The execution sidebar (per-node run detail). Too dense for a phone. */
  chatExecutionSidebar: boolean;

  // ─── Workflows ────────────────────────────────────────────────────
  /** Read-only view of a workflow graph. */
  workflowView: boolean;
  /** The graph builder. Monaco + drag-and-drop; desktop only, indefinitely. */
  workflowAuthoring: boolean;

  // ─── Workspaces / git ─────────────────────────────────────────────
  /** Create a worktree. Mobile gets a name-only form; advanced opts hidden. */
  worktreeCreate: boolean;
  /** Full worktree management (archive, recreate, import, force-delete). */
  worktreeManage: boolean;
  /** Git status/diff/commit/push. Mobile: iteration 2. */
  gitManagement: boolean;

  // ─── Daemons ──────────────────────────────────────────────────────
  /** See daemon status and heartbeat. */
  daemonView: boolean;
  /** Resume a suspended daemon. Safe, fast, high-value on mobile. */
  daemonResume: boolean;
  /** Create/suspend/delete daemons. Slow + destructive; deferred on mobile. */
  daemonManage: boolean;

  // ─── Shell ────────────────────────────────────────────────────────
  /** Full settings surface. */
  settings: boolean;
  /**
   * A trimmed identity screen: who am I, sign out, theme.
   *
   * Deliberately NOT the same axis as `settings`. `settings` is the ~15-section
   * tree (MCP, connectors, keyboard shortcuts, model tag configs, appearance
   * fonts); shipping that on a phone would be a scope decision nobody made.
   * This is the much narrower question of whether a user can end their session
   * and start a new one. Without it a mobile user with an expired provider
   * credential has no recovery path except finding a desktop, which is a dead
   * end rather than a scope cut — so the two move independently.
   */
  mobileAccount: boolean;
  /** Switch the active project. Hidden in embeds — the host picks context. */
  projectSwitching: boolean;
  /** File viewer / code browser. Read-only on mobile, via a chat drill-in sheet. */
  fileViewer: boolean;
  /** Integrated terminal. Never on mobile; never in an embed. */
  terminal: boolean;

  // ─── Mobile settings drill-in ────────────────────────────────────
  //
  // A separate, narrower axis from `settings` (the ~15-section desktop
  // tree) — the same pattern `mobileAccount` established. Each flag gates
  // one full-screen section reachable from `/m/settings`; MCP, Prompts,
  // Keyboard shortcuts, Developer, Connectors and port-access rules
  // deliberately have no flag here because no mobile screen renders them.
  /** Provider API keys + a trimmed default-model picker. */
  settingsAI: boolean;
  /** Plan, wallet balance, and a usage summary. */
  settingsBilling: boolean;
  settingsNotifications: boolean;
  settingsPrivacy: boolean;
  settingsAppearance: boolean;
  /** Worktree archive/branch/delete defaults. */
  settingsWorkspace: boolean;
  /** Version display only — no update-check UI. */
  settingsAbout: boolean;
  /**
   * GitHub connection status/connect/disconnect, plus repo browsing and
   * clone-to-daemon. Gated separately from `gitManagement` (git
   * status/diff/commit/push on an existing worktree) — this is credential
   * and provisioning work, not working-tree git, and it is not filesystem-
   * bound: `CloneRepo` targets a daemon over NATS, so a phone can trigger it.
   */
  settingsGitHub: boolean;

  // ─── Search ───────────────────────────────────────────────────────
  /** Chat history search + search within the open chat. */
  searchChats: boolean;
  /**
   * File content search (regex/glob filters). Results open a read-only,
   * Prism-highlighted preview — NOT the full `fileViewer` code browser.
   */
  searchFiles: boolean;
}

const DESKTOP: SurfaceCapabilities = {
  chatSend: true,
  chatCreate: true,
  chatApprovals: true,
  chatAttachments: true,
  chatWorkflowParams: true,
  chatDaemonSelection: true,
  chatBranching: true,
  chatExecutionSidebar: true,
  workflowView: true,
  workflowAuthoring: true,
  worktreeCreate: true,
  worktreeManage: true,
  gitManagement: true,
  daemonView: true,
  daemonResume: true,
  daemonManage: true,
  settings: true,
  mobileAccount: true,
  projectSwitching: true,
  fileViewer: true,
  terminal: true,
  settingsAI: true,
  settingsBilling: true,
  settingsNotifications: true,
  settingsPrivacy: true,
  settingsAppearance: true,
  settingsWorkspace: true,
  settingsAbout: true,
  settingsGitHub: true,
  searchChats: true,
  searchFiles: true,
};

/**
 * Mobile web (iteration 1). Monitoring + triggering, not authoring.
 *
 * The `false` entries are the scope boundary. Flipping one to `true` is a
 * product decision — it should show up in review as exactly that, and it
 * should come with the mobile UI to back it.
 */
const MOBILE: SurfaceCapabilities = {
  chatSend: true,
  chatCreate: true,
  chatApprovals: true,
  // Deferred to iteration 2 — needs an OS picker and upload progress UI.
  chatAttachments: false,
  // Authoring. Explicitly out of scope for the mobile surface.
  chatWorkflowParams: false,
  chatDaemonSelection: false,
  chatBranching: false,
  // Dense multi-pane UI; no phone layout worth shipping.
  chatExecutionSidebar: false,
  // A flat, ordered step list (MobileWorkflowStepList/MobileWorkflowScreen)
  // reachable from `/m/workflows` and from a running chat — not the desktop
  // ReactFlow canvas, which needs pan/zoom room a 390px viewport doesn't
  // have. Authoring stays off below; this is view-only.
  workflowView: true,
  workflowAuthoring: false,
  // Off until there is a mobile UI that reaches it. The RPC supports
  // name-only creation, but nothing on the mobile surface opens that flow, and
  // a capability that no screen honours is worse than one set false: it reads
  // as shipped in review. Flip this back the same day the create sheet lands.
  //
  // There is also an unresolved question to answer first: CreateWorktree is a
  // 30-120s unary RPC with no completion event, so backgrounding the app
  // mid-create loses the result. See the mobile scoping doc's pre-work list.
  worktreeCreate: false,
  worktreeManage: false,
  // Read-only status/diff via a chat drill-in sheet; no staging, commit, or
  // push until the cloud-daemon bearer-token work lands.
  gitManagement: false,
  daemonView: true,
  daemonResume: true,
  // Slow and destructive — wants push notifications before it makes sense.
  daemonManage: false,
  // A trimmed account/sign-out screen is separate from the full settings tree.
  settings: false,
  mobileAccount: true,
  // Off: MobileShell selects the user's first project and there is no picker.
  // Marking this true described an intent rather than a behaviour — a user
  // with three projects cannot reach the other two from a phone today.
  projectSwitching: false,
  // Read-only browse + preview via a chat drill-in sheet (MobileWorkspaceSheet).
  // Deliberately NOT the desktop FileViewerTab/Monaco path — see that
  // component's own module comment for why.
  fileViewer: true,
  terminal: false,
  // The mobile settings drill-in (`/m/settings`). Each section is a thin
  // mobile view over the same hooks the desktop settings tree uses, verified
  // responsive at 390px — not the desktop sidebar+panel layout.
  settingsAI: true,
  settingsBilling: true,
  settingsNotifications: true,
  settingsPrivacy: true,
  settingsAppearance: true,
  settingsWorkspace: true,
  settingsAbout: true,
  // Connection management + clone are both server-side (daemon-targeted),
  // not filesystem-bound, so nothing about being on a phone rules this out.
  settingsGitHub: true,
  // `/m/search`: both desktop search features exist behind keyboard shortcuts
  // only, so they were invisible on mobile despite being responsive.
  searchChats: true,
  searchFiles: true,
};

/**
 * Embedded white-label widget. Narrower than mobile in a different direction:
 * the host owns navigation, project context, and settings, so those are absent
 * regardless of screen size — which is exactly why this axis is surface and
 * not viewport.
 */
const EMBED: SurfaceCapabilities = {
  chatSend: true,
  chatCreate: false,
  chatApprovals: true,
  chatAttachments: false,
  chatWorkflowParams: false,
  chatDaemonSelection: false,
  chatBranching: false,
  chatExecutionSidebar: false,
  workflowView: true,
  workflowAuthoring: false,
  worktreeCreate: false,
  worktreeManage: false,
  gitManagement: false,
  daemonView: false,
  daemonResume: false,
  daemonManage: false,
  settings: false,
  // The host app owns identity. A Sign out button inside someone else's
  // product would end a session the host established and never asked us to
  // manage — worse than absent.
  mobileAccount: false,
  projectSwitching: false,
  fileViewer: false,
  terminal: false,
  // The host app owns its own settings and search UI, if any — none of this
  // axis applies inside someone else's product.
  settingsAI: false,
  settingsBilling: false,
  settingsNotifications: false,
  settingsPrivacy: false,
  settingsAppearance: false,
  settingsWorkspace: false,
  settingsAbout: false,
  settingsGitHub: false,
  searchChats: false,
  searchFiles: false,
};

const CAPABILITIES: Record<Surface, SurfaceCapabilities> = {
  desktop: DESKTOP,
  mobile: MOBILE,
  embed: EMBED,
};

/** Capability set for a surface. */
export function capabilitiesFor(surface: Surface): SurfaceCapabilities {
  return CAPABILITIES[surface];
}

/**
 * Resolve the surface from a pathname.
 *
 * Path-based rather than viewport-based on purpose: a phone-sized desktop
 * browser window should still get the full ADE, and a tablet loading `/m/`
 * should get the mobile surface. The URL is the explicit signal; screen width
 * is a guess. `embed` is never inferred from a path — an embedding host sets
 * it directly when mounting the widget.
 */
export function surfaceForPath(pathname: string): Surface {
  return pathname === "/m" || pathname.startsWith("/m/") ? "mobile" : "desktop";
}
