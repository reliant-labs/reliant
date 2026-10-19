// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: the REAL console chrome (ConsoleShell) around a preview
 * harness's screen, on a fabricated project — so a screenshot of a harness
 * shows the nav and top bar the product shows, not a bare screen that looks
 * like the product and is not (which cost two review cycles once, asking
 * where the left nav had gone).
 *
 * The harness's own pickers (case, scheme, dark) sit in a strip above the
 * page, outside the product's top bar.
 */

import { useState, type ReactNode } from "react";

import { ConsoleShell } from "./ConsoleShell";
import { ProjectMonogram } from "./ConsoleProjectSwitcher";
import type { ConsolePage } from "./paths";

const PROJECT = "control-plane";

const ENVS = [
  { name: "prod", preview: false },
  { name: "e2e", preview: false },
  { name: "dev", preview: false },
  { name: "pr-1042", preview: true },
];

export function ConsolePreviewShell({
  page,
  controls,
  children,
  env: controlledEnv,
  onEnvChange,
}: {
  page: ConsolePage;
  controls?: ReactNode;
  children: ReactNode;
  /** The top-bar scope, when the harness's screen reads it (Releases); otherwise the shell keeps its own. */
  env?: string;
  onEnvChange?: (env: string) => void;
}) {
  const [ownEnv, setOwnEnv] = useState(page.kind === "environment" ? page.env : "prod");
  const env = controlledEnv ?? ownEnv;
  const setEnv = onEnvChange ?? setOwnEnv;
  const [collapsed, setCollapsed] = useState(false);
  return (
    <ConsoleShell
      project={PROJECT}
      page={page}
      env={env}
      envs={ENVS}
      alertsFiring={1}
      scope={{
        long: ENVS.filter((candidate) => !candidate.preview).map((candidate) => ({ name: candidate.name, recorded: true })),
        previews: ENVS.filter((candidate) => candidate.preview).map((candidate) => ({ name: candidate.name, recorded: true })),
      }}
      onEnvChange={setEnv}
      user={{ name: "preview", email: "preview@example.com" }}
      collapsed={collapsed}
      onToggleCollapsed={() => setCollapsed((prev) => !prev)}
      brand={
        <span className="flex items-center gap-2 px-1.5 text-sm font-semibold text-ink">
          <ProjectMonogram name={PROJECT} />
          {PROJECT}
        </span>
      }
      brandCollapsed={<ProjectMonogram name={PROJECT} />}
    >
      {controls && (
        <div className="mb-6 flex flex-wrap items-center gap-3 rounded-lg border border-dashed border-border px-3 py-2" data-testid="preview-controls">
          {controls}
        </div>
      )}
      {children}
    </ConsoleShell>
  );
}
