// Copyright (c) 2025 Reliant Labs

/**
 * Policies › Denials (admins and owners): what the org's policies refused,
 * and what rules in audit mode would have refused, over the last days
 * (ListOrgPolicyDenials, org-policies §3.4): "blocked 31 calls this week by
 * the model policy, 9 would have been blocked by the audit-mode rule".
 *
 * One row per policy and reason, summed over the window, split by outcome:
 * "Refused" for an enforced rule, "Would have been refused" for one in audit,
 * which counts and refuses nothing. The outcome is the server's per count, so
 * a rule switched from audit to enforce mid-window shows both rows.
 *
 * Admin-only on the server too: the counts name members and what they tried.
 */

import { useState } from "react";

import Badge from "@/components/forge-ui/badge";
import Button from "@/components/forge-ui/button";
import { isRpcNotDeployed, policyErrorMessage, type PolicyDenialCount } from "@/services/controlPlane/orgPolicy";

import { listWords, selectClass } from "./PolicyCard";
import { SECTION_KEYS, SECTION_TITLES, type SectionKey } from "./policyDocument";
import { useOrgPolicyDenials } from "./usePolicies";

const WINDOWS = [7, 30, 90] as const;

const REASON_WORDS: Record<string, string> = {
  model_not_allowed: "A model the policy does not allow",
  member_spend_cap_reached: "A member's spend limit was reached",
  org_spend_cap_reached: "The organization's spend limit was reached",
  provider_not_allowed: "A provider or kind of account the policy does not allow",
  integration_not_allowed: "An integration the policy does not allow",
};

/** A reason in words; one this page does not know reads as its own name. */
export function reasonWords(reason: string): string {
  if (REASON_WORDS[reason]) return REASON_WORDS[reason];
  const s = reason.replace(/_/g, " ");
  return s.charAt(0).toUpperCase() + s.slice(1);
}

export interface DenialRow {
  policy: string;
  reason: string;
  outcome: "refused" | "audited";
  count: number;
  /** The members the calls acted as, by user id. */
  userIds: string[];
  /** Some calls were a hosted app's environment key, which acts as no member. */
  apps: boolean;
}

/**
 * The window's counts summed per policy, reason and outcome, policies in page
 * order (an unknown section last), refusals before would-be refusals, then by
 * count.
 */
export function summarizeDenials(counts: PolicyDenialCount[]): DenialRow[] {
  const rows = new Map<string, DenialRow & { users: Set<string> }>();
  for (const c of counts) {
    const key = `${c.policy}\u0000${c.reason}\u0000${c.outcome}`;
    let row = rows.get(key);
    if (!row) {
      row = { policy: c.policy, reason: c.reason, outcome: c.outcome, count: 0, userIds: [], apps: false, users: new Set() };
      rows.set(key, row);
    }
    row.count += c.count;
    if (c.userId) row.users.add(c.userId);
    else row.apps = true;
  }
  const order = (p: string) => {
    const i = SECTION_KEYS.indexOf(p as SectionKey);
    return i === -1 ? SECTION_KEYS.length : i;
  };
  return [...rows.values()]
    .map(({ users, ...r }) => ({ ...r, userIds: [...users].sort() }))
    .sort(
      (a, b) =>
        order(a.policy) - order(b.policy) ||
        a.policy.localeCompare(b.policy) ||
        (a.outcome === b.outcome ? 0 : a.outcome === "refused" ? -1 : 1) ||
        b.count - a.count ||
        a.reason.localeCompare(b.reason),
    );
}

/** "Ada, Bob and 3 others", "a hosted app". */
function whoWords(row: DenialRow, nameOf: (userId: string) => string): string {
  const names = row.userIds.map(nameOf);
  const shown = names.slice(0, 2);
  const rest = names.length - shown.length;
  const parts = rest > 0 ? [...shown, `${rest} ${rest === 1 ? "other" : "others"}`] : shown;
  if (row.apps) parts.push("a hosted app");
  return listWords(parts);
}

const calls = (n: number) => `${n.toLocaleString("en-US")} ${n === 1 ? "call" : "calls"}`;

export function PolicyDenials({ orgId, nameOf }: { orgId: string; nameOf: (userId: string) => string }) {
  const [days, setDays] = useState<number>(7);
  const denials = useOrgPolicyDenials(orgId, days);
  const span = days === 7 ? "this week" : `in the last ${days} days`;

  let body;
  if (denials.isPending) {
    body = (
      <p className="px-4 py-3 text-sm text-muted-foreground" role="status">
        Loading denials…
      </p>
    );
  } else if (denials.isError) {
    body = isRpcNotDeployed(denials.error) ? (
      <p className="px-4 py-3 text-sm text-muted-foreground" data-testid="policy-denials-unavailable">
        Denial counts are not available on this deployment yet.
      </p>
    ) : (
      <div className="flex items-center gap-3 px-4 py-3" role="alert">
        <p className="flex-1 text-sm text-muted-foreground">{policyErrorMessage(denials.error, "Could not load the denial counts.")}</p>
        <Button variant="outline" size="sm" onClick={() => void denials.refetch()}>
          Try again
        </Button>
      </div>
    );
  } else {
    const rows = summarizeDenials(denials.data.counts);
    body =
      rows.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" data-testid="policy-denials-empty">
          Nothing was refused {span}, and no rule in audit mode would have refused anything.
        </p>
      ) : (
        <>
          <p className="border-b border-border px-4 py-3 text-sm text-foreground" data-testid="policy-denials-totals">
            {calls(denials.data.refusedTotal)} refused {span}; {calls(denials.data.auditedTotal)} would have been refused by rules in audit mode.
          </p>
          <ul className="divide-y divide-border" data-testid="policy-denials">
            {rows.map((r) => (
              <li
                key={`${r.policy}-${r.reason}-${r.outcome}`}
                className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-4 py-3"
                data-testid={`policy-denial-${r.policy}-${r.reason}-${r.outcome}`}
              >
                <div className="min-w-0 flex-1">
                  <p className="flex flex-wrap items-center gap-2 text-sm font-medium text-foreground">
                    {SECTION_TITLES[r.policy as SectionKey] ?? r.policy}
                    {r.outcome === "audited" ? <Badge label="Audit only" variant="info" size="sm" /> : <Badge label="Enforced" size="sm" />}
                  </p>
                  <p className="text-sm text-muted-foreground">
                    {reasonWords(r.reason)}
                    {(r.userIds.length > 0 || r.apps) && ` · ${whoWords(r, nameOf)}`}
                  </p>
                </div>
                <p className="text-sm tabular-nums text-foreground">
                  {calls(r.count)} {r.outcome === "audited" ? "would have been refused" : "refused"}
                </p>
              </li>
            ))}
          </ul>
        </>
      );
  }

  return (
    <section className="rounded-lg border border-border bg-card" aria-labelledby="policy-denials-heading" data-testid="policy-denials-panel">
      <header className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3">
        <h3 id="policy-denials-heading" className="flex-1 text-sm font-semibold text-foreground">
          What the policies refused
        </h3>
        <select aria-label="Period" className={selectClass} value={days} onChange={(e) => setDays(Number(e.target.value))}>
          {WINDOWS.map((d) => (
            <option key={d} value={d}>
              Last {d} days
            </option>
          ))}
        </select>
      </header>
      {body}
      <p className="border-t border-border px-4 py-2 text-xs text-muted-foreground">
        Counted per UTC day; a refusal shows here within about a minute. Rules in audit mode refuse nothing: they count what they would have refused.
      </p>
    </section>
  );
}
