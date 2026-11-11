// Copyright (c) 2025 Reliant Labs

import { useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { UserMinus } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import { formatAbsoluteTime } from "@/lib/relativeTime";
import { toast } from "@/lib/toast-manager";
import {
  canAdministerMembers,
  memberErrorMessage,
  removeMember,
  roleName,
  updateMemberRole,
  ORG_ROLES,
  type OrgMember,
  type OrgRole,
} from "@/services/controlPlane/members";
import { orgKeys, type MyOrg } from "@/services/controlPlane/orgs";
import { useAuthStore } from "@/store/authStore";

import { useRefreshMembers, type MembersQuery } from "./useMembers";

/** A member as a person reads them: their name, else their address. */
export function memberLabel(m: Pick<OrgMember, "name" | "email" | "userId">): string {
  return m.name || m.email || m.userId;
}

/** Whether the signed-in person is `m`. */
function useIsSelf(): (m: OrgMember) => boolean {
  const user = useAuthStore((s) => s.user);
  return (m) => !!user && (m.userId === user.id || (!!user.email && m.email.toLowerCase() === user.email.toLowerCase()));
}

/**
 * The roles `callerRole` may give or take away from someone who is `targetRole`
 * (or null when the caller may not change them at all). Only an owner touches
 * owners, or makes one. A courtesy: the server enforces the same.
 */
export function assignableRoles(callerRole: string, targetRole: string): OrgRole[] | null {
  if (!canAdministerMembers(callerRole)) return null;
  if (callerRole === "owner") return [...ORG_ROLES];
  if (targetRole === "owner") return null;
  return ORG_ROLES.filter((r) => r !== "owner");
}

const selectClass =
  "rounded-md border border-border bg-background px-2 py-1 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring disabled:opacity-60";

export function MemberRoster({ org, query }: { org: MyOrg; query: MembersQuery }) {
  const isSelf = useIsSelf();
  const refresh = useRefreshMembers(org.id);
  const qc = useQueryClient();
  const [pendingRole, setPendingRole] = useState<string | null>(null);
  const [removing, setRemoving] = useState<OrgMember | null>(null);
  const [removeBusy, setRemoveBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const members = query.data?.pages.flatMap((p) => p.members) ?? [];

  const changeRole = async (member: OrgMember, role: OrgRole) => {
    if (role === member.role) return;
    setPendingRole(member.userId);
    setError(null);
    try {
      await updateMemberRole(org.id, member.userId, role);
      toast.success(`${memberLabel(member)} is now ${role === "member" ? "a member" : `an ${role}`}. Their permissions were reset to match.`);
      await refresh();
      // Your own role is also what the org switcher and this page gate on.
      if (isSelf(member)) await qc.invalidateQueries({ queryKey: orgKeys.mine });
    } catch (err) {
      setError(memberErrorMessage(err, "role", `Could not change ${memberLabel(member)}'s role.`));
    } finally {
      setPendingRole(null);
    }
  };

  const confirmRemove = async () => {
    if (!removing) return;
    setRemoveBusy(true);
    setError(null);
    try {
      const revoked = await removeMember(org.id, removing.userId);
      const tokens = revoked > 0 ? ` ${revoked} access ${revoked === 1 ? "token" : "tokens"} they made for ${org.name} ${revoked === 1 ? "was" : "were"} revoked.` : "";
      toast.success(`Removed ${memberLabel(removing)} from ${org.name}.${tokens}`);
      setRemoving(null);
      await refresh();
    } catch (err) {
      setRemoving(null);
      setError(memberErrorMessage(err, "remove", `Could not remove ${memberLabel(removing)}.`));
    } finally {
      setRemoveBusy(false);
    }
  };

  return (
    <section className="rounded-lg border border-border bg-card" aria-labelledby="members-roster-heading">
      <h3 id="members-roster-heading" className="border-b border-border px-4 py-3 text-sm font-semibold text-foreground">
        People
      </h3>

      {error && (
        <p role="alert" className="border-b border-border px-4 py-3 text-sm text-destructive-ink" data-testid="members-error">
          {error}
        </p>
      )}

      {query.isPending ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" role="status">
          Loading members…
        </p>
      ) : query.isError ? (
        <div className="flex items-center gap-3 px-4 py-3" role="alert">
          <p className="flex-1 text-sm text-muted-foreground">{memberErrorMessage(query.error, "load", "Could not load the members of this organization.")}</p>
          <Button variant="outline" size="sm" onClick={() => void query.refetch()}>
            Try again
          </Button>
        </div>
      ) : (
        <ul className="divide-y divide-border" data-testid="members-roster">
          {members.map((m) => {
            const self = isSelf(m);
            const roles = assignableRoles(org.role, m.role);
            // Removing yourself is leaving (below), and an admin cannot remove an owner.
            const canRemove = !self && roles !== null;
            return (
              <li key={m.userId} className="flex flex-wrap items-center gap-3 px-4 py-3" data-testid={`member-${m.userId}`}>
                <Avatar member={m} />
                <div className="min-w-0 flex-1">
                  <p className="flex items-center gap-2 truncate text-sm font-medium text-foreground">
                    <span className="truncate">{memberLabel(m)}</span>
                    {self && <span className="text-xs font-normal text-muted-foreground">(you)</span>}
                    {m.isFounder && <Badge label="Founder" size="sm" />}
                  </p>
                  <p className="truncate text-xs text-muted-foreground">
                    {m.name && m.email ? `${m.email} · ` : ""}
                    {m.joinedAt ? `Joined ${formatAbsoluteTime(m.joinedAt.toISOString())}` : ""}
                  </p>
                </div>
                {roles ? (
                  <select
                    aria-label={`Role for ${memberLabel(m)}`}
                    value={m.role}
                    disabled={pendingRole === m.userId}
                    onChange={(e) => void changeRole(m, e.target.value as OrgRole)}
                    className={selectClass}
                    data-testid={`member-role-${m.userId}`}
                  >
                    {/* The current role stays listed even when this caller could not assign it. */}
                    {(roles.includes(m.role as OrgRole) ? roles : [m.role as OrgRole, ...roles]).map((r) => (
                      <option key={r} value={r}>
                        {roleName(r)}
                      </option>
                    ))}
                  </select>
                ) : (
                  <Badge label={roleName(m.role)} variant={m.role === "owner" ? "info" : "neutral"} />
                )}
                {canRemove && (
                  <Button
                    variant="ghost"
                    size="sm"
                    icon={<UserMinus className="h-4 w-4" aria-hidden="true" />}
                    aria-label={`Remove ${memberLabel(m)}`}
                    onClick={() => setRemoving(m)}
                    data-testid={`member-remove-${m.userId}`}
                  />
                )}
              </li>
            );
          })}
        </ul>
      )}

      {query.hasNextPage && (
        <div className="border-t border-border px-4 py-3">
          <Button variant="outline" size="sm" loading={query.isFetchingNextPage} onClick={() => void query.fetchNextPage()}>
            Show more members
          </Button>
        </div>
      )}

      <ConfirmationDialog
        open={removing !== null}
        title={removing ? `Remove ${memberLabel(removing)}?` : "Remove member?"}
        description={`They lose access to ${org.name} at once. Access tokens they made for this organization, including ones CI uses, are revoked. They keep their own account, machines and chats.`}
        confirmLabel="Remove"
        loading={removeBusy}
        onConfirm={() => void confirmRemove()}
        onCancel={() => setRemoving(null)}
      />
    </section>
  );
}

function Avatar({ member }: { member: OrgMember }) {
  const initial = memberLabel(member).charAt(0).toUpperCase() || "?";
  if (member.avatarUrl) {
    return <img src={member.avatarUrl} alt="" className="h-8 w-8 shrink-0 rounded-full border border-border object-cover" referrerPolicy="no-referrer" />;
  }
  return (
    <span aria-hidden="true" className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full border border-border bg-background text-xs font-medium text-muted-foreground">
      {initial}
    </span>
  );
}
