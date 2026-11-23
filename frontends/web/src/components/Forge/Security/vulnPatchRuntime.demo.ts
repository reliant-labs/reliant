// Copyright (c) 2025 Reliant Labs

/**
 * vulnPatchRuntime.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). The demo has no Reliant project, repository or
 * GitHub account, so a patch is never startable there: the buttons say why
 * instead of loading the chat and project code.
 */

import type * as Real from "./vulnPatchRuntime";

export type VulnPatchStart = Real.VulnPatchStart;
export type StartedVulnPatch = Real.StartedVulnPatch;

export const useVulnPatchAccess: typeof Real.useVulnPatchAccess = () => ({
  state: "blocked",
  reason: "The demo has no repository to patch: in Reliant, a patch opens a pull request on the project's GitHub repository.",
});

export const startVulnPatch: typeof Real.startVulnPatch = async () => {
  throw new Error("Patching is not part of the forge demo.");
};
