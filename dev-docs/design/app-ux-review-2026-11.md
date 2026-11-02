# App UX review against the new site (2026-11)

The Reliant app (`frontends/web`, which is also the Electron renderer) reviewed
against the new reliantlabs.io landing page (landing-page `main` d6f49ac). The
review also covers the frontend forge scaffolds for generated apps.

- **Scope:** recommendations only. No product code was changed.
- **App build reviewed:** reliant `origin/main` 0ec4b8119. reliant #1844 is merged and included, so the onboarding plan step shows S1–3XL.
- **Forge build reviewed:** forge `origin/main` 6d6ffcf96.
- **Review dates:** 2026-11-01.
- **Reviewers:** six area reviewers plus one lead. The lead built the harness, covered brand and shell, and wrote this synthesis.

| Part | File |
|---|---|
| Brand tokens, type, global shell | [00-brand-and-shell.md](app-ux-review-2026-11/00-brand-and-shell.md) |
| Settings | [01-settings.md](app-ux-review-2026-11/01-settings.md) |
| Chat, agent runs, sidebars | [02-chat-and-sidebars.md](app-ux-review-2026-11/02-chat-and-sidebars.md) |
| Apps, deploys, observability console | [03-apps-observability.md](app-ux-review-2026-11/03-apps-observability.md) |
| Onboarding, billing, plans, machines | [04-onboarding-billing-machines.md](app-ux-review-2026-11/04-onboarding-billing-machines.md) |
| Workflows, automations, integrations | [05-workflows-automations-integrations.md](app-ux-review-2026-11/05-workflows-automations-integrations.md) |
| Forge-scaffolded app UI (forge repo) | [06-forge-scaffold.md](app-ux-review-2026-11/06-forge-scaffold.md) |
| How the fixture harness works | [HARNESS.md](app-ux-review-2026-11/HARNESS.md) |

The area files have the full evidence: measurements, `file:line` references,
and every lower-ranked item. This document dedupes them and ranks them
together.

**How this was verified: measured, not seen.**
- **No reviewer saw pixels.** `take_screenshot` returned a text placeholder instead of an image (bug 3f6a8b8e).
- Saving to a file did not help either. The DevTools MCP refused paths under the research directory. A PNG saved under `/tmp` opened as metadata only.
- **What the findings rest on instead:**
  - the DOM and the accessibility tree;
  - computed styles;
  - WCAG contrast computed in the page, with alpha layers composited onto the real background;
  - overflow and target-size probes at 1905, 1440, 1280, 1024, 800 and 390 px;
  - real keyboard events;
  - the code.
- **The owner should judge the visuals on the live preview (below).**

## Executive summary

**The app is better built than it looks, and it looks like a different
company's product.**
- **Structure is sound.**
  - Density, elevation rules and dark-first surfaces are solid.
  - Every price is server-sourced and correct against the new price list.
  - Contrast mostly passes AA. Three contract tests hold it in every color scheme.
  - Many empty and error states are designed rather than inherited.
- **Nothing visible says Reliant.**
  - The default scheme is pure-black monochrome, with a near-white primary button.
  - The UI font is Inter.
  - The Reliant mark never appears after sign-in.
  - Blue appears only where a component hard-codes it.
- **The site, by contrast,** is a near-black blue-tinted canvas in Geist, with brand blue for action and focus and violet for labels.

**Closing the brand gap is mostly token work, in two steps.**
1. **Cheap, this week:**
   - bundle Geist (this also fixes an Appearance option that is broken today);
   - put the mark in the sidebar;
   - replace the onboarding CTA, which fails AA contrast;
   - remove two rainbow gradients;
   - sentence-case the shell labels;
   - adopt one focus ring.
