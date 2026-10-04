// Copyright (c) 2025 Reliant Labs

/**
 * Nothing links to the retired paths of the Workflows area (WORKFLOW_UI.md
 * §1.3): `/runs*`, `/automations*` and the `/workflow` hub all live under
 * `/workflows` now. The old paths still REDIRECT, so a stray link would keep
 * working and nobody would notice it costs an extra hop, flashes the wrong
 * URL, and keeps a route alive that should be dead. This scan is what notices.
 *
 * The only files allowed to spell the old paths are the redirect table
 * (workflowsAreaRoutes.tsx) and the path mapping it uses (lib/workflowsArea.ts). The builder's own paths
 * (`/workflow/$workflowName`, `/workflow/new`) are unchanged and allowed.
 */

import { readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { describe, expect, it } from "vitest";

const SRC_DIR = join(__dirname, "..", "..", "..");

/** The redirect table and the path mapping it uses. */
const ALLOWED = new Set(["workflowsAreaRoutes.tsx", "lib/workflowsArea.ts"]);

/** A quoted string that IS an old path: "/runs", '/runs/$runId', `/automations`, "/workflow". */
const OLD_PATH_LITERAL = /(["'`])\/(?:runs|automations)(?:\/[^"'`\s]*)?\1|(["'`])\/workflow\2/;

function sourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    if (statSync(full).isDirectory()) {
      if (entry === "gen" || entry === "__tests__" || entry === "node_modules") continue;
      out.push(...sourceFiles(full));
    } else if (/\.(ts|tsx)$/.test(entry) && !/\.test\.(ts|tsx)$/.test(entry)) {
      out.push(full);
    }
  }
  return out;
}

/** Comment lines are prose; a path written there is not a link. */
function isCommentLine(line: string): boolean {
  const trimmed = line.trim();
  return trimmed.startsWith("//") || trimmed.startsWith("*") || trimmed.startsWith("/*");
}

export function findOldPathLinks(): string[] {
  const hits: string[] = [];
  for (const file of sourceFiles(SRC_DIR)) {
    const rel = relative(SRC_DIR, file);
    if (ALLOWED.has(rel)) continue;
    readFileSync(file, "utf8")
      .split("\n")
      .forEach((line, index) => {
        if (!isCommentLine(line) && OLD_PATH_LITERAL.test(line)) {
          hits.push(`${rel}:${index + 1}: ${line.trim()}`);
        }
      });
  }
  return hits;
}

describe("Workflows area links", () => {
  it("no source file outside the redirect table links to /runs, /automations or the /workflow hub", () => {
    expect(findOldPathLinks()).toEqual([]);
  });

  it("the pattern catches each old spelling and spares the builder's paths", () => {
    for (const bad of [`to="/runs"`, `to: '/runs/$runId'`, `"/automations"`, `"/automations/$triggerId"`, `navigate({ to: "/workflow" })`]) {
      expect(OLD_PATH_LITERAL.test(bad), bad).toBe(true);
    }
    for (const fine of [`"/workflow/$workflowName"`, `"/workflow/new"`, `"/workflows/runs"`, `"/workflows/library"`]) {
      expect(OLD_PATH_LITERAL.test(fine), fine).toBe(false);
    }
  });
});
