// Copyright (c) 2025 Reliant Labs

/**
 * WHO MAY USE WHICH OF THE ORG'S AI KEYS: a people × keys grid, beside the
 * people × environments grid (org-provider-accounts §3.3, §7, D2).
 *
 * Only keys whose access is "Selected people" have a column: a key open to
 * everyone, or to owners and admins, is decided by its access setting on
 * Settings › AI accounts, not here. Each edit sends the key's whole new set of
 * people (SetOrgProviderAccountGrants replaces it), so two admins cannot
 * interleave into a set neither chose; a failed edit reverts the box, because
 * a box left ticked after a refusal is someone believed to have access they
 * do not have.
 *
 * Grants are keyed by the person's sign-in subject (the roster's externalId).
 * A grant counts only while they stay in the org: someone who leaves and
 * rejoins starts with none, and the grid shows what the server holds.
 */

import { useState } from "react";

import { useOrgProviderAccounts } from "@/hooks/org-provider-account-queries";
import { useSetOrgProviderAccountGrants } from "@/hooks/org-provider-account-service-hooks_gen";
import type { MyOrg } from "@/services/controlPlane/orgs";
import type { OrgMember } from "@/services/controlPlane/members";
import { orgAccountErrorMessage, type OrgAccount } from "@/types/orgProviderAccount";

export function AIAccountGrants({ org, members }: { org: MyOrg; members: OrgMember[] }) {
  const q = useOrgProviderAccounts(org.id);
  const set = useSetOrgProviderAccountGrants();
  // Pending edits by account: the set being sent, shown until the server answers.
  const [pending, setPending] = useState<Record<string, string[]>>({});
  const [error, setError] = useState<string | null>(null);

  const selected = (q.data?.accounts ?? []).filter((a) => a.access === "selected");
  if (!q.data?.callerIsAdmin || selected.length === 0) return null;

  const granted = (a: OrgAccount): string[] => pending[a.account.id] ?? a.grants.map((g) => g.userId);

  const toggle = async (a: OrgAccount, userId: string, on: boolean) => {
    const current = granted(a);
    const next = on ? [...new Set([...current, userId])] : current.filter((id) => id !== userId);
    setError(null);
    setPending((p) => ({ ...p, [a.account.id]: next }));
    try {
      await set.mutateAsync({ orgId: org.id, accountId: a.account.id, userIds: next });
    } catch (err) {
      setError(orgAccountErrorMessage(err, "Could not change who may use that key. Nothing was changed."));
    } finally {
      // Either way the list re-reads (the hook invalidates on success); drop the optimistic set.
      setPending((p) => {
        const rest = { ...p };
        delete rest[a.account.id];
        return rest;
      });
    }
  };

  return (
    <section aria-labelledby="ai-grants-heading" className="space-y-3 rounded-lg border border-border bg-card p-4" data-testid="ai-account-grants">
      <div>
        <h2 id="ai-grants-heading" className="text-sm font-semibold text-foreground">
          AI keys
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Who may use each of {org.name}'s keys that are open to selected people. Change a key's access on Settings › AI accounts.
        </p>
      </div>
      {error && (
        <p role="alert" className="rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink" data-testid="ai-grants-error">
          {error}
        </p>
      )}
      <div className="overflow-x-auto rounded-md border border-border/60 bg-background">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border/60 text-left text-xs text-muted-foreground">
              <th scope="col" className="px-3 py-2 font-medium">
                Member
              </th>
              {selected.map((a) => (
                <th key={a.account.id} scope="col" className="px-3 py-2 text-center font-medium">
                  <span className="block truncate text-foreground">{a.account.label}</span>
                  <span className="block truncate">{a.account.providerDisplayName}</span>
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {members.map((m) => {
              const name = m.name || m.email;
              return (
                <tr key={m.userId} className="border-b border-border/60 last:border-0">
                  <th scope="row" className="px-3 py-2 text-left font-normal text-foreground">
                    <span className="block truncate">{name}</span>
                    {m.name && <span className="block truncate text-xs text-muted-foreground">{m.email}</span>}
                  </th>
                  {selected.map((a) => {
                    const on = !!m.externalId && granted(a).includes(m.externalId);
                    return (
                      <td key={a.account.id} className="px-3 py-2 text-center">
                        <input
                          type="checkbox"
                          checked={on}
                          disabled={!m.externalId || a.account.id in pending}
                          title={m.externalId ? undefined : "This control plane does not say who this member signs in as yet."}
                          aria-label={`${name} may use ${a.account.label}`}
                          onChange={(e) => void toggle(a, m.externalId, e.target.checked)}
                          className="h-4 w-4 accent-primary"
                          data-testid="ai-grant-box"
                        />
                      </td>
                    );
                  })}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </section>
  );
}
