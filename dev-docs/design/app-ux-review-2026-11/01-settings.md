> Copied on 2026-11-01 from `research/app-ux-review-2026-11/01-settings.md` (outside any repository) so it is versioned with the synthesis.

# 01 — Settings (every settings page)

Reviewer: settings area agent, 2026-11-01. Review only: no product code changed.

- **Code reviewed:** reliant `origin/main` **0ec4b8119**. This is the same commit as HARNESS.md v2. The commit the main checkout sits on, 9f51649ee, is ~150 files behind in `components/Settings`. Its sections have been renamed and regrouped since, so it must not be used for this area.
- **Preview:** https://5316-2aab1465-f76b-4d95-b61b-480d9ba070e6.workspaces.reliantapi.com/settings/account
  - Production `vite build` from my worktree `ux-review-settings-8a60fea7`, served by `harness/run.sh 5316` with the v2 harness.
  - Fixtures: `fixtures-settings/` adds `GetPreferences`. The scenarios `?fx=settings-errors` and `?fx=settings-slow` are stubbed there.
- **How findings were checked:** measured, not seen.
  - `take_screenshot` returns no pixels in this harness (bug 3f6a8b8e).
  - `take_screenshot` with `filePath` is refused, because `research/…/shots/` is outside the DevTools MCP's workspace roots. So no screenshots are saved for this area.
  - Every finding below comes from the DOM and a11y tree, computed styles, a WCAG contrast probe and an overflow probe run on all 20 desktop sections, plus a code read.
  - Widths checked: 1905 and 1440 px desktop, dark and light themes, and 390 px with `?desktop=1` (the desktop layout on a narrow window). `/m/settings` was checked as the phone surface.

---

## 1. Page inventory

Sources:
- the route tree: `src/routes.tsx:574`, `src/settingsSectionRoutes.tsx`, `src/settingsConnectorRoutes.tsx`;
- the ids: `src/routeSchemas.ts` (`SETTINGS_SECTION_IDS`, `SETTINGS_SECTION_ALIASES`, `BILLING_TAB_IDS`);
- the nav: `components/Settings/SettingsNavigation.tsx`;
- the page switch: `components/Settings/SettingsContent.tsx`.

### Routes

| Route | Behaviour |
|---|---|
| `/settings` | `SettingsPage`, default section `account` |
| `/settings/$section` | 19 section ids, below |
| `/settings/machines/$machineId` | Machines with one machine open (detail and Access card) |
| `/settings/environments/$machineId` | Redirects to `/settings/machines/$machineId` |
| aliases | `general`→`ai`, `environments`→`machines`, `workspaces`→`branches`, `machine`, `worktrees`, `providers` redirect (replace) |
| unknown slug | Redirects to `/settings?notFound=<slug>` with a dismissible notice (`SettingsPage.tsx`) |
| `/settings/connectors` | Retired. Redirects to `/settings/machines?from=connectors` |
| `/settings/connectors/authorize` | OAuth consent page for third-party MCP clients (`ConnectorConsentPage.tsx`, `ConnectorConsent.tsx`). Its path is a contract |
| `/settings/billing?tab=` | `overview`, `plans` ("Change plan"), `usage` ("Usage & invoices"), `ai` ("Reliant AI"). `invoices`→usage is a legacy value. `limits` is accepted but not in the strip |
| Phone: `/m/settings` | 10 drill-in sub-screens held in component state, not the URL (`Mobile/MobileSettingsScreen.tsx`). Also `/m/account`, `/m/github`, `/m/diagnostics` |
| Phone redirect | `lib/mobileRedirect.ts mobilePathFor`: `/settings/machines*` → `/m/daemons`; `/settings/billing` → `/m/settings?section=billing`; every other `/settings/*` → the `/m/settings` list, and the section is lost |

### Desktop sections, in nav order (4 groups, 20 items)

