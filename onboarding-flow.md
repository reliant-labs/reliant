# Onboarding Flow

## Overview

Reliant's onboarding has two layers:

1. **OnboardingFlow** - a full-screen overlay wizard that explains the product value, connects a workspace, captures first intent, creates/selects the project location, and configures model access.
2. **OnboardingWizard** - a 9-step guided spotlight tour of the UI, plus a persistent setup-guide checklist.

The flow is designed so users understand the value before setup, then reach a useful first chat quickly. The UI tour is opt-in: it starts only for "Explore Reliant" or from the setup guide later.

---

## Entry conditions

Onboarding is a first-run experience for accounts or workspaces with no projects. In `ModernApp`, the overlay is mounted over `ProjectPicker` only when the loaded project list is empty, or when a development override is active.

| Condition                   | Trigger                                                                                                            |
| --------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| **No loaded projects**      | `projects.length === 0` after project loading completes; the project picker remains behind the overlay.            |
| **Development override**    | `?reset-onboarding` clears onboarding state and forces the overlay to show for local testing.                      |
| **Credits test override**   | `?onboarding-credits=eligible` or `?onboarding-credits=ineligible` forces the model step branch for local testing. |
| **Internal dev force flag** | `devForceShowOnboarding` can force the overlay during development.                                                 |

Authentication still gates access before onboarding: Supabase users sign in or auto-sign in first, API-key mode accepts the key first, and cloud mode may register additional control-plane behavior.

The overlay renders when `onboardingFlowStore.state` is `not_started` or `in_progress`, or when the development force flag is active. It persists in `localStorage` under `reliant-onboarding`.

---

## Layer 1: OnboardingFlow overlay

### Step 0 - ValueStep (how Reliant works)

A value screen that explains Reliant before asking setup questions:

- **Connect context** - Reliant needs a hosted workspace or a local daemon to access files and run tools.
- **Choose a workflow** - the first intent maps to Forge, Agent, content pipelines, or guided workflows.
- **Recover gracefully** - threads, approvals, and workspace state keep long-running work resumable.

The only primary action is **Set up workspace**.

### Step 1 - ComputeStep (connect workspace)

Users choose where Reliant runs:

| Choice                       | Behavior                                                                                                                |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| **Hosted Reliant workspace** | Shown when `VITE_CONTROL_PLANE_API_URL` is set; creates/reuses a hosted daemon, waits for it to connect, then advances. |
| **Use my computer**          | Shows download/install instructions and `reliant daemon start`; advances to daemon polling.                             |

Hosted workspaces can safely auto-create project paths because Reliant owns `/home/workspace/projects`. Local/user-owned daemons never auto-create a folder without confirmation.

### Step 2 - DaemonConnectStep (local only)

Shown when `compute === 'local_daemon'`. It polls `ListDaemons` until an active daemon is detected, then advances automatically. Users can skip if they want to connect later.

### Step 3 - GoalStep (intent selection)

Six intent cards pre-wire launch defaults but no longer finish onboarding directly. Local daemons choose intent before folder location so the location step knows whether to open an existing folder, create a starter workspace, or create the sample workspace.

| Intent                       | Workflow                   | Code source                                                | Extras                                                                                              |
| ---------------------------- | -------------------------- | ---------------------------------------------------------- | --------------------------------------------------------------------------------------------------- |
| **Build something new**      | `forge-one-shot` initially | `new_project`                                              | Opens a separate project-style step for Forge vs Clean project.                                     |
| **Work on existing project** | `agent`                    | `local_folder` locally, `github_repo` on hosted workspaces | Local users choose a folder; hosted users pick and clone a GitHub repo.                             |
| **Create a landing page**    | `get-it-right`             | `new_project`                                              | Preset `ux`, review instructions for visual quality.                                                |
| **Create a pitch deck**      | `pitch-deck`               | `new_project`                                              | `ask: false`, auto-start pipeline.                                                                  |
| **Write docs / blog post**   | `blog-content-pipeline`    | `new_project`                                              | Preset `documentation`.                                                                             |
| **Create a custom workflow** | `build-workflow`           | `new_project`                                              | Scopes the workflow, builds/tests custom integrations, then builds the workflow with scenario validation. |
| **Explore Reliant**          | `agent`                    | `sample_project`                                           | Creates/selects a sample workspace and starts the guided spotlight tour after onboarding completes. |

### Step 4 - GitHubConnectStep (hosted existing project only)

Shown when `codeSource === 'github_repo'` and `compute === 'cloud_free_trial'`. It saves GitHub credentials, lists repos, clones the selected repo into `/home/workspace/projects/<repo-slug>`, creates/selects the Reliant project for that path, and stores the repo, branch, project name, and hosted path on the launch plan.

### Step 5 - ProjectLocationStep (project location)

Shown for generated workspaces and local folders:

- **Cloud new or sample project** - derives `/home/workspace/projects/<slug>` from the intent/project name and creates the project there.
- **Local new or sample project** - suggests `~/Projects/<slug>` but requires the user to confirm, type, or browse a folder.
- **Existing local folder** - requires the user to browse or enter the folder path.

Project creation/select happens before model setup so the final completion handler can write the initial prompt into the selected project's new-chat draft.

### Step 6 - ForgeStep (new app only)

Shown when `intent === 'build_app'` and `codeSource === 'new_project'`.

