// Copyright (c) 2025 Reliant Labs

/**
 * Which backend "Ask Reliant" uses: one seam, two backends.
 *
 *   "chat"     Reliant (the default): a new chat on its own workspace
 *              (startAskReliantChat). Needs the daemon and a Reliant project.
 *   "handoff"  The forge shell (forge.reliantlabs.io) and every demo on
 *              fixture data: no daemon, no chat. The action opens the handoff
 *              sheet instead (AskAgentSheet): a references-only prompt to paste
 *              into the reader's own agent, the forge commands to run
 *              themselves, forge's playbook, and a way into Reliant.
 *
 * Every surface (trace, issue, alert, panel, workload, release, deploy,
 * session) reads it through useAskReliant, so a provider at the top of a shell
 * switches all of them at once.
 */

import { createContext, useContext, useMemo, type ReactNode } from "react";

import type { AskCli } from "./askReliantPrompt";

export type AskAgentMode = "chat" | "handoff";

export interface AskAgentModeValue {
  mode: AskAgentMode;
  /** How the reader's agent spells the CLI: `forge` in the forge shell, `reliant forge` in Reliant. */
  cli: AskCli;
  /** The subject is sample data (the forge demo): the sheet says so, and offers no deep link into Reliant. */
  demo: boolean;
}

const DEFAULT: AskAgentModeValue = { mode: "chat", cli: "reliant forge", demo: false };

const AskAgentModeContext = createContext<AskAgentModeValue>(DEFAULT);

export function AskAgentModeProvider({
  mode,
  cli = "reliant forge",
  demo = false,
  children,
}: Partial<Omit<AskAgentModeValue, "mode">> & { mode: AskAgentMode; children: ReactNode }) {
  const value = useMemo(() => ({ mode, cli, demo }), [mode, cli, demo]);
  return <AskAgentModeContext.Provider value={value}>{children}</AskAgentModeContext.Provider>;
}

export function useAskAgentMode(): AskAgentModeValue {
  return useContext(AskAgentModeContext);
}