| Group | Nav label | id | Component | Internal tabs / sub-views |
|---|---|---|---|---|
| Account | Account | `account` | `AccountSettings.tsx`, `DeleteAccountDialog.tsx`, `../LinkedAccounts` | — |
| Account | Billing & usage (cloud only) | `billing` | `cloud/billing.tsx` and `cloud/overview/*`, `cloud/usage/*`, `cloud/reliantAI.tsx` | Overview · Change plan · Usage & invoices · Reliant AI (URL `?tab=`) |
| Account | Members (cloud only) | `member-permissions` | `cloud/MemberPermissions.tsx` | — |
| AI | AI providers | `ai` | `AISettings.tsx`, `providers/*`, `ModelPreferences.tsx`, `ToolPreferences.tsx`, `LocalModelsSection.tsx`, `CustomEndpointsSection.tsx` | Providers · Models · Local & custom (component state, **not** URL) |
| AI | MCP servers | `mcp` | `MCPSettings.tsx`, `MCPTabs.tsx`, `MCPInstalledTab.tsx`, `MCPDiscoverTab.tsx`, `MCPCustomServerModal.tsx`, `MCPSetupWizard.tsx` | Installed · Discover, plus status filter chips |
| AI | Prompts | `prompts` | `PromptsSettings.tsx` | — |
| AI | Presets | `presets` | `PresetsSettings.tsx` | Your presets / Built-in / Failed to load |
| AI | Web browser | `browser` | `BrowserSettings.tsx` | — |
| Work | Projects | `projects` | `ProjectsSection.tsx`, `projects/*` | — |
| Work | Branches | `branches` | `BranchesSection.tsx`, `../Worktrees/WorktreesPanel.tsx`, `WorktreeSettings.tsx` | Active · Archived · Cleanup settings |
| Work | Automations | `automations` | `AutomationSettings.tsx` | — |
| Work | Machines | `machines` | `cloud/machines.tsx`, `cloud/machineAccess.tsx`, `cloud/MachineDiskCard.tsx`, `cloud/GrowDiskModal.tsx`, … | list, then detail (`/settings/machines/$id`) |
| Work | GitHub | `git-connections` | `GitConnectionsSettings.tsx` | — |
| Work | Access tokens | `tokens` | `TokenSettings.tsx` | — |
| App | Appearance | `appearance` | `AppearanceSettings.tsx`, `ColorSchemeSelector.tsx`, `ToolCallSettings.tsx` | 14 headings on one page |
| App | Keyboard shortcuts | `shortcuts` | `KeyboardShortcutsSettings.tsx` | 12 categories |
| App | Notifications | `notifications` | `NotificationSettings.tsx` | — |
| App | Privacy | `privacy` | `PrivacySettings.tsx` | — |
| App | About | `about` | `AboutSection.tsx`, `UpdateSection.tsx`, `VersionDetails.tsx` | — |
| App | Developer | `developer` | `DeveloperSettings.tsx` | **Visible in the production web build** (§4) |

### Shell

- `SettingsPage.tsx` → `SettingsViewerTab.tsx`. That renders:
  - the sidebar: `SettingsHeader.tsx` (Back/Close and OrgSwitcher) and `SettingsNavigation.tsx`;
  - `SettingsContent.tsx` (one `max-w-4xl` layout).
- Every section opens with `SettingsPageHeader.tsx` (h1 at 20.6px, required description).
- The sidebar collapses to a 60–96px icon rail when its container is under 600px.
- Up and Down arrows move between sections while focus is in the nav. Escape leaves settings, unless focus is in a field or a modal is open.

---

## 2. What is already good (keep it)

- **URL is the source of truth.** Sections, billing tabs and machine detail are all addressable. Aliases and unknown slugs redirect with `replace` and a notice, and never crash.
- **One layout.** One width and one page-header component for all 20 pages, which replaced five widths and a card-in-card. Measured: every h1 is the same size (20.6px), and every page has a one-sentence description.
- **Delete account is a model destructive flow.** It shows real counts from the server, states what survives, quotes the refund amount, and asks for the email to be typed.
- **No raw palette colours remain in `components/Settings`.** A grep for `text-/bg-/border-(green|red|amber|gray)-NNN` comes back empty.
- **Contrast passes.** The WCAG probe found 0 text below 4.5:1 on every section in dark and light. The probe composites alpha layers and is limited to text nodes.
- **Copy describes the purpose.** Header descriptions say what each page is for: Projects, Automations, Local & custom and Branches are good examples.

---

## 3. Findings per page

Severity: **H** = wrong, broken or misleading; **M** = friction or inconsistency; **L** = polish.

### Information architecture (cross-page)

