// Copyright (c) 2025 Reliant Labs

/**
 * The org's API keys, grouped by provider in the order the org's chats try
 * them (the In-order sequence of the org's tier, org-provider-accounts D4).
 *
 * An owner or admin sees each key whole (masked key, status, who may use it)
 * and acts on it: Rotate, Pause/Resume, Rename, change who may use it, move it
 * up or down, Delete. Pausing, rotating and deleting are never gated, so an
 * admin can always stop the org's spend. A member sees the provider, label and
 * status, and whether it is theirs to use.
 */

import { useState } from "react";
import { ArrowDown, ArrowUp, MoreHorizontal } from "lucide-react";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import { ProviderLogo } from "@/components/icons/ProviderLogo";
import {
  useDeleteOrgProviderAccount,
  useReorderOrgProviderAccounts,
  useUpdateOrgProviderAccount,
} from "@/hooks/org-provider-account-service-hooks_gen";
import { useAuthStore } from "@/store/authStore";
import type { MyOrg } from "@/services/controlPlane/orgs";
import {
  ACCESS_TO_PROTO,
  accessLabel,
  mayUse,
  orgAccountErrorMessage,
  type OrgAccount,
  type OrgAccountAccess,
} from "@/types/orgProviderAccount";
import { cn } from "@/lib/utils";

import { AccessOptions, RenameOrgKeyDialog, RotateOrgKeyDialog } from "./OrgKeyDialogs";

/** The key's state in words, or nothing when it is serving. A member cannot fix a key: they are told to ask. */
function statusText(a: OrgAccount, isAdmin: boolean): { text: string; ink: string } | null {
  switch (a.account.status) {
    case "available":
      return null;
    case "paused":
      return { text: "Paused", ink: "text-muted-foreground" };
    case "limited":
      return { text: "Rate-limited", ink: "text-warning-ink" };
    case "needs_sign_in":
      return {
        text: isAdmin ? "The provider rejected this key: rotate or remove it" : "Not working: an admin needs to fix it",
        ink: "text-destructive-ink",
      };
  }
}

function groupByProvider(accounts: OrgAccount[]): OrgAccount[][] {
  const groups = new Map<string, OrgAccount[]>();
  for (const a of accounts) groups.set(a.account.provider, [...(groups.get(a.account.provider) ?? []), a]);
  return [...groups.values()].map((g) => [...g].sort((x, y) => x.account.priority - y.account.priority));
}

interface OrgKeysSectionProps {
  org: MyOrg;
  accounts: OrgAccount[];
  isAdmin: boolean;
  /** Shown when there are no keys. */
  emptyAction?: React.ReactNode;
}