- **Use Forge** - sets `workflowId: builtin://forge-one-shot`, `useForge: true`, and Forge-oriented prompt/params.
- **Clean project** - sets `workflowId: builtin://agent`, `useForge: false`, and a blank-project agent prompt.

This is a separate screen rather than an inline toggle.

### Step 7 - ModelStep (credits or BYOK)

Model setup is last:

- If the control-plane user payload passes credit, global-budget, and IP eligibility checks, Reliant credits are auto-selected.
- If credits are unavailable or eligibility is unknown, BYOK is required before starting.
- `?onboarding-credits=eligible` and `?onboarding-credits=ineligible` force the two branches for testing.

Clicking the final action calls `completeOnboarding(plan)` and then `onboardingFlowStore.completeOnboarding()` to dismiss the overlay. Errors are caught and displayed inline with retry.

---

## Completion handler (`useOnboardingComplete`)

When onboarding finishes, `completeOnboarding(plan)` runs in order:

1. **Registered handlers** - cloud mode registers one that calls `CompleteOnboarding` RPC. Hosted daemon creation happens earlier in `ComputeStep`, before project creation needs filesystem access.
2. **Workflow params + presets** - seeds `tempNewChatParams` in `chatParamsStore` with the plan's `workflowParams`, `__selectedWorkflow`, and `__selectedPresets`, so the first new chat inherits them.
3. **Initial prompt** - writes the plan's `initialPrompt` to `workspaceStateStore.setNewChatDraft` when a project is already selected, pre-filling the chat input.
4. **Guided tour** - if `launchTour`, calls `onboardingChecklistStore.startWizard()` to begin the spotlight tour.

`ProjectPicker` itself is not an onboarding step. It remains the explicit project selection flow that is visible behind first-run onboarding and used directly when users open, create, or switch projects later.

---

## Layer 2: OnboardingWizard (spotlight tour + checklist)

### Guided tour (9 steps)

The tour is **never auto-started**. It runs only when:

- The user chose "Explore Reliant" (which sets `launchTour: true`), or
- The user clicks "Start tour" in the setup-guide checklist.

| #   | Step             | Type            | What it highlights                       |
| --- | ---------------- | --------------- | ---------------------------------------- |
| 1   | Welcome          | Modal           | Value pitch                              |
| 2   | Chat & Sidebars  | Multi-spotlight | Left sidebar, chat input, right sidebar  |
| 3   | Workspaces       | Spotlight       | Workspace selector buttons               |
| 4   | Workflow Intro   | Spotlight       | Workflow button in header                |
| 5   | Workflow Hub     | Spotlight       | Template browser (enters workflow mode)  |
| 6   | Workflow Builder | Spotlight       | Visual canvas (opens `__new__` workflow) |
| 7   | Builder Chat     | Spotlight       | Builder AI assistant panel               |
| 8   | Presets & Params | Modal           | Explains presets and parameters          |
| 9   | Completion       | Modal           | Quick-start actions                      |

Completing the final step or skipping marks the tour done, adds `take-product-tour` to the checklist completed items, and persists state.

### Setup-guide checklist

The checklist is a floating panel (bottom-right) that appears once the OnboardingFlow overlay is completed or skipped. It tracks real user actions via auto-detection and store subscriptions.

**Required items:**

| Item                  | Auto-detected by                              |
| --------------------- | --------------------------------------------- |
| Add an API key        | Provider check or `api-key-saved` event       |
| Start a chat          | `chatStore.chats.size > 0`                    |
| Use a custom workflow | Any chat with a non-default workflow          |
| Create a workflow     | Any user-source workflow exists               |
| Take the product tour | `hasCompletedOnboarding` flag (tour finished) |

**Bonus items:**

| Item                  | Auto-detected by                      |
| --------------------- | ------------------------------------- |
| Create a workspace    | Any non-main worktree exists          |
| Create a preset       | Any user-source preset exists         |
| Install an MCP server | `mcpGrpc.listServers` returns results |
| Read the docs         | Manual (external link click)          |

Each item has an action button: open a modal, focus the chat input, navigate to workflow mode, start the tour, open a URL, etc.

The panel can be collapsed to a pill or dismissed entirely. State is persisted to the settings DB.

---

## State management

| Store                      | Key                                       | Purpose                                     |
| -------------------------- | ----------------------------------------- | ------------------------------------------- |
| `onboardingFlowStore`      | `reliant-onboarding` (localStorage)       | Overlay state, plan, step index             |
| `onboardingChecklistStore` | Settings DB keys (`onboarding.*`)         | Tour progress, checklist items, panel state |
| `projectStore`             | `projects`, `currentProject`, `isLoading` | Project-count gate and picker visibility    |
| `chatParamsStore`          | `tempNewChatParams` (transient)           | Workflow params/presets for the first chat  |
| `workspaceStateStore`      | `newChatDraft`                            | Initial prompt pre-fill                     |

---

## Dev tools

- `?reset-onboarding` URL param clears onboarding state and forces the overlay to show, even when projects already exist.
- `?onboarding-credits=eligible` forces the credits branch in the model step.
- `?onboarding-credits=ineligible` forces the BYOK-required branch in the model step.
- `window.__resetOnboarding()` console helper clears localStorage and logs instructions.
- Settings > About > "Restart product tour" calls `restartWizard()`.