/**
 * WHO IN THE ORG MAY DO WHAT, AND WHERE.
 *
 * A grant is per (org, user, permission), and it is the whole of a person's
 * authority: no check consults a role any more, so "admin" is a PRESET — the
 * name for holding the full set — rather than a level that implies anything.
 * A grant is held either ORG-WIDE or on ONE ENVIRONMENT (control-plane
 * docs/design/orgs-and-members.md §4), so this is a people × environments
 * grid: a row per member, an "Org-wide" column, then a column per
 * environment. What someone may do on an environment is the union of the two.
 *
 * ── THE SET IS COMPLETE, NOT A DELTA ──────────────────────────────────
 *
 * Every edit sends the member's whole new set, org-wide AND every
 * environment's, in one UpdateOrgMemberGrants. That makes the call idempotent,
 * and it means two admins editing the same person cannot interleave into a
 * combination neither of them chose. It is also why a toggle has to send the
 * permissions that are STAYING, not just the one that moved, and why
 * `environment_grants` is sent even when empty: leaving it out is how an old
 * client says "leave the environments alone".
 *
 * ── WHY DISABLE RATHER THAN LET IT FAIL ───────────────────────────────
 *
 * The server refuses a permission the caller does not hold — org-wide, or on
 * that environment — always. Disabling those boxes is not the control; it is
 * what stops the UI offering an edit it already knows will be refused, and the
 * tooltip says which rule refused it.
 *
 * ── OPTIMISM, AND WHY IT IS REVERTED ON FAILURE ───────────────────────
 *
 * The checkbox moves immediately because a permissions screen with a spinner
 * per click is unusable. But a failed edit REVERTS it: a box left ticked after
 * the server refused is the worst outcome available here, because the operator
 * walks away believing someone has authority they do not have.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { AlertTriangle, Loader2, Lock } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Tag from "@/components/forge-ui/tag";
import Tooltip from "@/components/forge-ui/tooltip";
import { cn } from "@/lib/utils";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import {
  listMemberPermissions,
  memberPermissionErrorMessage,
  updateMemberPermissions,
  type EnvironmentGrant,
  type GrantEnvironment,
  type MemberPermissions as Member,
} from "@/services/controlPlane/memberPermissions";
import { SettingsPageHeader } from "../SettingsPageHeader";

// The org permissions, in the order they are shown, grouped by family so the
// halves of a read/write pair sit together. Mirrors the control plane's per-org
// grant set (control-plane internal/orggrants OrgScopes): a grant missing here
// is one no admin can see or hand out. A grant the server reports that is not
// listed is still kept on save, because the set sent is the member's own set.
//
// identity:read / identity:manage administer the END USERS of the org's apps
// (invite, roles, lock, password-set codes) — their own product, deliberately
// not implied by deploy:write.
//
// reliant:api, daemon:connect, llm:invoke, proxy:port and mcp:connector are
// deliberately absent: they are every user's ordinary authority over their own
// resources, never granted per-org. Showing them would imply a gate that does
// not exist, and suggest that unticking one could take away someone's own
// daemon.
const ORG_PERMISSIONS = [
  "deploy:read",
  "deploy:write",
  "deploy:config:read",
  "secret:read",
  "secret:write",
  "domain:read",
  "domain:write",
  "db:query",
  "storage:read",
  "obs:write",
  "billing:read",
  "cluster:manage",
  "token:read",
  "token:write",
  "identity:read",
  "identity:manage",
] as const;

// Holding this is what makes someone a permission administrator.
const ADMIN_PERMISSION = "token:write";

// Roles whose members hold the whole set org-wide by preset. Narrowing one is
// a role change (Members tab), not a grant edit, so their rows are read-only.
const PRESET_ROLES = new Set(["owner", "admin"]);

// What `forge login` and the credential helper ask for (forge
// internal/cloud LoginScopes). A token that resolves to nothing org-wide is
// refused when it is minted (orggrants.CarriesNothingOrgWide), so someone
// whose permissions are ALL environment-bound needs one of these org-wide to
// use them from forge; the console needs none.
const FORGE_LOGIN_SCOPES = [
  "deploy:read",
  "deploy:write",
  "secret:read",
  "secret:write",
  "domain:read",
  "domain:write",
];
// The read the hint offers: the narrowest grant that unblocks the mint.
const HINT_READ = "deploy:read";

function byDisplayOrder(scopes: string[]): string[] {
  const rank = (s: string) => {
    const i = (ORG_PERMISSIONS as readonly string[]).indexOf(s);
    return i === -1 ? ORG_PERMISSIONS.length : i;
  };
  return [...scopes].sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
}

function envScopesOf(member: Member, envId: string): string[] {
  return member.environmentGrants.find((g) => g.environmentId === envId)?.scopes ?? [];
}

function withEnvScopes(member: Member, envId: string, scopes: string[]): EnvironmentGrant[] {
  const rest = member.environmentGrants.filter((g) => g.environmentId !== envId);
  return scopes.length > 0 ? [...rest, { environmentId: envId, scopes }] : rest;
}

function toggled(scopes: string[], scope: string): string[] {
  return scopes.includes(scope) ? scopes.filter((s) => s !== scope) : [...scopes, scope];
}

function isPreset(member: Member): boolean {
  return PRESET_ROLES.has(member.role);
}

/**
 * The environment-only trap: every grant bound to an environment and nothing
 * org-wide that forge's login asks for. The console honours such grants, but
 * a token for them is refused at the mint.
 */