1. **H — Project-scoped pages sit in a global settings tree, and nothing says which project they apply to.**
   - **Prompts:**
     - `PromptsSettings.tsx`: the **New** button is `disabled` with no tooltip or reason when no project is open (measured `disabled: true`, `title: ""`).
     - The empty state still says "Click New to add your first prompt".
   - **Branches:**
     - `BranchesSection.tsx`, `../Worktrees/WorktreesPanel.tsx:128`: "No project open. Workspaces belong to a project. Open a project to see the workspaces…"
     - There is no project picker and no action. The page cannot be used from a deep link or a refresh, even though the user has a project.
     - The copy also still says "Workspaces" after the rename to Branches.
   - **MCP servers:** `MCPSettings.tsx` "installed servers are available to this project's agents", but the page names no project.
   - So the scope model (global < project < project-local, mentioned in the MCP footer tips) is invisible on the pages it governs.

2. **M — Reliant AI is split across two sections.**
   - AI providers shows "Use $X of Reliant credits" / "Enable" and the Reliant provider row.
   - Billing has a "Reliant AI" tab (`cloud/reliantAI.tsx`) with keys, credit and spend.
   - A user who wants "my Reliant AI keys" has to guess. Inside the Billing tab the copy points to "add credit on the billing page" and "automatic top-ups on the Billing page" (`reliantAI.tsx:286,417`), which is the page they are already on.

3. **M — Machines and Access tokens overlap.**
   - Access tokens (`TokenSettings.tsx`) exists to "let a self-hosted or headless machine connect", and shows a `reliant daemon start --token` recipe.
   - Machines (`cloud/machines.tsx`) has its own three-step "Run Reliant on your own machine" pairing flow.
   - These are two ways to connect a machine, on two pages, in the same group, and neither mentions the other.

4. **M — Several settings are filed under the wrong group.**
   - **Web browser** is in the "AI" group. It controls Reliant's built-in browser (link opening, homepage), which is app behaviour.
   - **Response streaming** is in Appearance (`AppearanceSettings.tsx:518`). The comment there calls it "a chat preference", and it is grouped visually with Editor (advanced).
   - **Presets** and **Automations** are workflow concerns. The Presets description itself says a workflow's presets also live on its page in Workflows.

5. **M — Appearance is a catch-all.**
   - It has 14 headings: theme, colour scheme, file browser hidden files, fonts, chat density, Editor (advanced) with tool-call display, response streaming, spawn display, workflow viewer and code editor.
   - The header says "Pick light or dark mode and a color scheme". It does not say this page also controls the editor's minimap or how spawns render.

6. **M — There is no way to search settings.**
   - 20 sections hold about 150 controls, and the location of items like "show hidden files", "streaming" or "spawn display" cannot be guessed.

7. **L — Naming drifts between nav, h1, phone and copy.**
   - The nav says "MCP servers" and "Keyboard shortcuts", but the h1s say "MCP Servers" and "Keyboard Shortcuts". Every other h1 is sentence case.
   - Panel h3s are Title Case: "Crash and Error Reporting", "Browser Permission", "Font Settings", "Spawn Display", "Linked Accounts".
   - On the phone: "Workspace preferences — Archive, branch, and delete defaults" is desktop's Branches → Cleanup settings. "AI providers — API keys and default model" is fine.

### Account (`AccountSettings.tsx`)

- **M — Sign Out is styled the same as Delete account.**
  - It uses `variant="danger"` (`AccountSettings.tsx:145`), the same red treatment as Delete account directly below.
  - It is guarded by a native `confirm('Are you sure you want to sign out?')` (`:64`).
  - Signing out is not destructive. It should be a secondary button with no confirm.
- **L — Headings are unbalanced.** The "Linked Accounts" h3 renders at 17.8px. Its siblings are 14px.
- **L — Small buttons.** Sign Out and Delete account are `size="xs"`, 23px tall.

### Billing & usage (`cloud/billing.tsx`, `cloud/reliantAI.tsx`, `cloud/usage/*`)

