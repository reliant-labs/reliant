// Copyright (c) 2025 Reliant Labs

/**
 * WHICH PROJECT THE FORGE SCREENS ARE ABOUT.
 *
 * Two spellings reach these routes:
 *
 *   project       a Reliant project id — the ordinary case. The forge project
 *                 name is the one persisted on that project's row, or reported
 *                 by its daemon.
 *   forgeProject  a forge project NAME (forge.yaml `name`), from a link the
 *                 control plane wrote — a queued deploy's action URL. The
 *                 control plane knows no Reliant project ids, so this is all
 *                 it can say.
 *
 * ForgeLayout replaces `forgeProject` with `project` as soon as it finds the
 * Reliant project that declares that name. When it stays, no project here does
 * — typically the org admin a teammate sent the link to, who has never opened
 * the project — and the screens read the CONTROL PLANE by that name alone.
 *
 * NO DAEMON IN THAT CASE, and that is the reason `projectId` is null rather
 * than whatever project happens to be current. A daemon answers for a
 * checkout; the current project's checkout is a different project, and its
 * `prod` beside this project's `prod` is exactly the wrong-environment mix the
 * project-scoped reads exist to prevent.
 */

export interface ForgeScope {
  /** The Reliant project — and so the daemon — to ask. Null when the URL names a foreign forge project. */
  projectId: string | null;
  /** The forge project name the URL names directly, when no Reliant project here declares it. */
  forgeProject: string | null;
}

export function forgeScopeOf(
  search: { project?: string; forgeProject?: string },
  currentProjectId: string | null | undefined
): ForgeScope {
  const forgeProject = (search.forgeProject ?? "").trim();
  if (forgeProject !== "") return { projectId: null, forgeProject };
  return { projectId: search.project ?? currentProjectId ?? null, forgeProject: null };
}
