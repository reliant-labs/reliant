// Copyright (c) 2025 Reliant Labs

import { useEffect, useId, useState } from "react";
import { Check, Copy } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import Modal from "@/components/forge-ui/modal";
import {
  inviteMember,
  memberErrorMessage,
  roleName,
  seatsFull,
  ORG_ROLES,
  type InviteResult,
  type OrgRole,
  type SeatUsage,
} from "@/services/controlPlane/members";
import type { MyOrg } from "@/services/controlPlane/orgs";

import { SeatCount } from "./SeatCount";
import { useRefreshMembers } from "./useMembers";

/** What each role is for, in the invite form. */
const ROLE_HINT: Record<OrgRole, string> = {
  member: "Works in the organization with the permissions you give them.",
  admin: "Every permission, and may invite and remove members.",
  owner: "Everything an admin can do, and may manage owners.",
};

// Good enough to catch a typo; the server is the judge of an address.
const EMAIL = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

/**
 * Invite someone by email, with a role. On success it says whether the email
 * went out and, either way, offers the link to copy: mail can be off on a
 * deployment or land in spam, and the link is useless to anyone whose
 * verified address is not the invited one.
 */
export function InviteDialog({ open, onClose, org, seats }: { open: boolean; onClose: () => void; org: MyOrg; seats: SeatUsage | null }) {
  const refresh = useRefreshMembers(org.id);
  const [email, setEmail] = useState("");
  const [role, setRole] = useState<OrgRole>("member");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [result, setResult] = useState<InviteResult | null>(null);
  const emailId = useId();
  const roleId = useId();

  // Each opening starts clean.
  useEffect(() => {
    if (!open) return;
    setEmail("");
    setRole("member");
    setError(null);
    setResult(null);
  }, [open]);

  const roles = org.role === "owner" ? ORG_ROLES : ORG_ROLES.filter((r) => r !== "owner");
  const valid = EMAIL.test(email.trim());

  const submit = async () => {
    if (!valid || busy) return;
    setBusy(true);
    setError(null);
    try {
      const res = await inviteMember(org.id, email, role);
      setResult(res);
      await refresh();
    } catch (err) {
      setError(memberErrorMessage(err, "invite", "Could not send the invitation. Try again."));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open={open}
      onClose={onClose}
      title={result ? "Invitation ready" : `Invite someone to ${org.name}`}
      description={result ? undefined : "They join when they accept, signed in with this email address."}
      size="md"
      footer={
        result ? (
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" loading={busy} disabled={!valid} onClick={() => void submit()} data-testid="invite-submit">
              Send invitation
            </Button>
          </>
        )
      }
    >
      {result ? (
        <InviteLinkPanel result={result} email={result.invitation?.email ?? email.trim()} />
      ) : (
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            void submit();
          }}
        >
          <div className="space-y-1.5">
            <label htmlFor={emailId} className="block text-sm font-medium text-foreground">
              Email address
            </label>
            <Input
              id={emailId}
              type="email"
              autoComplete="off"
              autoFocus
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              placeholder="teammate@example.com"
              data-testid="invite-email"
            />
          </div>
          <div className="space-y-1.5">
            <label htmlFor={roleId} className="block text-sm font-medium text-foreground">
              Role
            </label>
            <select
              id={roleId}
              value={role}
              onChange={(e) => setRole(e.target.value as OrgRole)}
              className="block w-full rounded-md border border-border bg-background px-3 py-2 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
              data-testid="invite-role"
            >
              {roles.map((r) => (
                <option key={r} value={r}>
                  {roleName(r)}
                </option>
              ))}
            </select>
            <p className="text-xs text-muted-foreground">{ROLE_HINT[role]}</p>
          </div>
          {seats && (
            <div className="space-y-1">
              <SeatCount seats={seats} />
              {seatsFull(seats) && (
                <p className="text-xs text-muted-foreground">
                  Remove a member or revoke a pending invitation to free a seat first.
                </p>
              )}
            </div>
          )}
          {error && (
            <p role="alert" className="rounded-md border border-destructive/40 bg-background px-3 py-2 text-sm text-destructive-ink" data-testid="invite-error">
              {error}
            </p>
          )}
          {/* Enter submits; the visible button is in the footer. */}
          <button type="submit" hidden />
        </form>
      )}
    </Modal>
  );
}

/**
 * The outcome of an invitation: sent or not, and the link to share by hand.
 * The link carries the invitation's secret in its fragment; it is shown and
 * copied, never logged.
 */
export function InviteLinkPanel({ result, email }: { result: InviteResult; email: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(result.acceptUrl);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };
  return (
    <div className="space-y-4" data-testid="invite-result">
      <p className="text-sm text-foreground" data-testid="invite-result-status">
        {result.emailSent
          ? `${result.resent ? "Sent again" : "Sent"} to ${email}. The invitation expires in 7 days.`
          : `We couldn't email ${email}, so send them this link yourself. It expires in 7 days.`}
      </p>
      {result.acceptUrl && (
        <div className="space-y-1.5">
          <p className="text-xs text-muted-foreground">
            {result.emailSent ? "You can also share the link directly." : "Only someone signed in as this address can use it."}
          </p>
          <div className="flex items-center gap-2 rounded-md border border-border/60 bg-background p-2">
            <code className="min-w-0 flex-1 truncate text-xs text-foreground" data-testid="invite-link">
              {result.acceptUrl}
            </code>
            <Button
              variant="outline"
              size="sm"
              leftIcon={copied ? <Check className="h-3.5 w-3.5" aria-hidden="true" /> : <Copy className="h-3.5 w-3.5" aria-hidden="true" />}
              onClick={() => void copy()}
              data-testid="invite-copy-link"
            >
              {copied ? "Copied" : "Copy link"}
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}