- **M — Engineer-speak leaks into user copy.**
  - On Usage & invoices, measured in the DOM: "Machine runtime measurement window is unavailable; do not assume whole-period cost coverage Approximate hours are display-only; weighted balances are not dollar credit." That is two sentences run together, written for an auditor.
  - Also "Monthly admission budget", "Enforcement", and "Organization usage (select an organization first) is unavailable."
  - The page description: "…machine subscriptions, independent app hosting billing, usage and invoices."
- **M — The Reliant AI tab shows two spend figures in one card.** "AI spend (last 30 days) $0.00" sits next to "Spent this period: $18.40" (`reliantAI.tsx:374–412`). They are different windows from different fields, with nothing reconciling them.
- **M — Density.** The Overview tab stacks credit, coupon, auto top-up form, compute plan and overage controls into one long column. The Usage tab has 9 headings.
- **L — Card titles.** These h3s are 12.2px uppercase eyebrow labels ("AI CREDIT", "COMPUTE"), one rung below every other panel heading.
- **Explicit save buttons** ("Save top-up settings", "Save extra-time settings", "Save spending cap") are correct here, because they change money. Keep them.

### Members (`cloud/MemberPermissions.tsx`)

- **L — Unhelpful empty state.** "No members." gives no explanation and no invite path. The read-only notice ("An organization admin with token:write can change them") is good.
- **L — Naming.** The nav says "Members" but the page is permissions only. Either name it "Member permissions" or add membership management.

### AI providers (`AISettings.tsx`, `providers/*`)

- **M — Internal tabs are not in the URL.** Providers / Models / Local & custom use `useState` (`AISettings.tsx`). You cannot link to "Default models", and a refresh drops back to Providers. Billing solved this already with `?tab=`.
- **M — The tabs are only partly accessible.**
  - They have `role=tab` and `aria-selected`, but no `aria-controls`.
  - The tabpanel has no `id`.
  - Every tab is `tabIndex=0`, so there is no roving tabindex.
  - ArrowRight does nothing (measured).
  - Billing, Branches and MCP each hand-roll their own tab strip. Branches gets `aria-controls` right; AI and Billing do not.
- **Good:** the empty state ("Sign in with Claude / ChatGPT / All providers") and the loading and "could not be loaded / Try again" states are designed.

### MCP servers (`MCPSettings.tsx` and children)

- **M — Empty and help states compete.** An empty install shows six zero-count chips ("All 0 Active 0 Inactive 0 Disconnected 0 Needs Setup 0"), then "No servers match your current view", then four bullet tips. The first-run state should be one call to action: Discover, or Add custom server.
- **L — Chip overflow at 390px.** The filter chip row scrolls horizontally (`sw 374 / cw 298`). That is acceptable, but nothing signals it can scroll.

### Prompts (`PromptsSettings.tsx`)

- **M — Silently disabled action.** The New button is disabled with no explanation (see IA 1).
- **M — Autosave on every keystroke** across title, category and body. "Saving…" appears, but there is no "Saved" confirmation and no error recovery beyond a red box.
- **L — Unlabelled search.** The search input has only a placeholder ("Search prompts…"). The New button is `text-xs`.

### Presets (`PresetsSettings.tsx`)

- Fine. The groups and the "Failed to load" list with file paths and errors are good.
- **L — Copy.** The description runs two sentences into a cross-reference. It could be shortened.

### Web browser (`BrowserSettings.tsx`)

- **M — Mixed save model.** "Open links in Reliant" applies immediately, while "Default homepage" needs an explicit **Save** button. Nothing marks the field as changed but unsaved, and leaving the page loses the edit.
- **L — Label not associated.** The homepage input has a visible "Default homepage" label that is not linked to the field (measured unlabelled; placeholder only).
- **L — Not a setting.** "UI context capture" is a feature explainer with no control. It belongs in docs or as a hint in the browser toolbar.

### Projects (`ProjectsSection.tsx`)

- **Good:** loading, error-with-retry, empty ("No projects yet") and remove-confirmation modal states are all designed. Removal copy says files on disk are never deleted.
- **L — Tiny text.** The remote and branch subline renders at 10.7px ("github.com · main"), which is below the 12px floor.

### Branches (`BranchesSection.tsx`, `WorktreesPanel.tsx`, `WorktreeSettings.tsx`)

- **H — Unusable from a deep link** (see IA 1). The chip says "Project: No project open" beside "Machine: cloud-machine", and the panel offers no action.
- **L — Stale terminology.** "Workspaces" remains in the empty-state copy (`WorktreesPanel.tsx:128`).

