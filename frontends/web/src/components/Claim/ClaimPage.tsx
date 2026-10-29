// Copyright (c) 2025 Reliant Labs

/**
 * /claim — the human half of a claimable deploy. An agent deployed a site with
 * no account and handed over `…/claim#rcl_<token>`; this page shows what it is
 * about to hand over and, on "Claim it, it's free", makes the account that
 * deployed it theirs. The states and the flow are in useClaimFlow.ts; the
 * words are in claimCopy.ts.
 *
 * Deliberately NOT under the authenticated layout: the page has to show the
 * site to someone with no session at all, and a layout redirect to /auth would
 * lose the claim.
 */

import { CircleCheck, Clock, Globe, TriangleAlert } from "lucide-react";

import { AuthHeader, AuthLayout } from "@/components/AuthLayout";
import Button from "@/components/forge-ui/button";
import { CardInset } from "@/components/forge-ui/card";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import type { ClaimSite } from "@/services/controlPlane/claim";

import { FINISH_DECLINED_COPY, REFUSAL_COPY, REPLACE_ANONYMOUS_COPY } from "./claimCopy";
import { ClaimIntoAccount } from "./ClaimIntoAccount";
import { FreeSiteDialog } from "./FreeSiteDialog";
import { ExistingAccount } from "./ExistingAccount";
import type { ClaimSituation } from "./claimSituation";
import { useClaimFlow } from "./useClaimFlow";
import { useAuthStore } from "@/store/authStore";

