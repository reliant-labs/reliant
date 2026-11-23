// Copyright (c) 2026 Reliant Labs

/**
 * The Move to Forge workflow's Run dialog, for the empty states' Migrate
 * action (ProjectEmptyCta.tsx). It loads when it opens: every console empty
 * state renders the row, and the dialog brings the run form and the chat
 * actions with it. A module of its own so the forge demo's bundle can replace
 * it (migrateRunDialog.demo.tsx): the demo has no workflows to run.
 */

import { lazy } from "react";

export const MigrateRunDialog = lazy(() =>
  import("@/components/workflow/run/RunNowDialog").then((m) => ({ default: m.RunNowDialog })),
);
