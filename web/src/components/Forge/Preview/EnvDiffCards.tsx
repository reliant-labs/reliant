// Copyright (c) 2025 Reliant Labs

/**
 * THE §8.2 DIFF CARDS — one per environment this checkout declares.
 *
 * ── WHY A CARD PER ENVIRONMENT AND NOT ONE DOCUMENT ─────────────────────────
 *
 * `forge env diff --all` renders every declared environment in one call, which
 * is cheaper per environment than asking one at a time. It is also the wrong
 * shape for this surface, because the cost is not the round trip — it is the
 * RENDER, 2.2–2.5s of KCL per environment on the user's own machine. `--all`
 * pays for every environment the moment anyone wants one, and there is no way
 * to want less.
 *
 * So each card asks for its own environment, when opened. A user who wants all
 * of them opens all of them and pays exactly what `--all` would have cost; a
 * user who wants `prod` pays for `prod`. The expensive default is the one
 * nobody chose.
 *
 * ── WHICH ENVIRONMENTS ARE LISTED ───────────────────────────────────────────
 *
 * The ones DECLARED IN THIS CHECKOUT, from forge's topology, because the
 * question this surface answers is "what would this code do" — and code that
 * does not declare an environment would do nothing to it. An environment that
 * exists in the control plane but not in this checkout is Live's subject, not
 * Preview's.
 *
 * The list is absent, not empty-stated, when there is nothing to show. A
 * heading over a blank area asks the reader to work out whether something
 * failed.
 */

import { environments, type ForgeTopologyReport } from "@/services/forge/topology";

import { EnvDiffCard } from "./EnvDiffCard";

export interface EnvDiffCardsProps {
  projectId: string | null;
  /** forge's topology report for this checkout, when the daemon answered. */
  topology: ForgeTopologyReport | null;
  /** The checkout every card renders. Empty means the main checkout. */
  checkoutPath: string;
  /**
   * The environment the user is looking at. It leads the list — this page is
   * about that environment, and making someone hunt for it among its siblings
   * is a worse answer to the question they actually asked.
   */
  currentEnv: string;
}

export function EnvDiffCards({
  projectId,
  topology,
  checkoutPath,
  currentEnv,
}: EnvDiffCardsProps) {
  const declared = declaredEnvNames(topology, currentEnv);
  if (declared.length === 0) return null;

  return (
    <div className="space-y-2" data-testid="env-diff-cards">
      {declared.map((env) => (
        <EnvDiffCard
          // Keyed by the CHECKOUT too. Changing the picker makes every card a
          // question about different code, so the old cards must not survive
          // with their previous answers open underneath a new branch's name.
          key={`${checkoutPath}:${env}`}
          projectId={projectId}
          env={env}
          checkoutPath={checkoutPath}
        />
      ))}
    </div>
  );
}

/**
 * The environments declared in this checkout, the current one first.
 *
 * The current environment is included even when forge's topology does not
 * declare it: a user on this page asked about it, and a card that says "nothing
 * is declared here" is a real answer, whereas omitting the card leaves them
 * wondering whether Preview simply did not load.
 */
export function declaredEnvNames(
  topology: ForgeTopologyReport | null,
  currentEnv: string
): string[] {
  const names = environments(topology)
    .filter((env) => env.declared !== false)
    .map((env) => env.env);
  const rest = names.filter((name) => name !== currentEnv).sort();
  if (!currentEnv) return rest;
  return [currentEnv, ...rest];
}
