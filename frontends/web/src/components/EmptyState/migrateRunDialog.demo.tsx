// Copyright (c) 2026 Reliant Labs

/**
 * components/EmptyState/migrateRunDialog.tsx in the forge demo's bundle (a
 * demo twin, vite-plugin-demo-bundle.ts): the demo has no Reliant projects,
 * so Migrate links to the workflow's page and this dialog never opens. It
 * renders nothing, and keeps the workflow run form out of the demo bundle.
 */

import type * as Real from "./migrateRunDialog";

export const MigrateRunDialog: typeof Real.MigrateRunDialog = (() => null) as unknown as typeof Real.MigrateRunDialog;