2. **One real project:** a new **Reliant** color scheme as the default.
   - It needs one prerequisite. Today `--primary` is both the fill color and the text color, at roughly 1,600 call sites.
   - In brand blue, `text-primary` fails AA on dark surfaces (3.9:1).
   - So split out a `--primary-ink` token before switching (see [Brand tokens](#adopting-the-landings-brand-tokens)).

**The larger findings are about trust and positioning, not paint.** The site
says "You set the direction. Your agents do the work." The app most often
fails at the two moments that line describes.

- **Direction: what the user is asked to approve.**
  - An agent-made automation's approval shows as one truncated line with a green Approve button beside it.
  - The full card is a raw key/value dump with JSON and the user's own UUID.
  - Approve-all is one chord.
- **Work: what the agent is doing right now.**
  - Running tool calls are collapsed, while finished diffs are expanded.
  - A failed chat is painted green, like a running one.
  - The deploy and observability console's empty states offer a CLI command instead of "deploy with an agent".
  - "Ask Reliant" is missing exactly where things break: a crash-looping workload, or a failed release.

**Several things are plainly broken.**
- **Settings:**
  - the Developer section ships to every production web user;
  - the Privacy page claims chats and code are "always stored locally", which is false for the cloud product.
- **Chat list:** "Delete" archives a chat, but the trash icon on an archived row permanently deletes it, in one click, with no undo.
- **Plans:** an existing subscriber cannot change plan. Every route leads to a dead-end paragraph.
- **Workflows:** the list's Name column collapses to 0 px below about 1060 px.
- **Dashboards:** one malformed panel can blank the whole app.

**In-app pricing vs the new pricing.**
- **Machines are correct.** All seven sizes, S1–3XL at $25–$489, with 160 included hours and $0.20–$3.80 overage, render exactly as on the site.
- **Hosting disclosure is wrong:**
  - "7.0 cents / vCPU-hour" instead of $0.07;
  - storage as "0.0342465753424658 cents / GiB-hour";
  - egress without its 10 GB free tier;
  - the $5 monthly minimum is missing, under a card that says "no base fee";
  - egress and extra static sites are absent from usage attribution.
- **Hours:** one account shows three different hour units for the same 62-of-160 hours.

## Preview

Production `vite build` of 0ec4b8119. Every request is answered in the page
by invented fixtures, and no backend is involved.

**https://5315-2aab1465-f76b-4d95-b61b-480d9ba070e6.workspaces.reliantapi.com/**

Signed in as the fictional "Alex Demo" with the project `shopfront`. A small
corner badge marks the fixture data; add `?fxbadge=0` to hide it. Switches
are read on page load, so reload after changing one. Add `?fx=clear` to
reset.

| Look at | URL |
|---|---|
| Shell, chat list in every state, a long agent run | `/` and open "Refactor cart totals and fix multi-quantity discounts" |
| First run, the machine step (S1–3XL) | `/onboarding?fx=onboarding`, then **reload** |
| Coupon-covered first run | `/onboarding?fx=onboarding,coupon` |
| Billing, M subscriber; plan change dead end | `/settings/billing`, then the **Change plan** tab, then pick L |
| Hosting price disclosure | `/settings/billing?tab=usage` |
| Machines (capacity and provisioning states) | `/settings/machines?fx=capacity,provisioning` |
| Workflows list; shrink the window below 1060 px | `/workflows/library` |
| Inbox: an activation approval | `/inbox` |
| Apps; the console's empty states | `/apps`, then `/forge/p/shopfront` |
| Sign-in screen | `/auth?fx=signed-out` |
| Light mode, or another scheme | add `?theme=light`, or `?scheme=blue` (`black` is the default) |
| Phone | a phone-width window redirects to `/m/...` |

The forge-scaffolded sample app (area 06) runs separately in mock mode at
`https://5321-2aab1465-f76b-4d95-b61b-480d9ba070e6.workspaces.reliantapi.com/`.

## Top 15 for the app (reliant), ranked by impact for the effort

Effort scale: S is up to half a day, M is one to three days, L is longer.

Each item is merged from the areas named in brackets. The items are ordered
by impact for the effort. Within the same ratio, trust-breaking problems come
first.

### 1. Settings: stop showing Developer to web users, and make the Privacy page true

H impact, S effort. [01]

**Developer section.**
- **Where:** `components/Settings/SettingsNavigation.tsx:89`.
- **Problem:** `isDevBuild()` returns true whenever the app is not Electron. So the production web build lists **Developer** to every user, with Restart Backend, the mock driver and experimental flags.
- **Fix:** `isDevBuild = () => import.meta.env.DEV || Boolean(window.RELIANT_CONFIG?.isDev)`. Keep the redirect for a deep link to `/settings/developer`.

**Privacy page.**
- **Where:** `PrivacySettings.tsx:83-87`.
- **Problem:** it says "Your code, conversations, and project data are always stored locally and never automatically shared".
- **Fix:**
  - Replace it with an accurate line: "These switches only control diagnostics and usage analytics. They don't change where your chats and code are stored." Link it to the privacy policy.
  - Drop the amber "Privacy Mode Active" warning that appears when the user opts out.
  - **The final wording is the owner's call** (legal).

### 2. Destructive chat actions say what they do

H impact, S effort. [02]

**Problems.**
- The active row's context menu item is labelled **Delete**, but it archives, with no undo.
- On an archived row, the hover trash icon calls the same mutation. That **permanently deletes** the chat in one click, with no confirmation and no undo.
- Rename failure uses `window.alert`.

**Fix.**
- Rename the active-row item to **Archive**, with the same Undo toast as the hover button.
- On archived rows, use **Delete permanently…**, behind forge's `confirmation_dialog`.
- Give it its own mutation, so the verb cannot flip with server state.
- Turn the rename failure into a toast.

**Files:** `Layout/Sidebar.tsx` (`getChatContextMenuItems`, `ArchivedChatItem.handleDelete`), `hooks/chat-queries.ts`, and `internal/handlers/chat/chat_crud.go` `DeleteChat`.

### 3. Green means success only, and status is said in words

H impact, S effort. [02, 03, 04, 05, 00]

| Surface | Today | Fix | Files |
|---|---|---|---|
| Chat list rows | A **failed** chat gets the same green `bg-success/5` row as a running one: any non-null `runStatus` picks the green row (`Sidebar.tsx` ~323) | Neutral row plus a dot when running; a "Needs you" chip on a warning tint; a destructive dot plus a "Failed" chip | `Sidebar.tsx`, `lib/runStatus.ts` |
| Tool rows | A failed `npm test` says **Warning**; every finished row carries a "Completed" pill | "Failed"; no pill on success | `Chat/ToolExecution.tsx` ~712 |
| Approval buttons | Approve is a green fill and Deny a red fill, on every Inbox row and in chat | Approve uses the primary (brand) fill, Deny is outline. Name both after their subject | `Chat/ApprovalActions.tsx`, `inbox/InboxItem.tsx` |
| Machines | Suspended is amber; an offline laptop is red | Neutral dot with words; red only for `failed` | `Settings/cloud/machines.tsx:161-168` |
| Right sidebar | Raw green, yellow and blue dots: in-progress shows green | Status tokens, plus the label in the accessible name | `FileBrowser/RightSidebar.tsx` ~569-622 |
| Onboarding | "Free" is `text-emerald-500` | `text-foreground` | `ComputeStep.tsx:645` |
| Charts | Series 2 uses `--secondary`, a surface tint at 1.1–1.3:1 against cards, so it is **invisible**. Series 3 is green and series 5 is red | Dedicated `--chart-1…6` tokens: 1 = brand blue, 2 = violet, no green or red | `charts/UPlotChart.tsx:117`, `Metrics/ExplorerChart.tsx:44`, `waterfall/TraceWaterfall.tsx:80`, plus a contract test |

Use the same vocabulary on desktop and mobile. Today the same chat reads
"Failed" on desktop and "Needs you" on mobile.

### 4. Pricing in the app reads like the price list

H impact, S effort. [04]

**Hosting rates as dollars, in the public units.**
- Show "$0.07 per vCPU-hour" and "$0.012 per GiB-hour".
- Show "$0.25 per GiB-month". Convert the hour rate in the client, or have the server send a monthly rate.
- Show "$0.15 per GB after 10 GB free each month".
- Show "$5 per extra static site per month (first site free)".
- Add the **$5 monthly minimum**, and replace "no base fee".
- **Files:** `usage/UsageBillingPanel.tsx:49`, `usage/UsageBillingCard.tsx:41`.

**One hour unit.**
- Show "62 of 160 M-hours used · ≈ 98 h left" everywhere.
- Drop "equivalent h at plan size", "weighted h", the "PRICING VERSION" stat and the "display-only" disclaimer.
- **Files:** `Billing/exactMachineUsage.ts:17`, `billing.tsx:1495-1545`, `Mobile/MobileBillingScreen.tsx`.

**Size labels, not wire names.**
- Write "Runs S1, S2, M", not "s1, s2, m". In the overage list, show only the sizes the plan can run.
- Use the plan name "M", not "Compute M".
- Write "160 hours", not "160.0".
- **Files:** `overview/ComputeBand.tsx:171`, `ComputeOverageControl.tsx:135`, `MachineSaleConsent.tsx:32`, `CheckoutStep.tsx:284`, `machines.tsx:1234`.

**Pickers show what the site shows.**
- Add a second line: "160 h included · then $0.30/h · 50 GB disk".
- Lead the spec with vCPU and say "dedicated".
- Show whole dollars when the amount is a whole number ("$39/mo").
- **Files:** `Billing/PlanTiles.tsx`, `machineSpecs.ts:77-81`, `billingUtils.ts:522`.

Item 13 in the next tier, bringing egress and static sites into hosting usage, belongs with this item.

### 5. Shared table layout: no zero-width columns, no page-level sideways scroll

H impact, S effort. [05, 01]

**Workflows list.**
- **Problem:** six fixed-width columns take 767 px, and Name gets what is left. Measured Name width: 228 px at 1280, **0 px** at 1024 and 800. Every workflow name has `clientWidth` 0.
- **Fix:**
  - give Name a floor of `min-width: 16rem`;
  - below about 1100 px of container width, fold Next run and Health into secondary lines with a container query.
- **Files:** `workflows/list/WorkflowRow.tsx`, `WorkflowsListPage.tsx`.

**Every DataTable.**
- **Problem:** the `sr-only` "Actions" header is absolutely positioned, and its `overflow-x-auto` wrapper is not positioned. So the header escapes the wrapper, and the whole page scrolls sideways at 390 px (Machines measured 617 px vs 390 px).
- **Fix:** `relative overflow-x-auto` in `components/forge-ui/data_table.tsx:267`.
- This is a forge component library primitive, so it also needs fixing in forge.

### 6. Make the agent's live work the strongest thing in a run

H impact, S–M effort. [02]

**Expand running tool groups and collapse finished ones.**
- **Where:** `Chat/ChatMessage.tsx` `renderToolRun` passes `defaultCollapsed={true}` for any group of two or more tools, whatever their status.
- **Fix:**
  - set `defaultCollapsed={!runHasLiveCall}`;
  - collapse finished `edit` diffs to a `+2 −1` summary;
  - keep failed rows open.

**Tool rows name the action and the object, with timing.**
- Examples: "Edited totals.ts +2 −1", "Ran `npm test` · failed · 4.1s", "Spawned **reviewer**: Review discount fix".
- **Files:** `lib/toolFormatters.ts`, `ToolExecution.tsx`.
- The data is already there in `startedAt` and `completedAt`.

**Other changes.**
- The thinking footer names what is running ("Running 2 tools · 1m 04s"), not "Still working — no response yet".

### 7. Brand quick wins

H impact, S effort. [00, 04]

1. **Geist and Geist Mono as the default faces.**
   - Bundle them in `public/fonts/`, add `@font-face` and preload, and point `--font-sans` / `--font-mono` at them (`index.css` 416-417 and 593-645, `index.html`).
   - Keep Inter as an option.
   - Keep JetBrains Mono for the terminal and editor until Geist Mono's box-drawing coverage is checked.
   - This also **fixes the broken "Geist" font option**, which is selectable today but falls back to the system font because no Geist face is bundled.
2. **The two-color mark at the top of the sidebar,** linking home.
   - It appears today only on `ApiKeyLogin` and `ProxyAuth`.
3. **Onboarding CTA.** `bg-sky-600` with white text measures about **4.0:1, which fails AA** (`ComputeStep.tsx:960`). Use the brand fill `#0a70e0` (4.8:1).
4. **Remove the rainbow gradients:**
   - the onboarding progress bar's emerald→cyan→sky→violet (`OnboardingFlow/ProgressBar.tsx:27-49`);
   - the builder chat's `from-blue-500 to-purple-500` with `hover:scale-105` (`workflow/WorkflowEditorChatPanel.tsx:85`).
5. **Sentence-case the shell labels:**
   - the sidebar's `ALL PROJECTS` and project headers (`Layout/Sidebar.tsx:655-658`);
   - mobile settings' `YOU` / `PREFERENCES`;
   - billing's `AI CREDIT` / `COMPUTE` card titles.
6. **One focus ring:** `ring-2 ring-ring ring-offset-2 ring-offset-background`, replacing the mix of `ring-ring/40` and white outlines.
7. **"New chat" becomes the shell's one filled primary button.**
   - Today it has the same weight as Apps, Inbox and Workflows.
   - Drop the duplicate Settings gear from the header.

### 8. Never show a wrong or blank state

H impact, S effort. [02, 03]

| Problem | Fix | Files |
|---|---|---|
| A dashboard panel with no `series` throws, and the app-wide error boundary blanks everything | Wrap each panel in an error boundary ("This panel couldn't render · View spec · Remove"), and guard with `(panel.series ?? [])` | `Dashboards/DashboardView.tsx`, `Dashboards/drill.ts:58-59` |
| While the chat list is loading or has failed, the sidebar says "No active chats — Create your first chat" | Skeleton rows while pending; an inline "Couldn't load chats · Retry" on error; the empty state only on a successful empty answer | `Layout/Sidebar.tsx` ~783 |
| The console says "Nothing deployed" for an app the Apps list calls live, when the live view is empty or failing | Say "prod is registered; waiting for its first observation", or "Couldn't read the live view · Retry" | `Console/ConsoleLayout.tsx`, `Workloads/OverviewHome.tsx` |

### 9. Approvals show what they grant, and no reflex approves power

H impact, M effort. [05, 02]

**Inbox.**
- **Problem:** an activation approval is one `truncate` line (384 of 8080 px visible), with green Approve beside it and no way to expand.
- **Fix:** make approvals expandable, open activation approvals by default, and put Approve **under** the disclosure.
- **File:** `inbox/InboxItem.tsx`.

**The activation card.**
- **Where:** `internal/handlers/trigger/activation_agent.go:328`.
- **Problem:**
  - it prints `Operation: create_trigger_activation`, the user's own UUID, raw JSON, and "Executable node kind call_llm: 2";
  - for a no-machine activation it prints `Machine: No machine (server-only) (id )`.
- **Fix:**
  - send structured `disclosure` lines (label, value, `widens`) in `approval.proto`;
  - render them in a new `ActivationApprovalCard`, used by both the chat and the Inbox;
  - lead with one sentence: "Turn on PR review in shopfront. It reviews pull requests in acme/shopfront and posts reviews as Reliant bot."

**Batch approval.**
- **Where:** `Chat/PermissionsPanel.tsx`.
- **Problem:** the chat's approval bar approves everything pending with one click, or with `⌘↵` from anywhere.
- **Fix:** when any pending approval widens power, list approvals one by one. Keep Approve all for routine tool calls only.

**Escape stops a run.**
- **Problem:** Escape stops a 20-minute run from inside the composer, with no confirmation.
- **Fix:** stop only when the composer is empty, or on a second Escape. Show "Approved N · Undo" after a chord approval.
- **File:** `config/shortcuts.yaml`.

### 10. Keyboard and screen-reader parity in the main loop

H impact, M effort. [02, 01]

**Chat list rows.**
- **Problem:** rows are `<div onClick>`. Measured: 0 of 7 are focusable.
- **Fix:** rows become buttons or links, with roving Up/Down, `aria-current`, and `role="list"`.

**Hover-only controls.**
- **Problem:** 21 of the sidebar's 42 tab stops are `opacity-0` buttons named only by a tooltip.
- **Fix:** add `aria-label` and `group-focus-within:opacity-100`.

**Tool-group headers.**
- **Problem:** they are mouse-only.
- **Fix:** copy `ToolExecution`'s `role="button"`, Enter/Space and `aria-expanded`.
- **Files:** `ToolExecutionGroup.tsx` ~191, `ToolExecutionCollapsibleGroup.tsx` ~101.

**Right-sidebar tabs.**
- **Problem:** they are `tabIndex=-1`, are not tabs, and clicking the active tab closes the panel.
- **Fix:** use a real `tablist` with arrow keys.
- **File:** `FileBrowser/RightSidebar.tsx` ~503-655.

**Settings tabs.**
- **Problem:** the AI, Billing, Branches and MCP tab strips are hand-rolled.
- **Fix:** one shared accessible Tabs component, bound to `?tab=`.

**Theme and scheme pickers.**
- **Fix:** give them `role="radiogroup"`, and add a **System** theme.

**Composer.**
- **Problem:** six controls are 23 px tall with no focus outline, and the textarea has no accessible name.
- **Fix:** 28 px targets, a focus ring, `aria-label="Message the agent"`, and one "Options" chip below 640 px. The composer takes 206 px of an 844 px phone today.

### 11. Money surfaces speak product, not audit

H impact, M effort. [04, 01]

**Checkout and consent.**
- **Where:** `Billing/MachineSaleConsent.tsx`, `MachinePricingReadinessCard.tsx`.
- **Problem:** the card form appears only after a 13 px unstyled native checkbox that reads "I accept version machine-pooled-v1 … This is not next-period acceptance for an existing subscription."
- **Fix:**
  - one summary line: "M machine · $39/month · 160 hours · 50 GB disk · $0.30/hour after";
  - a styled checkbox: "I agree to the machine pricing terms", with the version behind a details link.

**False "not approved" card.**
- **Problem:** the Change plan tab tells every subscriber "These are proposed terms, not an approved live offer…". The prices are approved and live.
- **Fix:** delete the card.

**Payment step with no way out.**
- **Problem:** when pricing readiness errors or is held, the onboarding payment step has no way forward.
- **Fix:** offer "Use your own computer instead (free)".
- **Files:** `MachineSaleConsent.tsx:18`, `CheckoutStep.tsx`.

**Engineering notes shown to customers.**
- "Delivery has not been live-tested" (Resend), in `ObservabilityLimits.tsx:112`.
- "Treat your services as potentially restricted" shown to an org that may not host anything, in `EnforcementPanel.tsx:69`.
- Raw ISO periods, in `UsageView.tsx:86`.
- "Machine runtime measurement window is unavailable; do not assume whole-period cost coverage…", on the Usage tab.
- "We need this to intercept your credentials", in `OAuthHelperPanel.tsx:230`.
- "Have a coupon? … it covers this in full", which is false for a partial coupon. It appears in four places.

### 12. Unblock plan changes

H impact, M effort, plus an **owner decision**. [04]

**Problem.**
- Change plan → L, Add machine → bigger size, and the Resize modal's "See plans" all end on the same paragraph: "This organization has an existing machine period. Proposed changes require the separate next-period terms acceptance in Billing…".
- There is no button.
- control-plane sets `effectivePeriodStart` for every live subscription.
- The client has no plan-change RPC. The only purchase path is `CreateCurrentUserComputeSubscriptionIntent`.

**Decision needed:** a prorated upgrade now, or a switch at the next period.

**Interim fix:** an honest path ("Plan changes take effect at your renewal on Nov 19", with a button that records it), and hide the links that lead nowhere.

### 13. "Your agents do the work" where the work happens

H impact, M effort. [03]

**Empty states.**
- **Where:** every console empty state uses `Forge/GettingStarted.tsx`: Overview, Releases, every observability view, Dashboards, Alerts, Environments and Frontends.
- **Problem:** each shows `forge env deploy <env>` and no button.
- **Fix:**
  - give `GettingStarted` a primary `action`: "Deploy with an agent", which opens a chat;
  - keep the command as the secondary path;
  - spell it `reliant forge env deploy` everywhere.

**Ask Reliant where things break.**
- **Where it is missing:**
  - a crash-looping workload's banner (`WorkloadDetail.tsx`);
  - the Overview's Needs attention rows;
  - a FAIL release verdict (`ReleaseComparison.tsx`, `ReleaseDetail.tsx`);
  - a failed deploy or migration job (`DeployJobPanel.tsx`);
  - a session with errors.
- **Fix:** add the subject kinds to `askReliantPrompt.ts`, and render Investigate / Fix with agent in each of those places.

### 14. A "Reliant" color scheme, made the default

H impact, M–L effort. [00]

Built from the landing's tokens. The full mapping table is in the
[brand section](#adopting-the-landings-brand-tokens).

**Order matters.**
1. **First, split `--primary-ink` off `--primary`.**
   - Codemod the 504 `text-primary` uses.
   - Demote the 277 `bg-primary/NN` selection tints to ink washes.
   - Add a contrast contract test.
2. **Then add the scheme.**
   - Files: `themes/professional-themes.css`, `ColorSchemeSelector.tsx`, `lib/forgeTokens.ts`.
   - Move the default in three places: `index.html`, `services/settingsSync.ts:401` and `hooks/useThemeInitialization.ts:61`.
3. **Then re-baseline** `e2e/color-schemes.spec.ts`.

Pure Black stays as the OLED option.

**Owner decision:** whether the brand scheme becomes the default for existing users, or only for new users.

### 15. Settings → Integrations, so a connection can be found and revoked

H impact, M effort. [05]

**Problem.**
- `connection.proto` already has List, Rename, SetDefault, Test, Delete and ListConnectionEvents.
- The UI uses them only inside `ConnectionPicker`.
- Nothing calls `DeleteConnection`.
- So a Slack connection made from a builder dialog cannot be seen again, audited or revoked.

**Fix:** a page that lists connections by integration, with:
- account, status, default and last used;
- "Used by N automations";
- Rename, Make default, Test, and Remove (with a confirmation that names the automations which lose it).

**Files:** `Settings/SettingsNavigation.tsx`, a new `Settings/IntegrationsSettings.tsx`, `hooks/connection-queries.ts`.

### The next tier, in rough order

Effort is in brackets: S, M or L.

1. **Ready-made automations strip** (M). Present the Autopilot pack as a pack, with user-facing one-line summaries instead of 120-word author docs. Hide building blocks from the automation dialog's workflow picker. [05]
2. **Project scope bar** (M) on Prompts, Branches and MCP. From a deep link these pages are inert today: Prompts' New button is silently disabled, and Branches says "Open a project" with no way to do it. [01]
3. **Plain-language refusal reasons** for trigger activations (S). Keep the reason after approval: today `activation_tools.go:235` replaces it with a generic sentence. [05]
4. **Live sub-agent strip** under the chat header (M). Spawned threads have no tab today. Use a fixed thread palette, replacing hashed hues. Fix the invalid `${hsl}10` tab background and the 6 px label. [02]
5. **Settings IA** (M):
   - one home for Reliant AI;
   - Access tokens folded into Machines → Add machine;
   - Appearance split into "Appearance" and "Chat & editor";
   - Web browser moved out of the AI group;
   - settings search;
   - one confirm-dialog pattern, replacing five `window.confirm` calls;
   - Sign out no longer styled as destructive. [01]
6. **Machine states** (S):
   - a first-class "Waiting for capacity" state with "Choose a smaller size" and "Cancel";
   - the size name in the Resources column;
   - prices in the Resize modal. [04]
7. **Coupon clarity** (S). Say what the coupon added ("100 hours on an M machine, one-time"), and add `aria-invalid` / `role="alert"` on errors. [04]
8. **Observability console** (S each):
   - a 12 px type floor (`text-2xs` is used 143 times in `components/Forge`);
   - short release identifiers;
   - a resting underline on prose links (in pure-black they are indistinguishable from body text, which fails WCAG 1.4.1);
   - a grouped Observability nav;
   - org Metrics mounting the real explorer, instead of a "lands here" placeholder. [03]
9. **Runtime health on the Apps list** (M). Today "Healthy" means converged even with alerts firing. Add alert ↔ release correlation on alert cards. [03]
10. **Managed Postgres surface** (L). There is no database row, page, backup or connect info anywhere. Check against the in-flight `feat/dashboards-postgres-panels`. [03]
11. **Cost where you run things** (M). Show a per-app "this month" figure. [03]
12. **Builder canvas for non-authors** (M). Use catalog display names instead of `GITHUB/PR.REVIEW.CREATE@1`, collapse CEL previews, and turn router branches into edge labels. [05]
13. **Egress and static sites in hosting usage attribution** (M). Today `usage/usageModel.ts:62-70` says "nothing measures them", which is no longer true. [04]

## Adopting the landing's brand tokens

The full analysis is in [00-brand-and-shell.md](app-ux-review-2026-11/00-brand-and-shell.md).
The essentials follow.

**What maps to what (dark).** HSL triplets, the format of
`professional-themes.css`.

| App token | Landing | Value |
|---|---|---|
| `--background` | `canvas` #0a0a0f | `240 20% 4.9%` |
| `--card` | `panel` #111118 | `240 17% 8%` |
| `--popover`, `--surface-modal` | `panel.raised` #18181f | `240 13% 10.8%` |
| `--muted` / `--accent` / `--secondary` (hover and selected only) | none | `240 11% 14%`. It must stay lighter than `--card` in dark mode |
| `--foreground` | `fg` #f4f4f5 | `240 5% 96%` (17.1:1 on a panel) |
| `--muted-foreground` | `fg.muted` #9d9da6 | `240 5% 63%` (7.0:1) |
| `--border` | 10% lavender-white hairline, composited onto a panel | `234 12% 16.5%` |
| `--input` / `--config-input-border` | `hairline.strong` | `234 9% 21%` |
| `--primary` / `--primary-hover` / `--primary-foreground` | blue 600 / 700 / white | `211 92% 46%` / `212 91% 40%` / white (4.8:1) |
| `--ring` | blue #0d8cff | `209 100% 52.5%` (5.6:1) |
| `--info` (live and running) | blue-300 #5cb0ff | `209 100% 68%` |
| new `--primary-ink` (blue text) | blue-300 (dark), #0961c4 (light) | 8.1:1 on a panel; 5.7:1 on #fafafa |
| new `--brand-violet` (eyebrows only) | violet-300 #a58cff | 7.0:1 |
| new `--chart-1…6` | blue, violet, then matched-L/C neutrals; no green or red | |
| `--success` / `--warning` / `--destructive` | unchanged | green stays success-only |

Light mode is derived, because the site is dark-only: keep the current
light ramp with a hue-240 tint, and use `--primary #0a70e0`.

**Where the `web-styling` contract needs updating**
(`.reliant/skills/web-styling/SKILL.md`):

1. **Brand roles.**
   - Blue is for action, links, focus and live state.
   - Violet is for labels only, never interactive.
   - Blue→violet is for one brand moment per screen.
   - Green is for success only.
   - Charts and syntax highlighting use data tokens.
2. **Raw palette classes are a violation outside data visualization.**
   - Today 55 files use raw `blue/purple/violet/green-NNN` classes, and `.tsx` files hold 129 hex literals.
   - Add a lint rule that warns first.
3. **Eyebrows vs labels.** `forge-ui/card.tsx` says never `uppercase tracking-wider`, while the landing uses it for violet section eyebrows.
   - Inside panels, labels are sentence case.
   - At most one violet eyebrow, and only on marketing-like surfaces: onboarding, first-run empty states, the plan picker.
4. **One focus recipe.**
5. **The elevation rule is unchanged, and the new scheme must keep it.**
   - The landing's `inset` (#06060a, darker than the page) maps to the app's existing "an inset is `bg-background`" rule. It must **not** become a new darker well token.
   - The `bg-muted` trap still holds: `--muted` must stay above `--card` in dark mode.
   - Do not import the landing's legacy `surface.50–400` ramp.
6. **The accent collision.** Outside `.forge-ui`, reliant's `--accent` is a muted tint.
   - A blue primary makes an unscoped forge component look less wrong, which makes the bug harder to spot.
   - The scoping stays mandatory.

**Risks.**
- **The `text-primary` split (item 14) is the one that bites.**
  - Without it, blue spreads to roughly 1,600 call sites.
  - Blue body text fails AA.
  - To see the spread today, open the preview with `?scheme=blue`: on the billing page, 13 elements paint in primary.
- **Chat's parallel palette.** `index.css` ~415-439 defines a separate `--chat-*` palette in literal hex. It must move to tokens or it will not follow the scheme [02].
- **Snapshots.** `e2e/color-schemes.spec.ts` must be re-baselined, and the image diff reviewed.
- **Radius.** Keep `--radius .5rem` for app controls. The landing's `rounded-full` and `rounded-2xl` are marketing-scale; Vercel's own dashboard keeps 6–8 px controls. Use `rounded-xl` only for hero-like panels: onboarding, plan cards, the "Build an app" card and empty states.
- **Type.**
  - After the Geist swap, move list content (chat titles, nav rows) from `text-xs` (12.2 px; 2,603 uses) to `text-sm`.
  - Use one heading rung (`PageHeader`, `tracking-tight`) instead of the 14 different `<h1>` class strings in use today.

## Quick wins vs larger projects

**Quick wins.** Each is S effort, with no product decision needed:
- top-15 items 1–5, 7 and 8;
- Settings: native `confirm()` calls → the app modal; Sign out styled as non-destructive; System theme; labels on unlabelled inputs [01];
- Chat: user messages collapse at 5–6 lines; "77%" instead of a bare "77" in the header; one "Untitled chat" label; `<time>` with the full date on hover [02];
- Workflows:
  - one On/Off vocabulary, replacing "Activate" on the builder rail;
  - orphan triggers of a workflow that failed to parse are merged into its "Failed to load" entry;
  - YAML modal labels;
  - "Default_repo" capitalization;
  - the Inbox age mismatch [05];
- Console:
  - short release identifiers;
  - the 12 px floor;
  - a resting underline on links;
  - org Metrics using the explorer;
  - the React Flow badge (1.4:1);
  - UCUM units;
  - funnel step labels [03];
- Billing:
  - coupon copy and ARIA;
  - capacity state;
  - neutral machine status colors;
  - remove the observability engineering notes;
  - sign-in brand line "Build it. Ship it. Run it." [04].

**Larger projects:**
- the Reliant scheme with the `--primary-ink` split (14);
- approvals disclosure and the activation card, which changes the server proto (9);
- keyboard parity in the main loop (10);
- money surfaces copy and consent redesign (11);
- plan changes, which needs a backend decision (12);
- "deploy with an agent" and Ask Reliant everywhere (13);
- Settings → Integrations (15);
- the Settings IA consolidation;
- a live sub-agent strip;
- runtime health and cost on the Apps list;
- the managed Postgres surface;
- the builder canvas for non-authors.

## Broken: fix regardless of design direction

| # | What | Where | Area |
|---|---|---|---|
| 1 | Developer settings listed to every production web user | `Settings/SettingsNavigation.tsx:89` | 01 |
| 2 | Privacy page claims data is "always stored locally" | `Settings/PrivacySettings.tsx:83-87` | 01 |
| 3 | Archived chat permanently deleted in one click, with no confirmation or undo; the active-row "Delete" archives | `Layout/Sidebar.tsx`, `internal/handlers/chat/chat_crud.go` | 02 |
| 4 | Failed chats get the green "running" row treatment | `Layout/Sidebar.tsx` ~323 | 02 |
| 5 | An existing subscriber cannot change plan (dead end) | `Billing/MachineSaleConsent.tsx:23`, control-plane `svcbilling/machine_acceptance.go:102` | 04 |
| 6 | Hosting disclosure: unreadable cents, no $5 minimum, "no base fee", egress free tier missing | `usage/UsageBillingPanel.tsx:49`, `UsageBillingCard.tsx:41` | 04 |
| 7 | "Proposed terms, not an approved live offer" shown for approved, live prices | `Billing/MachinePricingReadinessCard.tsx:42-57` | 04 |
| 8 | Consent sentence renders ": $0.00/month, 0 own-size hours…" on partial data | `Billing/MachineSaleConsent.tsx:32` | 04 |
| 9 | Workflows Name column 0 px at ≤ ~1060 px | `workflows/list/WorkflowRow.tsx` | 05 |
| 10 | DataTable's `sr-only` header escapes its scroller, so the page scrolls sideways at 390 px | `forge-ui/data_table.tsx:267` | 01 |
| 11 | One malformed dashboard panel blanks the whole app | `Dashboards/drill.ts:59`, `DashboardView.tsx:244` | 03 |
| 12 | Chart series 2 and the second trace service are invisible (1.1–1.3:1) | `charts/UPlotChart.tsx:117`, `TraceWaterfall.tsx:80`, `ExplorerChart.tsx:44` | 03 |
| 13 | Activation card prints `Machine: No machine (server-only) (id )` | `internal/handlers/trigger/activation_agent.go:328` | 05 |
| 14 | After approval, every activation failure returns one generic sentence | `internal/llm/tools/activation_tools.go:235` | 05 |
| 15 | A trigger of a workflow that fails to parse shows "Workflow not found", is titled `Project://triage`, and is listed twice | `workflows/list/workflowsView.ts:211-221` | 05 |
| 16 | Automation dialog offers hidden building blocks as workflows | `Automations/dialog/InlineAutomationForm.tsx` | 05 |
| 17 | Chat rows, tool-group headers and right-sidebar tabs are not keyboard-operable | `Sidebar.tsx`, `ToolExecutionGroup.tsx`, `RightSidebar.tsx` | 02 |
| 18 | Selected thread-tab background is invalid CSS (`${hsl()}10`), so it never renders | `thread-views/ThreadTabs.tsx` ~206 | 02 |
| 19 | Sidebar shows "No active chats" while the list is loading or has failed | `Layout/Sidebar.tsx` ~783 | 02 |
| 20 | Appearance → "Geist" font silently renders the system font (no Geist face bundled) | `index.css:609`, `AppearanceSettings.tsx:397,426` | 00 |
| 21 | Onboarding CTA white on `bg-sky-600` measures ~4.0:1 (fails AA) | `OnboardingFlow/steps/ComputeStep.tsx:960` | 04 |
| 22 | Phone settings footer says GitHub is unavailable, above a GitHub row | `Mobile/MobileSettingsScreen.tsx:289` | 01 |
| 23 | Reliant AI tab tells you to go "to the billing page" from the billing page | `Settings/cloud/reliantAI.tsx:286,417` | 01 |
| 24 | YAML modal Copy button has no accessible name | `workflow/YamlEditorModal.tsx:138-148` | 05 |
| 25 | Org Metrics still shows a "lands here" placeholder | `Org/OrgObservabilityPage.tsx:120-124` | 03 |

## The forge-scaffolded app UI (forge repo)

Area 06 reviewed what `reliant forge project new … --frontend web` plus
`forge scaffold` produce: sign-in, shell, dashboard and the generated CRUD
pages. It used a three-entity sample app (`fieldops`). These fixes land in
**forge** (`internal/templates/frontend/**`, `internal/codegen/frontend_*`,
`pkg/components/**`, `@reliantlabs/forge-web-runtime`), not reliant. That is
why they are ranked separately.

Every forge app inherits these, and so does every "Forge app" screenshot on
the landing page. Seven bugs were filed with `reliant forge report-bug`. The
ids are in 06 §5.

Effort scale: S is up to half a day, M is one to two days, L is several days.

### Top items for forge

| # | Change | Impact | Effort | Where (forge) |
|---|---|---|---|---|
| F1 | **Inter never renders.** `inter.variable` is on `<body>`, but `--font-sans` resolves on `:root`, so every forge app shows the OS font. Move it to `<html>`, and add a contract test | H | S | `internal/templates/frontend/nextjs/src/app/layout.tsx.tmpl:136` |
| F2 | **The mock-mode banner is compiled out of static exports,** so fixture data masquerades as real data. Render it client-side, as the Vite SPA already does | H | S | `nextjs/src/app/layout.tsx.tmpl` |
| F3 | **List pages show foreign keys as raw UUIDs** under "Customer Id", and filter them with free-text UUID inputs. Attach FKs to `ListColumns`, render `EntityName`, and use `EntityPicker` filters | H | M | `internal/codegen/frontend_pages_fk.go`, `frontend_pages.go:1760`, `pages/list-page.tsx.tmpl` (both trees) |
| F4 | **The list header overflows at 390 px** (the Create button sits off-canvas). Split it into a title row and a wrapping toolbar (`ui/filter_bar`), and move the count into the pager | H | M | `web-runtime/src/resource.tsx:101-124` |
| F5 | **Pages compose the components they install.** 25 of 35 auto-installed `ui/*` components are imported by nothing, so restyling `ui/input` changes no form. This is the root of the inconsistent control heights and radii | H | L | `pages/*.tmpl`, `_partials.tmpl`, `vite-spa-pages/*`, `sign-in/page.tsx.tmpl` |
| F6 | **Form accessibility and human messages:** `aria-invalid` / `aria-describedby` / `aria-required`; "Required" instead of "String must contain at least 1 character(s)" | M | S | `pages/_partials.tmpl`, `frontend_pages.go` `attachZodExpr` |
| F7 | **Fixture coherence:** half the mock jobs point at customers that don't exist; seeded `1@1.1` emails fail the generated validator, so seeded rows can't be edited | M | M | `internal/codegen/frontend_mocks.go` |
| F8 | **Expired sessions:** `auth:expired` is declared and never emitted. Redirect to sign-in with `returnTo` | M | M | `shared/src/lib/query-client.ts`, `route-guard.tsx.tmpl` |
| F9 | **Dark-mode native controls:** no `color-scheme` is declared, so selects and date pickers render light on dark | M | S | `nextjs/src/app/globals.css` (and the Vite SPA's) |
| F10 | **Contrast floor:** `ink-subtle` used for 12 px text (3.7–3.9:1); the avatar uses a raw palette with white initials (2.9:1) | M | S | `globals.css:108`, `nav.tsx.tmpl:155`, `dashboard.tsx.tmpl`, `pkg/components/components/ui/avatar.tsx:47` |

**Next:**
- real links instead of `<tr role="button">`, plus sortable headers (the API already supports sorting);
- currency inputs for `_cents` fields;
- a success toast and a record-named delete dialog;
- a copy pass: drop "Overview of your application resources.", the lone "NAVIGATION" label, and the `web | fieldops` tab title;
- landmarks: a skip link, no nested `<main>`;
- a "Recently updated" table so the dashboard demos well.

Area 06 recommends **not** putting Reliant's palette, Geist or brand blue
into the scaffold. Its neutral system is the right "obviously neutral"
starter, and forge's own design rules say so.

**Owner decision:** whether the scaffold's declared neutral face should be
Geist or Inter.
- **For Geist:** it matches our site and the Vercel aesthetic.
- **For Inter:** it is less of a signature.
- **Either way, F1 comes first,** because today neither font renders.

## Combined view: reliant and forge together

If one team took both lists, this is the order. It interleaves by impact for
the effort, and pulls forward forge items that make our own marketing
screenshots look wrong.

| Rank | Repo | Item |
|---|---|---|
| 1 | reliant | Settings: hide Developer; make the Privacy page true (1) |
| 2 | reliant | Destructive chat actions (2) |
| 3 | forge | Inter actually renders (F1) |
| 4 | reliant | Green means success; status in words (3) |
| 5 | reliant | Pricing reads like the price list (4) |
| 6 | forge | Mock banner in static exports (F2) |
| 7 | reliant + forge | Table layout: zero-width Name column; `data_table` scroller (5). The `data_table` fix lands in forge and is re-installed in reliant |
| 8 | reliant | Live work visible in a run (6) |
| 9 | reliant | Brand quick wins (7) |
| 10 | reliant | Never a wrong or blank state (8) |
| 11 | forge | FK names and pickers on list pages (F3) |
| 12 | forge | List header on mobile (F4) |
| 13 | reliant | Approvals disclosure (9) |
| 14 | reliant | Keyboard parity (10) |
| 15 | reliant | Money surfaces copy (11) |
| 16 | reliant | Plan changes, after the owner decides (12) |
| 17 | reliant | Agents in the console's empty and failure states (13) |
| 18 | reliant | Reliant scheme (14) |
| 19 | forge | Pages compose `ui/*` (F5) |
| 20 | reliant | Settings → Integrations (15) |

## Decisions for the owner

1. **Plan changes:** a prorated upgrade now, or a switch at the next period (item 12). This needs a control-plane RPC either way.
2. **Default scheme:** whether the Reliant scheme becomes the default for everyone, or only for new users (item 14). Pure Black stays as an option in both cases.
3. **Privacy page wording:** the final text (item 1).
4. **Forge scaffold's neutral font:** Geist or Inter (forge F1).

## Gaps and caveats

**Not exercised in the harness:**
- **Chat:** a mid-stream disconnect and resume. The recovery components were reviewed in code only.
- **Workflows:** the run-detail transcript (`/workflows/runs/$id`).
- **Billing:** Stripe Elements, because there is no publishable key.
- **App shell:** the terminal (WebSockets never connect).
- **Forge app (06):** the dev IdP sign-in round trip.
- **Electron:** title-bar chrome specific to Electron.

**Fixture effects on the findings:**
- **Console "Not deployed" (03, F4d):** the Overview showing "Not deployed" for a live app was induced by the fixtures. The underlying risk, two sources of truth, is real.
- **Rechecked after a harness fix:**
  - A shared fixture used retired machine sizes and a misnamed eligibility field until about 20:40Z.
  - Areas 04 and 05 overrode or fixed their own fixtures. Every area's fixtures pass the harness's schema validator, apart from three harmless leftovers that no finding depends on: a mistyped billing-email field (04), and procedures that do not exist in chat-sidebars and onboarding-billing.
  - No finding in this document depends on the old fixtures. Area 01's note on "unknown build" in a machine's Resources cell may be a fixture artifact, and is not ranked.

**Visuals:** no pixel was seen by any reviewer. Every color, contrast and
layout claim is a measurement. Area 04 saved two PNGs for the owner
(`research/app-ux-review-2026-11/shots/onboarding-billing/`).

**Harness and tooling issues found along the way** (not product):
- `take_screenshot` with a `filePath` under the research directory is refused by the DevTools MCP workspace-root check.
- `evaluate_script` sometimes reads the previous document right after a navigation.
- Headless Chrome reports `hover: none` after touch emulation, which sends desktop routes to `/m`.
