// Copyright (c) 2025 Reliant Labs

/**
 * WHAT EVERY CONSOLE PAGE IS ABOUT, said once by ConsoleLayout.
 *
 *   forgeProject  the forge project the URL names — the control plane's key
 *                 for everything it records (paths.ts).
 *   projectId     the Reliant project that declares it, which is what reaches
 *                 a daemon. Null when no project here declares it — typically
 *                 an org admin opening a link a teammate sent, who has never
 *                 opened the project: the pages then read the control plane by
 *                 name alone and ask no daemon. Using whichever project
 *                 happened to be current instead would put ITS checkout's
 *                 `prod` beside this project's `prod`, the wrong-environment
 *                 mix project-scoped reads exist to prevent.
 *   env           the environment scope in effect (envScope.ts): a name or
 *                 "all". Already resolved — defaulted, remembered, validated —
 *                 so every page reads the same scope from its first render.
 *
 * A context rather than each page re-deriving it from the URL, because the
 * derivation is not a pure read of the URL: it needs the project list and the
 * remembered scope, and two pages deriving it separately could disagree.
 */

import { createContext, useContext } from "react";

import type { useForgeRoster } from "@/hooks/forge-queries";

import type { ConsolePage } from "./paths";

export interface ForgeConsole {
  forgeProject: string;
  projectId: string | null;
  page: ConsolePage;
  env: string;
  setEnv: (env: string) => void;
  /** The project's environments — the same roster the nav and scope control read. */
  roster: ReturnType<typeof useForgeRoster>;
  /** A console page's href, carrying the scope. */
  href: (page: ConsolePage, search?: Record<string, unknown>) => string;
}

export const ForgeConsoleContext = createContext<ForgeConsole | null>(null);

export function useForgeConsole(): ForgeConsole {
  const value = useContext(ForgeConsoleContext);
  if (!value) throw new Error("useForgeConsole must be used inside the forge console layout (ConsoleLayout)");
  return value;
}
