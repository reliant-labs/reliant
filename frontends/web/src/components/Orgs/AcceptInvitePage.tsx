// Copyright (c) 2025 Reliant Labs

/**
 * /accept-invite — someone was invited into an organization. The link is
 * `…/accept-invite#rinv_<token>`; lib/inviteLinkBoot moved the token into
 * storage before the router existed. This page shows what they were invited
 * to, says before any button whether they are signed in as the invited
 * address, and on "Join" makes them a member and switches the app to the org.
 * The states are in useAcceptInvite.ts.
 *
 * Root-level and unguarded, like /claim: a signed-out invitee must see the
 * page and be sent through sign-in from here, and a layout redirect to /auth
 * would lose where they were going.
 */

import { Building2, CircleCheck, Clock, TriangleAlert } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import { AuthHeader, AuthLayout } from "@/components/AuthLayout";
import Button from "@/components/forge-ui/button";
import { CardInset } from "@/components/forge-ui/card";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { roleName, type InvitationPreview } from "@/services/controlPlane/members";

import { useAcceptInvite, type InviteRefusal } from "./useAcceptInvite";

const REFUSAL_COPY: Record<InviteRefusal, { title: string; body: string }> = {
  expired: {
    title: "This invitation has expired",
    body: "Invitations last 7 days. Ask whoever invited you to send a new one.",
  },
  revoked: {
    title: "This invitation was withdrawn",
    body: "An admin of the organization revoked it. Ask them to invite you again if you still need access.",
  },
  used: {
    title: "This invitation has already been used",
    body: "Someone accepted it already. If that wasn't you, ask the organization's admin for a new invitation.",
  },
};

export function AcceptInvitePage() {
  const { view, accept, signIn, signingIn, retry } = useAcceptInvite();
  const navigate = useNavigate();
  const goHome = () => void navigate({ to: "/" });

  return (
    <AuthLayout>
      <div className="space-y-6 p-6 sm:p-8" data-testid="accept-invite-page" data-state={view.kind}>
        {view.kind === "loading" && <Busy label="Looking up your invitation…" />}

        {view.kind === "no-link" && (
          <Notice
            tone="neutral"
            title="Open the invitation link from your email"
            body="This page needs the full invitation link, which ends in # followed by a code. Open it exactly as it was sent to you."
          />
        )}

        {view.kind === "signed-out" && (
          <div className="space-y-5" data-testid="accept-invite-signed-out">
            <AuthHeader
              title="You've been invited to an organization"
              description="Sign in, or create an account, with the email address the invitation was sent to. You'll come straight back here to join."
            />
            <Button variant="primary" size="xl" fullWidth className="!h-12" loading={signingIn} onClick={signIn} data-testid="accept-invite-sign-in">
              Sign in to accept
            </Button>
          </div>
        )}

        {view.kind === "unverified" && (
          <Notice
            tone="warning"
            title="Verify your email first"
            body="Invitations go to an email address, so your account needs a verified one before you can join. Verify it, then open the invitation link again."
            action={{ label: "Use a different account", onClick: signIn, loading: signingIn }}
          />
        )}

        {view.kind === "invalid" && (
          <Notice
            tone="warning"
            title="This invitation link isn't valid"
            body="It may have been copied incompletely, or replaced by a newer invitation. Ask whoever invited you to send it again."
            testId="accept-invite-invalid"
          />
        )}

        {view.kind === "unavailable" && (
          <Notice
            tone="warning"
            title="We couldn't load your invitation"
            body={view.message}
            action={{ label: "Try again", onClick: retry }}
            testId="accept-invite-unavailable"
          />
        )}

        {view.kind === "refused" && (
          <Notice tone="warning" title={REFUSAL_COPY[view.refusal].title} body={REFUSAL_COPY[view.refusal].body} testId={`accept-invite-refused-${view.refusal}`} />
        )}

        {view.kind === "already-member" && (
          <Notice
            tone="neutral"
            title={`You're already in ${view.preview.orgName}`}
            body="There's nothing to accept. Switch to it from the organization menu."
            action={{ label: "Continue", onClick: goHome }}
            testId="accept-invite-already-member"
          />
        )}

        {view.kind === "mismatch" && (
          <div className="space-y-5" data-testid="accept-invite-mismatch">
            <AuthHeader title={`Join ${view.preview.orgName}`} description="This invitation is for a different email address." />
            <InvitationCard preview={view.preview} />
            <div role="alert" className="rounded-lg border border-warning/40 bg-card p-4">
              <p className="text-sm text-foreground">
                It was sent to <span className="font-medium">{view.preview.inviteeEmailHint}</span>
                {view.signedInAs ? (
                  <>
                    , but you're signed in as <span className="font-medium">{view.signedInAs}</span>
                  </>
                ) : null}
                . Sign in with the invited address to accept it.
              </p>
            </div>
            <Button variant="primary" fullWidth loading={signingIn} onClick={signIn} data-testid="accept-invite-switch-account">
              Use a different account
            </Button>
          </div>
        )}

        {view.kind === "ready" && (
          <div className="space-y-5">
            <AuthHeader title={`Join ${view.preview.orgName}`} description={`${view.preview.invitedByName || "Someone"} invited you to join this organization.`} />
            <InvitationCard preview={view.preview} />
            {view.error && (
              <div role="alert" className="rounded-lg border border-destructive/40 bg-card p-4" data-testid="accept-invite-error">
                <p className="text-sm text-destructive-ink">{view.error}</p>
              </div>
            )}
            <Button variant="primary" size="xl" fullWidth className="!h-12" loading={view.working} onClick={accept} data-testid="accept-invite-submit">
              Join {view.preview.orgName}
            </Button>
          </div>
        )}

        {view.kind === "joined" && (
          <Notice
            tone="neutral"
            title={`You're in ${view.org.name}`}
            body="You're now working in this organization. Switch between your organizations from the organization menu."
            action={{ label: `Open ${view.org.name}`, onClick: goHome }}
            testId="accept-invite-joined"
          />
        )}
      </div>
    </AuthLayout>
  );
}