### Automations (`AutomationSettings.tsx`)

- **Good:** three radio choices, each with a full consequence description.
- **L — Copy.** Naming "trigger activations" assumes vocabulary the rest of the app calls "automations".

### Machines (`cloud/machines.tsx`)

- **M — Page-level horizontal scroll at 390px.**
  - At 390px the document scrolls sideways (`scrollWidth 617` vs `390`) even though both tables sit in `overflow-x-auto`.
  - The cause is the `sr-only` "Actions" header span. It is `position:absolute` and escapes the scroll wrapper, because the wrapper is not positioned (`components/forge-ui/data_table.tsx:267`).
  - This is a shared component, so every DataTable has the bug.
- **L — Unlabelled filter.** The status filter `<select>` ("All statuses / Pending / …") has no label.
- **L — Tiny text.** Step numbers 1, 2 and 3 render at 10.7px.
- **L — Placeholder value.** "unknown build" appears in the Resources column for a cloud machine. This may be a fixture artefact; check it against real data.

### GitHub (`GitConnectionsSettings.tsx`)

- **M — Native confirm.** Disconnect uses a native `window.confirm` (`:209`).
- **Good:** OAuth is primary, and the PAT fallback is labelled "for debugging authorization problems".

### Access tokens (`TokenSettings.tsx`)

- **M — Overlaps Machines** (see IA 3).
- **L — Placeholder in the example.** The example command uses the literal `"your-token"`. When a token has just been created, the recipe should fill it in.

### Appearance (`AppearanceSettings.tsx`, `ColorSchemeSelector.tsx`)

- **M — Selection state is not exposed to assistive tech.**
  - The Theme **Light / Dark** buttons and the six colour-scheme cards have no `aria-pressed`, `role=radio` or `aria-checked` (measured: all `null`). Selection is shown by colour and border only.
  - The chat-density buttons in the same file do use `aria-pressed`.
- **M — No "System" theme option.** It is light or dark only, and the initial value is inferred from the DOM. The app font select does offer "System Default".
- **M — Catch-all page** (see IA 5).
- **L — Unlabelled select.** The Workflow viewer "Default View Mode" select has a visible label that is not associated with it.
- **L — Novelty option.** "Comic Sans (Fun)" is offered in both font pickers of a professional product.
- **L — Scope of "Reset to defaults".** It sits in the Font Settings header but also resets chat density and workflow viewer mode. It covers more than its location suggests.

### Keyboard shortcuts (`KeyboardShortcutsSettings.tsx`)

- **M — Native confirm.** "Reset all" uses native `window.confirm` (`:529`).
- **L — Unlabelled search.** The search input is placeholder-only.

### Notifications (`NotificationSettings.tsx`)

- **L — Plain loading state.** Loading is a bare "Loading..." paragraph under the header. Other pages are in the same state; there is no shared skeleton.
- **L — Hidden dependent options.** "When to notify" options are hidden until permission is granted. That is correct, but a user who has not granted permission does not know what they will get.

### Privacy (`PrivacySettings.tsx`)

- **H — Inaccurate privacy claim.**
  - The page says "Your code, conversations, and project data are always stored locally and never automatically shared" (`:85`).
  - Reliant is a multi-tenant cloud product. Chats live in the API server's database, and cloud machines hold the code.
  - A privacy page must not overstate this. It is the one place a user reads literally.
- **M — Opting out is styled as a warning.** Turning either toggle off shows an amber "Privacy Mode Active" warning with an alert-triangle icon (`:66`), which treats a legitimate choice as a fault.

### About (`AboutSection.tsx`)

- Fine. The version, update section and links work. "Server unknown" is expected in the harness.

### Developer (`DeveloperSettings.tsx`)

- **H — Visible to every web user.**
  - `SettingsNavigation.tsx:89` has `isDevBuild()`, which returns `true` whenever the app is **not** Electron.
  - So the production web build (this harness, `ENVIRONMENT=review`, prod `vite build`) lists **Developer** to every web user.
  - The page offers "Restart Backend", the mock driver configuration and experimental feature toggles.
  - The forge-UI toggle on this page also gates routes (`routes.tsx` ForgeGate).

