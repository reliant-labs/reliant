// Copyright (c) 2025 Reliant Labs

import { useState } from "react";

import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import Modal from "@/components/forge-ui/modal";
import { formatRelativeTime } from "@/lib/relativeTime";
import { toast } from "@/lib/toast-manager";
import {
  inviteMember,
  memberErrorMessage,
  revokeInvitation,
  roleName,
  type InviteResult,
  type OrgInvitation,
  type OrgRole,
} from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { InviteLinkPanel } from "./InviteDialog";
import { useInvitations, useRefreshMembers } from "./useMembers";

/**
 * Invitations nobody has answered yet: admins and owners only, because they
 * are other people's addresses. Resend is a second InviteMember for the same
 * address, which rotates the link, extends the expiry and sends it again.
 */
export function PendingInvitations({ org }: { org: MyOrg }) {
  const invitations = useInvitations(org.id);
  const refresh = useRefreshMembers(org.id);
  const [busy, setBusy] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<OrgInvitation | null>(null);
  const [resent, setResent] = useState<InviteResult | null>(null);
  const [error, setError] = useState<string | null>(null);

  // An owner invitation can only be resent or revoked by an owner.
  const canTouch = (i: OrgInvitation) => i.role !== "owner" || org.role === "owner";

  const resend = async (inv: OrgInvitation) => {
    setBusy(inv.id);
    setError(null);
    try {
      const result = await inviteMember(org.id, inv.email, inv.role as OrgRole);
      if (result.emailSent) toast.success(`Sent the invitation to ${inv.email} again.`);
      else setResent(result);
      await refresh();
    } catch (err) {
      setError(memberErrorMessage(err, "invite", `Could not resend the invitation to ${inv.email}.`));
    } finally {
      setBusy(null);
    }
  };

  const confirmRevoke = async () => {
    if (!revoking) return;
    const inv = revoking;
    setBusy(inv.id);
    setError(null);
    try {
      await revokeInvitation(org.id, inv.id);
      toast.success(`Revoked the invitation to ${inv.email}. Its link no longer works.`);
      await refresh();
    } catch (err) {
      setError(memberErrorMessage(err, "revoke", `Could not revoke the invitation to ${inv.email}.`));
    } finally {
      setRevoking(null);
      setBusy(null);
    }
  };

  const pending = (invitations.data ?? []).filter((i) => i.status === "pending");

  return (
    <section className="rounded-lg border border-border bg-card" aria-labelledby="members-invites-heading" data-testid="members-invitations">
      <h3 id="members-invites-heading" className="border-b border-border px-4 py-3 text-sm font-semibold text-foreground">
        Pending invitations
      </h3>
      {error && (
        <p role="alert" className="border-b border-border px-4 py-3 text-sm text-destructive-ink">
          {error}
        </p>
      )}
      {invitations.isPending ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" role="status">
          Loading invitations…
        </p>
      ) : invitations.isError ? (
        <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">
          {memberErrorMessage(invitations.error, "load", "Could not load this organization's invitations.")}
        </p>
      ) : pending.length === 0 ? (
        <p className="px-4 py-3 text-sm text-muted-foreground">No invitations are waiting for an answer.</p>
      ) : (
        <ul className="divide-y divide-border">
          {pending.map((inv) => (
            <li key={inv.id} className="flex flex-wrap items-center gap-3 px-4 py-3" data-testid={`invitation-${inv.id}`}>
              <div className="min-w-0 flex-1">
                <p className="truncate text-sm font-medium text-foreground">{inv.email}</p>
                <p className="truncate text-xs text-muted-foreground">
                  {roleName(inv.role)}
                  {inv.invitedByEmail && ` · invited by ${inv.invitedByEmail}`}
                  {inv.expiresAt && ` · expires ${formatRelativeTime(inv.expiresAt.toISOString())}`}
                </p>
              </div>
              {canTouch(inv) && (
                <div className="flex items-center gap-2">
                  <Button variant="outline" size="sm" loading={busy === inv.id && !revoking} disabled={busy !== null} onClick={() => void resend(inv)} aria-label={`Resend the invitation to ${inv.email}`}>
                    Resend
                  </Button>
                  <Button variant="ghost" size="sm" disabled={busy !== null} onClick={() => setRevoking(inv)} aria-label={`Revoke the invitation to ${inv.email}`}>
                    Revoke
                  </Button>
                </div>
              )}
            </li>
          ))}
        </ul>
      )}

      <ConfirmationDialog
        open={revoking !== null}
        title="Revoke this invitation?"
        description={revoking ? `The link sent to ${revoking.email} stops working, and its seat is freed. You can invite them again later.` : undefined}
        confirmLabel="Revoke"
        loading={busy !== null}
        onConfirm={() => void confirmRevoke()}
        onCancel={() => setRevoking(null)}
      />

      <Modal open={resent !== null} onClose={() => setResent(null)} title="Share the invitation link" size="md">
        {resent && <InviteLinkPanel result={resent} email={resent.invitation?.email ?? ""} />}
      </Modal>
    </section>
  );
}
