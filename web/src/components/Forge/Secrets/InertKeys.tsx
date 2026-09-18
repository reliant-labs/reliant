// Copyright (c) 2025 Reliant Labs

/**
 * The inert set: keys in the store that no workload declares.
 *
 * These are not spare secrets, and a bare list of names would read as though
 * they were — "extra secrets, fine, more is safe". They are inert: forge injects
 * them nowhere, so nothing reads them at runtime. The usual causes are a typo in
 * a key name (the workload is still missing its real secret, and the store looks
 * full), a leftover from a workload that has since been deleted, or plain
 * configuration that belongs in deploy/kcl/<env>/config.k and not in a secret
 * store at all.
 *
 * The panel therefore explains before it lists, and it is deliberately NOT
 * styled as a failure — an inert key breaks nothing today. It is muted and
 * informational, which is also why it is absent entirely when the set is empty
 * rather than rendering a reassuring "0 inert keys" that nobody asked about.
 */

import { cn } from "@/lib/utils";
import type { ForgeSecretsReport } from "@/services/forge/secrets";
import { INERT_EXPLANATION, inertKeys } from "@/services/forge/secrets";

import { INERT_ICON } from "./presenceVocabulary";

export function InertKeys({ report }: { report: ForgeSecretsReport }) {
  const keys = inertKeys(report);
  if (keys.length === 0) return null;

  return (
    <section
      data-testid="secrets-inert"
      className={cn("rounded-lg border border-dashed border-border p-4")}
    >
      <div className="flex items-center gap-2">
        <INERT_ICON className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <h2 className="text-sm font-medium text-foreground">
          {keys.length} inert key{keys.length === 1 ? "" : "s"} in the store
        </h2>
      </div>
      <p className="pt-1.5 text-xs leading-snug text-muted-foreground">{INERT_EXPLANATION}</p>
      <ul className="flex flex-wrap gap-1.5 pt-3">
        {keys.map((key) => (
          <li
            key={key}
            className="rounded-full border border-dashed border-border px-2 py-0.5 font-mono text-2xs text-muted-foreground"
          >
            {key}
          </li>
        ))}
      </ul>
    </section>
  );
}