export function ClaimPage() {
  const { view, claim, claimIntoAccount, freeSiteDialog, answerFreeSite, cancelFreeSite, retryFinish, retryPreview } = useClaimFlow();

  return (
    <AuthLayout>
      <div className="space-y-6 p-6 sm:p-8" data-testid="claim-page" data-state={view.kind}>
        {view.kind === "loading" && <Busy label="Looking up your site…" />}
        {view.kind === "finishing" && <Busy label="Making this site yours…" />}

        {view.kind === "no-link" && (
          <Notice
            tone="neutral"
            title="Open the claim link from your agent"
            body="This page needs the full claim link, which ends in # followed by a code. Ask your agent for it again, and open it exactly as it was given to you."
          />
        )}

        {view.kind === "refused" && (
          <Notice
            tone={view.refusal === "claimable_claimed" ? "neutral" : "warning"}
            title={REFUSAL_COPY[view.refusal].title}
            body={REFUSAL_COPY[view.refusal].body}
            next={REFUSAL_COPY[view.refusal].next}
            testId={`claim-refused-${view.refusal}`}
          />
        )}

        {view.kind === "unavailable" && (
          <Notice
            tone="warning"
            title="We couldn't load this claim"
            body={view.message}
            action={{ label: "Try again", onClick: retryPreview }}
            testId="claim-unavailable"
          />
        )}

        {view.kind === "finish-failed" && (
          <Notice
            tone="warning"
            title="We couldn't finish your claim"
            body={`${view.message} Your account is saved, and the site is still waiting for you.`}
            action={{ label: "Try again", onClick: retryFinish }}
            testId="claim-finish-failed"
          />
        )}

        {view.kind === "finish-declined" && (
          <Notice
            tone="warning"
            title={FINISH_DECLINED_COPY.title}
            body={view.reason ? `${FINISH_DECLINED_COPY.body} (${view.reason})` : FINISH_DECLINED_COPY.body}
            action={{ label: "Try again", onClick: retryFinish }}
            testId="claim-finish-declined"
          />
        )}

        {view.kind === "ready" && (
          <Ready
            site={view.site}
            situation={view.situation}
            working={view.working}
            error={view.error}
            onClaim={claim}
            onClaimIntoAccount={claimIntoAccount}
          />
        )}
        {view.kind === "ready" && freeSiteDialog && (
          <FreeSiteDialog conflict={freeSiteDialog} site={view.site} onAnswer={answerFreeSite} onCancel={cancelFreeSite} />
        )}
      </div>
    </AuthLayout>
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
  next?: { label: string; href: string };
  action?: { label: string; onClick: () => void };
  testId?: string;
}

function Notice({ tone, title, body, next, action, testId }: NoticeProps) {
  const Icon = tone === "warning" ? TriangleAlert : CircleCheck;
  return (
    <div className="space-y-5 text-center" data-testid={testId ?? "claim-notice"}>
      <div
        className={
          tone === "warning"
            ? "mx-auto flex h-10 w-10 items-center justify-center rounded-full bg-warning/15 text-warning-ink"
            : "mx-auto flex h-10 w-10 items-center justify-center rounded-full bg-primary/10 text-primary"
        }
      >
        <Icon className="h-5 w-5" aria-hidden="true" />
      </div>
      <div className="space-y-2">
        <h1 className="text-xl font-semibold text-foreground">{title}</h1>
        <p className="text-sm text-muted-foreground">{body}</p>
      </div>
      {action && (
        <Button onClick={action.onClick} variant="primary" fullWidth>
          {action.label}
        </Button>
      )}
      {next && (
        <Button href={next.href} variant="outline" fullWidth>
          {next.label}
        </Button>
      )}
    </div>
  );
}

interface ReadyProps {
  site: ClaimSite;
  situation: ClaimSituation;
  working: boolean;
  error: string | null;
  onClaim: () => void;
  onClaimIntoAccount: () => void;
}

function Ready({ site, situation, working, error, onClaim, onClaimIntoAccount }: ReadyProps) {
  const existing = situation === "existing-account";
  const email = useAuthStore((s) => s.user?.email);
  return (
    <>
      <AuthHeader
        title="Claim this site"
        description="An agent deployed this for you. Claim it to keep it, free, in your own Reliant account."
      />

      <SitePreview site={site} />

      {situation === "replace-anonymous" && (
        <div className="rounded-lg border border-border bg-card p-4" data-testid="claim-replace-anonymous">
          <p className="text-sm font-medium text-foreground">{REPLACE_ANONYMOUS_COPY.heading}</p>
          <p className="mt-1 text-sm text-muted-foreground">{REPLACE_ANONYMOUS_COPY.body}</p>
        </div>
      )}

      {error && (
        <div role="alert" className="rounded-lg border border-destructive/40 bg-card p-4" data-testid="claim-error">
          <p className="text-sm text-destructive-ink">{error}</p>
        </div>
      )}

      {existing ? (
        <ClaimIntoAccount email={email} working={working} onClaim={onClaimIntoAccount} />
      ) : (
        <div className="space-y-3">
          <Button
            onClick={onClaim}
            loading={working}
            variant="primary"
            size="xl"
            fullWidth
            // A claim link is mostly opened from a phone: the one action on the page clears the 44px touch minimum.
            className="!h-12"
            data-testid="claim-submit"
          >
            {situation === "resume-upgrade" ? "Continue claiming" : "Claim it, it's free"}
          </Button>
          <p className="text-center text-xs text-muted-foreground">
            You'll add an email or sign in with a provider to keep it. Whoever claims it first owns it.
          </p>
          {situation === "signed-out" && <ExistingAccount />}
          {situation === "resume-upgrade" && <ExistingAccount leavesTemporaryAccount />}
        </div>
      )}
    </>
  );
}

function SitePreview({ site }: { site: ClaimSite }) {
  const suspended = site.state === "suspended";
  const host = site.hostnames[0];
  const title = site.project || host || "Your site";

  return (
    <CardInset padding="md" className="space-y-3" data-testid="claim-site" data-site-state={site.state}>
      <div className="flex items-start gap-3">
        <Globe className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        <div className="min-w-0 space-y-1">
          <p className="truncate text-sm font-semibold text-foreground" data-testid="claim-site-name">
            {title}
          </p>
          {site.env && <p className="text-xs text-muted-foreground">Environment: {site.env}</p>}
        </div>
      </div>

      {host && (
        <ul className="space-y-1 text-sm" aria-label="Site address">
          {site.hostnames.map((h) => (
            <li key={h} className="truncate font-mono text-xs text-foreground" data-testid="claim-site-host">
              {suspended ? h : <a href={`https://${h}`} target="_blank" rel="noreferrer noopener" className="underline underline-offset-2">{h}</a>}
            </li>
          ))}
        </ul>
      )}

      <Expiry site={site} />
    </CardInset>
  );
}

function Expiry({ site }: { site: ClaimSite }) {
  const suspended = site.state === "suspended";
  if (suspended) {
    return (
      <p className="flex items-start gap-2 text-xs text-warning-ink" data-testid="claim-expiry">
        <Clock className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span>
          Offline now.
          {site.purgeAfter && ` Claim it ${formatRelativeTime(site.purgeAfter.toISOString())} or it is deleted (${formatAbsoluteTime(site.purgeAfter.toISOString())}).`}
        </span>
      </p>
    );
  }
  if (!site.expiresAt) return null;
  return (
    <p className="flex items-start gap-2 text-xs text-muted-foreground" data-testid="claim-expiry">
      <Clock className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>
        Goes offline {formatRelativeTime(site.expiresAt.toISOString())} ({formatAbsoluteTime(site.expiresAt.toISOString())}) unless it is claimed.
      </span>
    </p>
  );
}