### Phone surface (`Mobile/MobileSettingsScreen.tsx`)

- **M — The footer contradicts the list above it.** The footer says "Machines and GitHub are available from the main menu… Manage those on desktop" (`:289`), but GitHub is a row in this same list.
- **M — Deep links lose the section.**
  - Sub-screens are component state, so a desktop deep link (for example `/settings/notifications`) lands on the list. Only billing carries `?section=`.
  - Back from a sub-screen is in-app only; the browser Back gesture leaves Settings entirely.
- **L — Theme in two places.** The Account row says "Identity, theme, sign out", and the Appearance row also owns theme.

### Shell (`SettingsViewerTab.tsx`, `SettingsHeader.tsx`, `SettingsNavigation.tsx`)

- **L — No group labels when collapsed.** When the rail collapses (under 600px) the group labels disappear, leaving 20 bare icons. Two of them, Members and Privacy, previously shared the Shield icon; that is now fixed with Lock.
- **L — Small rail targets.** Collapsed rail items are 44×26.
- **L — Plain loading fallback.** Suspense fallbacks are a centred "Loading settings..." chip (`SettingsViewerTab.tsx`) and "Loading…" (`SettingsContent.tsx`). They work, but they are not designed.

---

## 4. Broken or wrong (fix regardless of redesign)

1. **The Developer section ships to production web users.** `SettingsNavigation.tsx:89-92` (`isDevBuild` returns true off-Electron). It exposes Restart Backend, the mock driver and experimental flags.
2. **The Privacy page makes a false data-locality claim.** `PrivacySettings.tsx:83-87`.
3. **Machines scrolls the whole page sideways at narrow widths.** The cause is in the shared `components/forge-ui/data_table.tsx:267`: the `overflow-x-auto` wrapper is not `relative`, so the absolutely positioned `sr-only` header escapes it.
4. **The phone settings footer contradicts the list.** `MobileSettingsScreen.tsx:289` (GitHub).
5. **Branches and Prompts are inert without an open project, and say nothing useful.**
   - Branches: `WorktreesPanel.tsx:128` has no action, and its copy uses the stale "Workspaces".
   - Prompts: `PromptsSettings.tsx` New is silently disabled.
6. **Reliant AI tab copy points the user to the page they are already on.** `cloud/reliantAI.tsx:286,417`.

---

## 5. Recommended consolidated IA

The current four groups are a real improvement. The remaining problems are scope (global vs project), duplication (Reliant AI, machine pairing) and misfiled items. Proposal, 18 items:

```
Account
  Account              profile, linked sign-ins, sign out, delete
  Organization         members & permissions (cloud)            ← "Members"
  Billing & usage      Overview · Plans · Usage & invoices · Reliant AI
                       (Reliant AI keys/credit live ONLY here; AI providers links to it)
AI
  Providers & models   Providers · Models · Local & custom      (tabs in ?tab=)
  MCP servers          [project scope bar]
  Prompts              [project scope bar]
Workflows
  Presets
  Automation approvals ← "Automations"
Machines & code
  Machines             list · Add machine (pair code OR token) · detail (Access)
                       ← absorbs Access tokens as a tab
  GitHub
  Projects
  Branches             [project scope bar]
App
  Appearance           theme (Light/Dark/System), colour scheme, fonts, density
  Chat & editor        streaming, spawn display, tool-call display, workflow viewer,
                       file browser, code editor            ← split out of Appearance
  Built-in browser     ← "Web browser", moved from AI
  Notifications
  Keyboard shortcuts
  Privacy
  About
  (Developer: dev builds only)
```

Plus:
- **(a)** a settings search field at the top of the sidebar, filtering the section list and a static index of control labels;
- **(b)** a shared **project scope bar** in `SettingsPageHeader`'s `meta` slot, with a project picker, on every project-scoped page.

The phone list should mirror the same names and groups, with `/m/settings/$section` URLs.

---

## 6. Ranked recommendations

