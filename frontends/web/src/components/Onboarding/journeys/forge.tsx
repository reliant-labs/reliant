/**
 * The "Explore a forge app" journey.
 *
 * PLACEHOLDER CONTENT: two steps that exercise the engine end to end (an
 * action-gated spotlight, then a card). The real journey (the forge console
 * on fixture observability data) replaces these — see ../JOURNEYS.md. Bump
 * `version` when it ships.
 */

import { isForgeUIEnabled } from "../../../lib/forgeFeature";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import type { Journey } from "../types";

export const forgeJourney: Journey = {
  id: "forge",
  version: 1,
  title: "Explore a forge app",
  description:
    "Walk through a running app's console: traces, logs and profiles, and how an alert hands a problem to an agent.",
  // The Apps entry is the forge UI's, which a user can switch off in
  // Settings → Developer. The fixture-backed journey can drop this.
  isAvailable: isForgeUIEnabled,
  // In Reliant, Finish lands on the Apps index; the forge shell decides its own.
  finish: (shell) => (shell === "reliant" ? { to: "/apps" } : null),
  steps: [
    {
      id: "fg-open-apps",
      title: "Apps",
      description:
        "Everything your project runs: each environment, what's deployed where, its releases, and its logs, metrics and alerts.",
      target: "[data-onboarding='apps-button']",
      spotlightConfig: { padding: 4, detectBorderRadius: true },
      advanceOn: { kind: "click" },
      actionHint: "Click Apps to continue",
      autoAdvance: true,
      onEnter: () => useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(true),
    },
    {
      id: "fg-instrumented",
      title: "Instrumented from the first commit",
      description:
        "Every forge app ships logs, traces and metrics on each boundary, so an agent debugging it starts from what actually happened.",
    },
  ],
};
