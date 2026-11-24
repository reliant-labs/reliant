// Copyright (c) 2025 Reliant Labs

/**
 * The reliant services' answers: what the signed-in app reads beside the
 * forge console (projects, worktrees, chats and their transcripts, the two
 * open streams, workflows and runs, settings and models, machines).
 *
 * Imported only by ../../app.ts. The demo pack (../../index.ts) must never
 * import this tree: it would put reliant's generated services in the demo
 * bundle (__tests__/appLayer.test.ts guards it).
 */
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { chatHandlers } from "./chats";
import { daemonHandlers } from "./daemons";
import { projectHandlers } from "./projects";
import { settingsHandlers } from "./settings";
import { streamingHandlers } from "./streaming";
import { workflowHandlers } from "./workflows";
import { worktreeHandlers } from "./worktrees";

export function reliantScenarioHandlers(): MockHandlers {
  return {
    ...projectHandlers(),
    ...worktreeHandlers(),
    ...daemonHandlers(),
    ...settingsHandlers(),
    ...chatHandlers(),
    ...streamingHandlers(),
    ...workflowHandlers(),
  };
}