| # | Change | Impact | Effort | Files | Concrete change |
|---|---|---|---|---|---|
| 1 | Hide Developer outside dev builds | H | S | `components/Settings/SettingsNavigation.tsx:89` | `isDevBuild = () => import.meta.env.DEV \|\| Boolean(window.RELIANT_CONFIG?.isDev)`. Keep the `SettingsViewerTab` fallback redirect for `/settings/developer` |
| 2 | Make the Privacy copy true; stop warning on opt-out | H | S | `PrivacySettings.tsx:60-95` | Replace the "stored locally" note with an accurate one-liner, e.g. "These switches only control diagnostics and usage analytics. They don't change where your chats and code are stored." Link to the privacy policy. Replace the amber "Privacy Mode Active" alert with neutral helper text, or remove it |
| 3 | Project scope bar on project-scoped pages | H | M | `SettingsPageHeader.tsx` (`meta`), `PromptsSettings.tsx`, `BranchesSection.tsx`, `../Worktrees/WorktreesPanel.tsx:128`, `MCPSettings.tsx` | One `<SettingsProjectScope>` chip with a project picker, defaulting to the current or last project. When none: "Pick a project" select, never a disabled button. Replace "Workspaces" with "branches" in the empty copy. Give the disabled New in Prompts a reason, or remove the disabled state by defaulting the project |
| 4 | One confirm-dialog pattern; Sign out is not destructive | M | S | `AccountSettings.tsx:64,145`, `GitConnectionsSettings.tsx:209`, `KeyboardShortcutsSettings.tsx:529`, `cloud/reliantAI.tsx:235,241` | Replace 5 native `confirm()` calls with the existing app modal (as `ProjectsSection` and `DeleteAccountDialog` do), with a destructive confirm button that names the action ("Disconnect GitHub", "Revoke key"). Sign Out becomes `variant="secondary"` with no confirm |
| 5 | Single home for Reliant AI; fix self-referencing copy | M | S | `cloud/reliantAI.tsx:280-290,370-420`, `providers/ProvidersSection.tsx` | In the Billing → Reliant AI tab, replace "on the billing page" with buttons that switch to `?tab=overview` ("Add credit", "Set a monthly limit"). Show one spend figure per card, with its window named. In AI providers, the Reliant row links to `billing?tab=ai` |
| 6 | Merge Access tokens into Machines → Add machine | M | M | `TokenSettings.tsx`, `cloud/machines.tsx`, `SettingsNavigation.tsx`, `routeSchemas.ts` aliases (`tokens`→`machines?…`) | The "Add machine" flow offers two methods: a pairing code (desktop/CLI) or a token (headless). Token list and revoke become a Machines sub-tab. Add the alias so old links work |
| 7 | Split Appearance; move streaming and browser | M | M | `AppearanceSettings.tsx`, new `ChatEditorSettings.tsx`, `SettingsNavigation.tsx`, `SettingsContent.tsx`, `routeSchemas.ts` | Appearance keeps theme, scheme, fonts, size and density. "Chat & editor" takes streaming, spawn, tool-call, workflow viewer, file browser and code editor. Web browser moves to the App group as "Built-in browser". "Reset to defaults" moves to the page header and resets exactly what that page owns |
| 8 | Shared accessible Tabs; AI tabs in the URL | M | M | `AISettings.tsx`, `cloud/billing.tsx`, `BranchesSection.tsx`, `MCPTabs.tsx`, `routeSchemas.ts` (`settingsSearchSchema.tab` union or per-section param) | One `SettingsTabs` component: `aria-controls`/`id` pairs, roving tabindex, Left/Right/Home/End, `?tab=` binding. Use it on all four strips |
| 9 | Theme selection semantics + System option | M | S | `AppearanceSettings.tsx` (theme buttons), `ColorSchemeSelector.tsx` | Render Theme and Scheme as `role="radiogroup"` with `role="radio" aria-checked`. Add "System" (follows `prefers-color-scheme`), persisted as `system` |
| 10 | De-jargon the Billing usage copy | M | S | `cloud/billing.tsx` (header subtitle, Usage tab), `cloud/usage/AllowanceMeter.tsx`, `EnforcementPanel.tsx`, `DeployUsageSection.tsx` | Subtitle: "Credits, plans, usage and invoices." Rewrite the measurement-window warning as one plain sentence ("Usage for part of this period isn't available yet, so totals may be low."). Rename "Monthly admission budget" and "Enforcement" in user terms ("Monthly hosting limit", "What happens at the limit") |
| 11 | Settings search | M | M | `SettingsViewerTab.tsx`, `SettingsNavigation.tsx`, new static index | A filter input above the nav that matches section labels plus a hand-maintained keyword list per section ("hidden files"→appearance/chat-editor, "streaming", "homepage", "token"…). Enter navigates |
| 12 | Phone: fix the footer copy, URL-backed sub-screens | M | S | `Mobile/MobileSettingsScreen.tsx:289`, `lib/mobileRedirect.ts mobilePathFor`, `routes.tsx` (`/m/settings`) | Drop "and GitHub" from the footer. Read `?section=` for every section, not only billing, and map `/settings/<id>` → `/m/settings?section=<id>`. Rename "Workspace preferences" to "Branches". Remove "theme" from the Account row description |
| 13 | Machines narrow-width page scroll | L | S | `components/forge-ui/data_table.tsx:267` | `className="relative overflow-x-auto"`. Shared component, so coordinate with the shell or brand owner. Fixes every DataTable |
| 14 | Consistent save model | L | M | `BrowserSettings.tsx`, `PromptsSettings.tsx` | Rule: toggles, selects and short text autosave on change or blur, with an inline "Saved" tick near the control. Money and multi-field forms (billing, custom endpoints, tokens) keep explicit Save. So the homepage field saves on blur; Prompts shows "Saved"/"Couldn't save — Retry" |
| 15 | Label every control | L | S | `PromptsSettings.tsx` (search), `KeyboardShortcutsSettings.tsx` (search), `BrowserSettings.tsx` (homepage `htmlFor`), `AppearanceSettings.tsx` (workflow viewer select), `cloud/machines.tsx` (status filter) | `aria-label` on the searches; `id`/`htmlFor` on the labelled field and select |
| 16 | Heading ladder and case | L | S | `MCPSettings.tsx:733`, `KeyboardShortcutsSettings.tsx`, `../LinkedAccounts.tsx` (17.8px h3), `cloud/ui/card.tsx` (12px uppercase CardTitle), Title Case h3s in Privacy/Notifications/Appearance | Sentence-case every h1 and h3. Panel titles all `text-sm font-semibold`. Keep uppercase eyebrows only as non-heading labels |
| 17 | Small polish | L | S | `ProjectsSection`/`projects/ProjectsTable.tsx` (10.7px subline → `text-xs`), `cloud/machines.tsx` step numbers, `MemberPermissions.tsx` empty state, `AppearanceSettings.tsx` (drop "Comic Sans (Fun)"), `BrowserSettings.tsx` ("UI context capture" explainer → docs), `TokenSettings.tsx` (prefill the new token into the example) | As listed |

