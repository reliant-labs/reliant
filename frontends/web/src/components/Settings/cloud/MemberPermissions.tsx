/**
 * WHO IN THE ORG MAY DO WHAT.
 *
 * A grant is per (org, user, permission), and it is the whole of a person's
 * authority: no check consults a role any more, so "admin" is a PRESET — the
 * name for holding the full set — rather than a level that implies anything.
 * A member's authority can therefore be widened or narrowed one permission at
 * a time, without inventing a role for each combination.
 *
 * ── THE SET IS COMPLETE, NOT A DELTA ──────────────────────────────────
 *
 * Every edit sends the member's whole new set. That makes the call idempotent,
 * and it means two admins editing the same person cannot interleave into a
 * combination neither of them chose. It is also why a toggle has to send the
 * permissions that are STAYING, not just the one that moved.
 *
 * ── WHY DISABLE RATHER THAN LET IT FAIL ───────────────────────────────
 *
 * The server refuses a permission the caller does not hold, always. Disabling
 * those boxes is not the control — it is what stops the UI offering an edit it
 * already knows will be refused, which is the difference between "you cannot
 * grant this" and a round trip that ends in a toast.
 *
 * ── OPTIMISM, AND WHY IT IS REVERTED ON FAILURE ───────────────────────
 *
 * The checkbox moves immediately because a permissions screen with a spinner
 * per row is unusable. But a failed edit REVERTS it: a box left ticked after
 * the server refused is the worst outcome available here, because the operator
 * walks away believing someone has authority they do not have.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { Loader2 } from "lucide-react";

import {
  listMemberPermissions,
  updateMemberPermissions,
  type MemberPermissions as Member,
} from "@/services/controlPlane/memberPermissions";
import { SettingsPageHeader } from "../SettingsPageHeader";

// The org-administration permissions, in the order they are shown. Grouped by
// family so the two halves of a read/write pair sit together. Mirrors the
// control plane's per-org grant set (control-plane internal/orggrants
// OrgScopes): a grant missing here is one no admin can see or hand out.
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
  "secret:read",
  "secret:write",
  "domain:read",
  "domain:write",
  "cluster:manage",
  "token:read",
  "token:write",
  "identity:read",
  "identity:manage",
] as const;

// Holding this is what makes someone a permission administrator.
const ADMIN_PERMISSION = "token:write";

export function MemberPermissions() {
  const orgId = useActiveOrgStore((s) => s.effectiveId);
  // Separate lifetimes also protect A → B → A: A's first request must not
  // overwrite a newer roster merely because the selected id matches again.
  return <OrgMemberPermissions key={orgId ?? ""} orgId={orgId} />;
}

function OrgMemberPermissions({ orgId }: { orgId: string | null }) {
  const live = useRef(true);
  const isCurrent = useCallback(() => live.current && useActiveOrgStore.getState().effectiveId === orgId, [orgId]);
  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);
  const [members, setMembers] = useState<Member[]>([]);
  const [grantable, setGrantable] = useState<string[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const fetchMembers = useCallback(async () => {
    try {
      setError(null);
      if (!orgId) return;
      const res = await listMemberPermissions(orgId);
      if (!isCurrent()) return;
      setMembers(res.members);
      setGrantable(res.grantable);
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
    setLoading(true);
    void fetchMembers();
  }, [fetchMembers]);

  const canAdminister = grantable.includes(ADMIN_PERMISSION);

  const operations = useRef(new Map<string, number>());
  const toggle = async (member: Member, permission: string) => {
    if (!orgId || !isCurrent()) return;
    const operation = (operations.current.get(member.userId) ?? 0) + 1;
    operations.current.set(member.userId, operation);
    const isLatest = () => isCurrent() && operations.current.get(member.userId) === operation;
    const has = member.scopes.includes(permission);
    const next = has
      ? member.scopes.filter((s) => s !== permission)
      : [...member.scopes, permission];
    const previous = member.scopes;

    // Optimistic, then reverted below if the server refuses.
    setMembers((current) =>
      current.map((m) => (m.userId === member.userId ? { ...m, scopes: next } : m))
    );
    setError(null);
    try {
      const updated = await updateMemberPermissions(member.userId, next, orgId);
      if (!isLatest()) return;
      setMembers((current) =>
        current.map((m) =>
          m.userId === member.userId ? { ...m, scopes: updated.scopes } : m
        )
      );
    } catch (err) {
      if (!isLatest()) return;
      console.error("Failed to update member permissions:", err);
      setMembers((current) =>
        current.map((m) =>
          m.userId === member.userId ? { ...m, scopes: previous } : m
        )
      );
      setError(`Could not update ${member.email}'s permissions.`);
    }
  };

  return (
    <div className="space-y-6">
      <SettingsPageHeader
        title="Members"
        description="Control what each member of your organization is allowed to do. A token acting on someone's behalf can never carry more permissions than that person holds."
      />

      {error && (
        <div className="rounded-lg border border-destructive/40 bg-destructive/10 p-3">
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

      <div className="rounded-lg border border-border bg-card p-6 space-y-4">
        {loading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="w-4 h-4 animate-spin" />
            Loading members...
          </div>
        ) : members.length === 0 ? (
          <p className="text-sm text-muted-foreground">No members.</p>
        ) : (
          <div className="space-y-5">
            {members.map((member) => (
              <div key={member.userId} className="space-y-2">
                <div className="flex items-baseline gap-2">
                  <p className="text-sm font-medium">{member.email}</p>
                  <span className="text-xs text-muted-foreground">
                    {member.role}
                  </span>
                </div>
                <div className="flex flex-wrap gap-x-4 gap-y-2">
                  {ORG_PERMISSIONS.map((permission) => {
                    // Disabled when the caller could not hand it out anyway.
                    const allowed = canAdminister && grantable.includes(permission);
                    return (
                      <label
                        key={permission}
                        className="flex items-center gap-2 text-xs text-muted-foreground"
                      >
                        <input
                          type="checkbox"
                          // The member is in the label so a screen reader —
                          // and a test — can tell two rows' identical
                          // permission boxes apart.
                          aria-label={`${member.email} ${permission}`}
                          checked={member.scopes.includes(permission)}
                          disabled={!allowed}
                          onChange={() => toggle(member, permission)}
                          className="accent-primary"
                        />
                        <span className="font-mono">{permission}</span>
                      </label>
                    );
                  })}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
