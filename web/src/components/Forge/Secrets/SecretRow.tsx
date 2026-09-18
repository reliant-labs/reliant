// Copyright (c) 2025 Reliant Labs

/**
 * One declared secret.
 *
 * There is NO reveal control here, and there is nothing for one to reveal. The
 * whole stack is built so no field can carry a value: forge's report type graph
 * holds only names, booleans and coordinates; the daemon withholds forge's error
 * text entirely on this path rather than risk echoing a value; the RPC response
 * carries only meta plus report_json. So the absence of a "show" toggle is not a
 * feature that was deferred — adding one would require widening three layers
 * that are each pinned by a test against exactly that.
 *
 * What the row shows instead is the useful thing: WHO breaks if this is missing.
 * `declared_by` names the workload, its kind, and the Kubernetes Secret
 * name/key the value is fetched by at inject time. Those are coordinates, and a
 * developer chasing a failed `env up` needs them to know which workload's
 * Deployment to look at.
 */

import { cn } from "@/lib/utils";
import type { ForgeSecretEntry, ForgeSecretsReport } from "@/services/forge/secrets";
import {
  declarationsOf,
  presenceExplanation,
  presenceLabel,
  presenceOf,
  certaintyOfPresence,
} from "@/services/forge/secrets";

import { iconForPresence, styleForPresence } from "./presenceVocabulary";

export interface SecretRowProps {
  entry: ForgeSecretEntry;
  report: ForgeSecretsReport;
}

export function SecretRow({ entry, report }: SecretRowProps) {
  const presence = presenceOf(report, entry);
  const certainty = certaintyOfPresence(presence);
  const style = styleForPresence(presence);
  const Icon = iconForPresence(presence);
  const declarations = declarationsOf(entry);

  return (
    <tr className="border-b border-border last:border-0" data-testid={`secret-row-${entry.name}`}>
      <th scope="row" className="px-3 py-2.5 text-left align-top font-normal">
        <span className="font-mono text-sm text-foreground">{entry.name}</span>
      </th>

      <td className="px-3 py-2.5 align-top">
        <span
          data-testid={`presence-${entry.name}`}
          data-certainty={certainty}
          data-presence={presence}
          title={presenceExplanation(presence)}
          className={cn(
            "inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-2xs",
            style.container,
            style.foreground
          )}
        >
          <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
          {presenceLabel(presence)}
        </span>
      </td>

      <td className="px-3 py-2.5 align-top">
        {declarations.length === 0 ? (
          // Declared with no declaring workload is odd enough to say plainly
          // rather than render as an empty cell.
          <span className="text-2xs text-muted-foreground" data-testid={`declared-by-none-${entry.name}`}>
            No workload declares this
          </span>
        ) : (
          <ul className="space-y-1" data-testid={`declared-by-${entry.name}`}>
            {declarations.map((decl, index) => (
              <li
                key={`${decl.workload ?? "?"}-${decl.secret_key ?? index}`}
                className="flex flex-wrap items-baseline gap-x-2 gap-y-0.5"
              >
                <span className="font-mono text-xs text-foreground">{decl.workload || "unnamed"}</span>
                {decl.kind && (
                  <span className="rounded-full border border-border px-1.5 text-2xs text-muted-foreground">
                    {decl.kind}
                  </span>
                )}
                {(decl.secret_name || decl.secret_key) && (
                  // The coordinate the value is fetched BY — not the value.
                  <span className="font-mono text-2xs text-muted-foreground">
                    {decl.secret_name || "?"}
                    {decl.secret_key ? `/${decl.secret_key}` : ""}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </td>
    </tr>
  );
}
