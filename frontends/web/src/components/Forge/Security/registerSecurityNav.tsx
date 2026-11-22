// Copyright (c) 2026 Reliant Labs

/**
 * Security ▾'s rows (U6 follow-up): the Security pages register into the
 * nav's Security group (consoleNavRegistry.ts, `group: "security"`), which the
 * shell's nav draws only once a row has joined it. Two rows, in this order:
 *
 *   Vulnerabilities   /forge/p/<app>/security/vulnerabilities   lit on the list and on one advisory
 *   SBOMs             /forge/p/<app>/security/sboms
 *
 * Each row opens its built-in console page (`target`), so its href is
 * consolePath's and it lights wherever consolePageOf reads that page back:
 * the registry adds no route and no path of its own.
 *
 * forgeConsoleRoutes.tsx calls registerSecurityNav() at module load, beside
 * the Security pages' routes (#3085), so the rows are in place before the nav
 * first renders and are never there without the routes they link to.
 */

import { Bug, FileText } from "lucide-react";

import { registerConsoleNavEntry, type ConsoleNavEntry } from "../Console/consoleNavRegistry";

/**
 * The Security section's path segment. Once this branch is rebased on main,
 * where #3085's constants are, switch to SECURITY_SEGMENT from
 * Console/paths.ts (and SECURITY_PAGE_KINDS for the rows' order).
 */
const SECURITY_SEGMENT = "security";

/** Security ▾'s rows, in nav order. */
export const SECURITY_NAV_ENTRIES: readonly ConsoleNavEntry[] = [
  {
    id: "vulnerabilities",
    label: "Vulnerabilities",
    icon: <Bug className="h-4 w-4" aria-hidden="true" />,
    segment: SECURITY_SEGMENT,
    // Where the row sits in a nav that draws no Security group (the forge host's grouped nav).
    after: "more-tools",
    group: "security",
    target: { kind: "vulnerabilities" },
    // One advisory is a page of Vulnerabilities: its row stays lit there.
    kinds: ["vulnerabilities", "vulnerability"],
  },
  {
    id: "sboms",
    label: "SBOMs",
    icon: <FileText className="h-4 w-4" aria-hidden="true" />,
    segment: SECURITY_SEGMENT,
    after: "more-tools",
    group: "security",
    target: { kind: "sboms" },
  },
];

/** Register Security ▾'s rows; returns their removal (for tests). */
export function registerSecurityNav(): () => void {
  const removals = SECURITY_NAV_ENTRIES.map((entry) => registerConsoleNavEntry(entry));
  return () => removals.forEach((remove) => remove());
}