export function OrgKeysSection({ org, accounts, isAdmin, emptyAction }: OrgKeysSectionProps) {
  const userId = useAuthStore((s) => s.user?.id ?? null);
  const update = useUpdateOrgProviderAccount();
  const reorder = useReorderOrgProviderAccounts();
  const remove = useDeleteOrgProviderAccount();
  const [rotating, setRotating] = useState<OrgAccount | null>(null);
  const [renaming, setRenaming] = useState<OrgAccount | null>(null);
  const [deleting, setDeleting] = useState<OrgAccount | null>(null);
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const act = async (what: string, run: () => Promise<unknown>) => {
    setError(null);
    setMenuFor(null);
    try {
      await run();
    } catch (err) {
      setError(orgAccountErrorMessage(err, `Could not ${what}. Try again.`));
    }
  };

  const move = (group: OrgAccount[], index: number, by: -1 | 1) => {
    const ids = group.map((a) => a.account.id);
    const [moved] = ids.splice(index, 1);
    ids.splice(index + by, 0, moved);
    void act("reorder the keys", () => reorder.mutateAsync({ orgId: org.id, provider: group[0].account.provider, accountIds: ids }));
  };

  return (
    <section aria-labelledby="org-keys-heading" className="rounded-lg border border-border bg-card p-4" data-testid="org-keys">
      <h2 id="org-keys-heading" className="text-sm font-semibold text-foreground">
        API keys
      </h2>
      <p className="mt-1 text-sm text-muted-foreground">
        {isAdmin
          ? `Keys ${org.name} pays for. Members' chats in ${org.name} use them before their own accounts, in this order.`
          : `Keys ${org.name} pays for. Your chats in ${org.name} use the ones open to you before your own accounts.`}
      </p>
      {error && (
        <p role="alert" className="mt-3 rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink" data-testid="org-keys-error">
          {error}
        </p>
      )}
      {accounts.length === 0 ? (
        <div className="mt-4 space-y-3" data-testid="org-keys-empty">
          <p className="text-sm text-muted-foreground">
            {isAdmin
              ? `${org.name} has no API keys yet. Members' chats run on their own accounts until you add one.`
              : `${org.name} has no API keys yet. Your chats here run on your own accounts.`}
          </p>
          {emptyAction}
        </div>
      ) : (
        <div className="mt-4 space-y-4">
          {groupByProvider(accounts).map((group) => (
            <div key={group[0].account.provider} className="space-y-2">
              <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                <ProviderLogo provider={group[0].account.provider} />
                {group[0].account.providerDisplayName}
              </div>
              <ul className="divide-y divide-border/60 rounded-md border border-border/60 bg-background">
                {group.map((a, i) => {
                  const status = statusText(a, isAdmin);
                  const usable = mayUse(a, isAdmin, userId);
                  return (
                    <li key={a.account.id} className="flex flex-wrap items-center gap-x-3 gap-y-2 px-3 py-2.5" data-testid="org-key-row" data-account-id={a.account.id}>
                      <div className="min-w-0 flex-1">
                        <div className="flex flex-wrap items-baseline gap-x-2">
                          <span className="truncate text-sm font-medium text-foreground">{a.account.label}</span>
                          {isAdmin && a.account.maskedKey && <span className="font-mono text-xs text-muted-foreground">{a.account.maskedKey}</span>}
                          {status && (
                            <span className={cn("text-xs font-medium", status.ink)} data-testid="org-key-status">
                              {status.text}
                            </span>
                          )}
                        </div>
                        {!isAdmin && (
                          <p className="text-xs text-muted-foreground" data-testid="org-key-usable">
                            {usable === "yes" ? "Open to you" : usable === "maybe" ? "Open to the people an admin picked" : "Owners and admins only"}
                          </p>
                        )}
                      </div>
                      {isAdmin && (
                        <>
                          <select
                            aria-label={`Who may use ${a.account.label}`}
                            value={a.access}
                            onChange={(e) =>
                              void act("change who may use it", () =>
                                update.mutateAsync({ orgId: org.id, accountId: a.account.id, access: ACCESS_TO_PROTO[e.target.value as OrgAccountAccess] }),
                              )
                            }
                            className="rounded-md border border-border bg-background px-2 py-1 text-xs text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                            data-testid="org-key-access-select"
                          >
                            <AccessOptions orgName={org.name} />
                          </select>
                          {group.length > 1 && (
                            <span className="flex items-center">
                              <Button variant="ghost" size="xs" icon={<ArrowUp />} aria-label={`Move ${a.account.label} up`} disabled={i === 0 || reorder.isPending} onClick={() => move(group, i, -1)} />
                              <Button
                                variant="ghost"
                                size="xs"
                                icon={<ArrowDown />}
                                aria-label={`Move ${a.account.label} down`}
                                disabled={i === group.length - 1 || reorder.isPending}
                                onClick={() => move(group, i, 1)}
                              />
                            </span>
                          )}
                          <div className="relative">
                            <Button
                              variant="ghost"
                              size="xs"
                              icon={<MoreHorizontal />}
                              aria-label={`Actions for ${a.account.label}`}
                              aria-expanded={menuFor === a.account.id}
                              onClick={() => setMenuFor(menuFor === a.account.id ? null : a.account.id)}
                              data-testid="org-key-actions"
                            />
                            {menuFor === a.account.id && (
                              <div role="menu" className="absolute right-0 top-full z-20 mt-1 w-40 rounded-md border border-border bg-popover p-1 shadow-lg">
                                <MenuItem onClick={() => (setMenuFor(null), setRotating(a))}>Rotate key</MenuItem>
                                <MenuItem
                                  onClick={() =>
                                    void act(a.account.status === "paused" ? "resume the key" : "pause the key", () =>
                                      update.mutateAsync({ orgId: org.id, accountId: a.account.id, paused: a.account.status !== "paused" }),
                                    )
                                  }
                                >
                                  {a.account.status === "paused" ? "Resume" : "Pause"}
                                </MenuItem>
                                <MenuItem onClick={() => (setMenuFor(null), setRenaming(a))}>Rename</MenuItem>
                                <MenuItem danger onClick={() => (setMenuFor(null), setDeleting(a))}>
                                  Delete
                                </MenuItem>
                              </div>
                            )}
                          </div>
                        </>
                      )}
                      {!isAdmin && <span className="text-xs text-muted-foreground">{accessLabel(a.access, org.name)}</span>}
                    </li>
                  );
                })}
              </ul>
            </div>
          ))}
        </div>
      )}

      <RotateOrgKeyDialog account={rotating} org={org} onClose={() => setRotating(null)} />
      <RenameOrgKeyDialog account={renaming} org={org} onClose={() => setRenaming(null)} />
      <ConfirmationDialog
        open={deleting !== null}
        title={deleting ? `Delete ${deleting.account.label}?` : "Delete key?"}
        description={`The key is destroyed and nobody in ${org.name} can use it again. Chats on it move to another account at their next call.`}
        confirmLabel="Delete key"
        variant="danger"
        loading={remove.isPending}
        onCancel={() => setDeleting(null)}
        onConfirm={() => {
          const target = deleting;
          setDeleting(null);
          if (target) void act("delete the key", () => remove.mutateAsync({ orgId: org.id, accountId: target.account.id }));
        }}
      />
    </section>
  );
}

function MenuItem({ children, onClick, danger }: { children: React.ReactNode; onClick: () => void; danger?: boolean }) {
  return (
    <button
      type="button"
      role="menuitem"
      onClick={onClick}
      className={cn(
        "block w-full rounded px-2 py-1.5 text-left text-sm hover:bg-foreground/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
        danger ? "text-destructive-ink" : "text-foreground",
      )}
    >
      {children}
    </button>
  );
}