function needsOrgWideRead(member: Member): boolean {
  if (isPreset(member)) return false;
  const hasBound = member.environmentGrants.some((g) => g.scopes.length > 0);
  return hasBound && !member.scopes.some((s) => FORGE_LOGIN_SCOPES.includes(s));
}

/**
 * `embedded`: rendered as the Permissions tab of the Members page
 * (members/MembersPage.tsx), which owns the page heading.
 */
export function MemberPermissions({ embedded = false }: { embedded?: boolean } = {}) {
  const orgId = useActiveOrgStore((s) => s.effectiveId);
  // Separate lifetimes also protect A → B → A: A's first request must not
  // overwrite a newer roster merely because the selected id matches again.
  return <OrgMemberPermissions key={orgId ?? ""} orgId={orgId} embedded={embedded} />;
}

interface Edit {
  scopes: string[];
  environmentGrants: EnvironmentGrant[];
}

function OrgMemberPermissions({ orgId, embedded }: { orgId: string | null; embedded: boolean }) {
  const live = useRef(true);
  const isCurrent = useCallback(() => live.current && useActiveOrgStore.getState().effectiveId === orgId, [orgId]);
  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);
  const [members, setMembers] = useState<Member[]>([]);
  const [grantable, setGrantable] = useState<string[]>([]);
  const [environments, setEnvironments] = useState<GrantEnvironment[]>([]);
  const [environmentScopes, setEnvironmentScopes] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState<Record<string, number>>({});

  const fetchMembers = useCallback(async () => {
    try {
      setError(null);
      if (!orgId) return;
      const res = await listMemberPermissions(orgId);
      if (!isCurrent()) return;
      setMembers(res.members);
      setGrantable(res.grantable);
      setEnvironments(res.environments);
      setEnvironmentScopes(byDisplayOrder(res.environmentScopes));
    } catch (err) {
      if (!isCurrent()) return;
      console.error("Failed to load member permissions:", err);
      setError("Could not load member permissions.");
    } finally {
      if (isCurrent()) setLoading(false);
    }
  }, [isCurrent, orgId]);

  useEffect(() => {
    setMembers([]);
    setGrantable([]);
    setEnvironments([]);
    setEnvironmentScopes([]);
    setLoading(true);
    void fetchMembers();
  }, [fetchMembers]);

  const canAdminister = grantable.includes(ADMIN_PERMISSION);

  const operations = useRef(new Map<string, number>());
  const save = async (member: Member, next: Edit) => {
    if (!orgId || !isCurrent()) return;
    const operation = (operations.current.get(member.userId) ?? 0) + 1;
    operations.current.set(member.userId, operation);
    const isLatest = () => isCurrent() && operations.current.get(member.userId) === operation;
    const previous: Edit = { scopes: member.scopes, environmentGrants: member.environmentGrants };
    const apply = (edit: Edit) =>
      setMembers((current) =>
        current.map((m) => (m.userId === member.userId ? { ...m, ...edit } : m))
      );
    const bump = (by: number) =>
      setSaving((s) => ({ ...s, [member.userId]: Math.max(0, (s[member.userId] ?? 0) + by) }));

    // Only the environments this grid shows are sent. A grant on one the
    // server no longer lists (deleted) is inert, and naming it would be
    // refused as an environment outside the org.
    const known = new Set(environments.map((e) => e.id));
    const environmentGrants = next.environmentGrants.filter((g) => known.has(g.environmentId));

    // Optimistic, then reverted below if the server refuses.
    apply({ scopes: next.scopes, environmentGrants });
    setError(null);
    bump(1);
    try {
      const updated = await updateMemberPermissions(member.userId, next.scopes, orgId, environmentGrants);
      if (!isLatest()) return;
      apply({ scopes: updated.scopes, environmentGrants: updated.environmentGrants });
    } catch (err) {
      if (!isLatest()) return;
      console.error("Failed to update member permissions:", err);
      apply(previous);
      setError(memberPermissionErrorMessage(err, member.email));
    } finally {
      if (isCurrent()) bump(-1);
    }
  };

  const toggleOrgWide = (member: Member, scope: string) =>
    save(member, { scopes: toggled(member.scopes, scope), environmentGrants: member.environmentGrants });

  const toggleOnEnv = (member: Member, envId: string, scope: string) =>
    save(member, {
      scopes: member.scopes,
      environmentGrants: withEnvScopes(member, envId, toggled(envScopesOf(member, envId), scope)),
    });

  // Why a box cannot be changed, or null when it can. The first reason that
  // applies wins, so the tooltip names the rule the operator can act on.
  const lockedReason = (member: Member): string | null => {
    if (!canAdminister) return "Changing permissions needs token:write, which you don't hold.";
    if (isPreset(member)) {
      return `${member.role === "owner" ? "Owners" : "Admins"} hold every permission org-wide. Change their role on the Members tab to narrow it.`;
    }
    return null;
  };
  const orgBlock = (member: Member, scope: string): string | null =>
    lockedReason(member) ??
    (grantable.includes(scope) ? null : `You can't grant ${scope}: you can only grant what you hold yourself.`);
  const envBlock = (member: Member, env: GrantEnvironment, scope: string): string | null => {
    if (member.scopes.includes(scope)) {
      return `Held org-wide, which covers ${env.name}. Clear it under Org-wide to grant it on chosen environments only.`;
    }
    return (
      lockedReason(member) ??
      (env.grantable.includes(scope)
        ? null
        : `You can't grant ${scope} on ${env.name}: you hold it neither org-wide nor on ${env.name}.`)
    );
  };

  return (
    <div className="space-y-6">
      {embedded ? (
        <p className="text-sm text-muted-foreground">
          What each member may do, one permission at a time: across the organization, or on one environment only. Changing someone&apos;s role resets these to that role&apos;s set. A token acting on someone&apos;s behalf can never carry more permissions than that person holds.
        </p>
      ) : (
        <SettingsPageHeader
          title="Members"
          description="Control what each member of your organization is allowed to do, across the organization or on one environment. A token acting on someone's behalf can never carry more permissions than that person holds."
        />
      )}

      {error && (
        <div role="alert" className="rounded-lg border border-destructive/40 bg-destructive/10 p-3">
          <p className="text-sm text-destructive-ink">{error}</p>
        </div>
      )}

      {!loading && !canAdminister && (
        <p className="text-xs text-muted-foreground">
          You can only view these. An organization admin with the{" "}
          <span className="font-mono">token:write</span> permission can change
          them.
        </p>
      )}

      <div className="rounded-lg border border-border bg-card">
        {loading ? (
          <div className="flex items-center gap-2 p-6 text-sm text-muted-foreground">
            <Loader2 className="w-4 h-4 animate-spin" />
            Loading members...
          </div>
        ) : members.length === 0 ? (
          <p className="p-6 text-sm text-muted-foreground">No members.</p>
        ) : (
          // The grid scrolls sideways under a pinned member column, so an org
          // with many environments stays readable at any width.
          <div className="overflow-x-auto rounded-lg">
            <table className="min-w-full border-separate border-spacing-0 text-left">
              <caption className="sr-only">Member permissions, org-wide and per environment</caption>
              <thead>
                <tr>
                  <th scope="col" className="sticky left-0 z-20 w-64 min-w-64 border-b border-r border-border bg-card px-4 py-3 text-xs font-medium text-muted-foreground">
                    Member
                  </th>
                  <th scope="col" className="border-b border-border px-4 py-3 align-bottom">
                    <span className="block text-xs font-medium text-foreground">Org-wide</span>
                    <span className="block text-xs text-muted-foreground">Every environment</span>
                  </th>
                  {environments.map((env) => (
                    <th key={env.id} scope="col" className="border-b border-border px-4 py-3 align-bottom">
                      <span className="flex items-center gap-2 whitespace-nowrap">
                        <span className="text-xs font-medium text-foreground">{env.name}</span>
                        {env.protected && (
                          <Tooltip content="Protected environment: changing its secrets needs a step-up confirmation." focusable={false}>
                            <Tag icon={<Lock className="h-3 w-3" aria-hidden="true" />}>protected</Tag>
                          </Tooltip>
                        )}
                      </span>
                      <span className="block text-xs text-muted-foreground">This environment only</span>
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {members.map((member) => {
                  const hint = needsOrgWideRead(member);
                  const canFix = hint && lockedReason(member) === null && grantable.includes(HINT_READ);
                  return (
                    <tr key={member.userId} data-testid={`permissions-row-${member.userId}`}>
                      <th scope="row" className="sticky left-0 z-10 w-64 min-w-64 border-b border-r border-border bg-card px-4 py-3 align-top font-normal">
                        <span className="flex items-center gap-2">
                          <span className="truncate text-sm font-medium text-foreground" title={member.email}>
                            {member.email}
                          </span>
                          {(saving[member.userId] ?? 0) > 0 && (
                            <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin text-muted-foreground" aria-label="Saving" />
                          )}
                        </span>
                        <span className="block text-xs text-muted-foreground">
                          {member.role}
                          {isPreset(member) && " · every permission, by role"}
                        </span>
                        {hint && (
                          <span className="mt-2 block space-y-1.5" data-testid={`permissions-hint-${member.userId}`}>
                            <span className="flex gap-1.5 text-xs text-warning-ink">
                              <AlertTriangle className="mt-0.5 h-3 w-3 shrink-0" aria-hidden="true" />
                              <span>
                                These work in the console. To use them from forge or the CLI, {member.email} also
                                needs one permission held org-wide; <span className="font-mono">{HINT_READ}</span> is
                                enough. A token that could act only inside one environment is refused when it is minted.
                              </span>
                            </span>
                            {canFix && (
                              <Button variant="outline" size="xs" onClick={() => void toggleOrgWide(member, HINT_READ)}>
                                Grant {HINT_READ} org-wide
                              </Button>
                            )}
                          </span>
                        )}
                      </th>
                      <td className="border-b border-border px-4 py-3 align-top">
                        <div className="grid min-w-[18rem] grid-cols-2 gap-x-4 gap-y-1.5">
                          {ORG_PERMISSIONS.map((scope) => (
                            <PermissionToggle
                              key={scope}
                              scope={scope}
                              // The member is in the label so a screen reader —
                              // and a test — can tell two rows' identical
                              // permission boxes apart.
                              label={`${member.email} ${scope}`}
                              checked={member.scopes.includes(scope)}
                              blockedBy={orgBlock(member, scope)}
                              onToggle={() => void toggleOrgWide(member, scope)}
                            />
                          ))}
                        </div>
                      </td>
                      {environments.map((env) => {
                        const bound = envScopesOf(member, env.id);
                        return (
                          <td key={env.id} className="border-b border-border px-4 py-3 align-top">
                            <div className="flex min-w-[10rem] flex-col gap-1.5">
                              {environmentScopes.map((scope) => (
                                <PermissionToggle
                                  key={scope}
                                  scope={scope}
                                  label={`${member.email} on ${env.name}: ${scope}`}
                                  checked={member.scopes.includes(scope) || bound.includes(scope)}
                                  blockedBy={envBlock(member, env, scope)}
                                  onToggle={() => void toggleOnEnv(member, env.id, scope)}
                                />
                              ))}
                            </div>
                          </td>
                        );
                      })}
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </div>

      {!loading && members.length > 0 && (
        <p className="text-xs text-muted-foreground">
          {environments.length === 0
            ? "Once your organization has an environment, it gets a column here, so you can grant a permission on that environment only."
            : "A permission under Org-wide applies on every environment. One ticked under an environment applies there only, so a release manager can hold deploy:write on production and nothing wider. You can grant on an environment only what you hold org-wide or on that environment."}
        </p>
      )}
    </div>
  );
}

function PermissionToggle({
  scope,
  label,
  checked,
  blockedBy,
  onToggle,
}: {
  scope: string;
  label: string;
  checked: boolean;
  blockedBy: string | null;
  onToggle: () => void;
}) {
  const box = (
    <label
      className={cn(
        "flex items-center gap-2 text-xs",
        blockedBy ? "text-muted-foreground" : "cursor-pointer text-foreground"
      )}
    >
      <input
        type="checkbox"
        aria-label={label}
        checked={checked}
        disabled={blockedBy !== null}
        onChange={onToggle}
        className="accent-primary"
      />
      <span className="whitespace-nowrap font-mono">{scope}</span>
    </label>
  );
  if (!blockedBy) return box;
  // Not a tab stop: a large grid of read-only boxes would bury the editable
  // ones. The reason still describes the box for assistive technology, and
  // the page says in words when everything is read-only.
  return (
    <Tooltip content={blockedBy} focusable={false} triggerClassName="flex">
      {box}
    </Tooltip>
  );
}
