// Copyright (c) 2025 Reliant Labs

/**
 * The reliant services' answers: what the signed-in app reads beside the
 * forge console (projects, worktrees, chats and their transcripts, the
 * Inbox, the three open streams, workflows and runs, settings and models,
 * provider accounts, machines, the server's version, and forge's reports
 * about shop's checkout).
 *
 * Imported only by ../../app.ts. The demo pack (../../index.ts) must never
 * import this tree: it would put reliant's generated services in the demo
 * bundle (__tests__/appLayer.test.ts guards it).
 */
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { chatHandlers } from "./chats";
import { daemonHandlers } from "./daemons";
import { forgeHandlers } from "./forge";
import { inboxHandlers } from "./inbox";
import { projectHandlers } from "./projects";
import { providerAccountHandlers } from "./providerAccounts";
import { settingsHandlers } from "./settings";
import { streamingHandlers } from "./streaming";
import { systemHandlers } from "./system";
import { workflowHandlers } from "./workflows";
import { worktreeHandlers } from "./worktrees";
import { mcpHandlers } from "./mcp";

export function reliantScenarioHandlers(): MockHandlers {
  return {
    ...projectHandlers(),
    ...worktreeHandlers(),
    ...daemonHandlers(),
    ...forgeHandlers(),
    ...settingsHandlers(),
    ...mcpHandlers(),
    ...chatHandlers(),
    ...streamingHandlers(),
    ...workflowHandlers(),
    ...inboxHandlers(),
    ...providerAccountHandlers(),
    ...systemHandlers(),
  };
}
