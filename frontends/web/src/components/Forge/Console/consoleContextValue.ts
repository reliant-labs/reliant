// Copyright (c) 2025 Reliant Labs

/**
 * The console context's value and object (consoleContext.ts explains what it
 * holds). Its own module so components outside the console's pages (the
 * project shell's empty states, EmptyState/ProjectEmptyCta.tsx) can read it
 * when present without pulling in, or being broken by a mock of, the hooks.
 */

import { createContext } from "react";

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