function InvitationCard({ preview }: { preview: InvitationPreview }) {
  return (
    <CardInset padding="md" className="space-y-2" data-testid="accept-invite-card">
      <div className="flex items-start gap-3">
        <Building2 className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="min-w-0 space-y-1">
          <p className="truncate text-sm font-semibold text-foreground" data-testid="accept-invite-org">
            {preview.orgName}
          </p>
          <p className="text-xs text-muted-foreground">
            Role: {roleName(preview.role)}
            {preview.invitedByName && ` · invited by ${preview.invitedByName}`}
          </p>
        </div>
      </div>
      {preview.expiresAt && (
        <p className="flex items-start gap-2 text-xs text-muted-foreground">
          <Clock className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>
            Expires {formatRelativeTime(preview.expiresAt.toISOString())} ({formatAbsoluteTime(preview.expiresAt.toISOString())})
          </span>
        </p>
      )}
    </CardInset>
  );
}

function Busy({ label }: { label: string }) {
  return (
    <div className="flex flex-col items-center gap-4 py-6" role="status" aria-live="polite">
      <div className="h-6 w-6 animate-spin rounded-full border-2 border-primary border-t-transparent motion-reduce:animate-none" />
      <p className="text-sm text-muted-foreground">{label}</p>
    </div>
  );
}

interface NoticeProps {
  tone: "neutral" | "warning";
  title: string;
  body: string;
  action?: { label: string; onClick: () => void; loading?: boolean };
  testId?: string;
}

function Notice({ tone, title, body, action, testId }: NoticeProps) {
  const Icon = tone === "warning" ? TriangleAlert : CircleCheck;
  return (
    <div className="space-y-5 text-center" data-testid={testId ?? "accept-invite-notice"}>
      <div
        className={
          tone === "warning"
            ? "mx-auto flex h-10 w-10 items-center justify-center rounded-full bg-warning/15 text-warning-ink"
            : "mx-auto flex h-10 w-10 items-center justify-center rounded-full bg-primary/10 text-primary-ink"
        }
      >
        <Icon className="h-5 w-5" aria-hidden="true" />
      </div>
      <div className="space-y-2">
        <h1 className="text-xl font-semibold text-foreground">{title}</h1>
        <p className="text-sm text-muted-foreground">{body}</p>
      </div>
      {action && (
        <Button onClick={action.onClick} loading={action.loading} variant="primary" fullWidth>
          {action.label}
        </Button>
      )}
    </div>
  );
}