Not in scope here, because other agents own it: brand tokens (the #0d8cff and #6e39ff adoption, Geist, near-black surfaces) and the global shell. One related note for that owner: the settings nav's active state, `bg-primary/10 text-primary` with a 2px left border, is the only place the accent appears in the settings chrome. It will pick up the brand blue automatically once tokens land.

---

## Top 5

1. **The Developer section is shown to every production web user** (`SettingsNavigation.tsx:89`). It exposes Restart Backend, the mock driver and experimental flags. H, S.
2. **The Privacy page claims data is "always stored locally"**, which is false for the cloud product. The page also warns in amber when a user opts out (`PrivacySettings.tsx`). H, S.
3. **Project-scoped pages (Prompts, Branches, MCP) do not show or let you pick a project.** From a deep link they are inert: New is silently disabled, and Branches says "Open a project" with no way to do it. Fix with a shared project scope bar. H, M.
4. **Consolidate the IA.**
   - One home for Reliant AI (Billing → Reliant AI, whose copy currently says "go to the billing page" from the billing page).
   - Access tokens merged into Machines → Add machine.
   - Appearance split into Appearance plus "Chat & editor".
   - Web browser moved out of the AI group.
   - A settings search added.
   M, M.
5. **One interaction grammar across settings.**
   - Replace 5 native `confirm()` calls with the app's modal, and make Sign out non-destructive.
   - A shared accessible Tabs component with `?tab=` URLs (the AI tabs are not addressable, and arrow keys do nothing).
   - Radio semantics for Theme and Scheme, plus a "System" theme.
   - One autosave or explicit-save rule.
   M, S–M.
