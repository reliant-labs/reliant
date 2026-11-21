// Copyright (c) 2025 Reliant Labs

import { useId, type ReactNode } from "react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import { useActiveOrg } from "@/hooks/useOrgs";
import { hasControlPlane } from "@/services/controlPlane/config";

/** What kind of link named the org, in the words the panel uses. */
export type OrgLinkSubject = "billing link" | "environment link";

export interface OrgSwitchGateProps {
  /** The org (id) the link names (`?org=`); empty or absent means the link names none. */
  requestedOrgId?: string | null;
  /** Forget the link's org (strip `?org=` from the URL) and carry on in the active one. */
  onDismiss: () => void;
  subject: OrgLinkSubject;
  /** Prefix of the test ids: `<prefix>-gate`, `<prefix>-switch`, `<prefix>-stay`, `<prefix>-retry`. */
  testIdPrefix?: string;
  children: ReactNode;
}

/**
 * A link that names an org (`?org=<id>`) opened while the app acts in another
 * one. The page acts in the ACTIVE org, so it must not render for the wrong
 * one — and it must not switch on its own either (owner decision): a switch
 * the person did not see is how spend gets authorized in an org they did not
 * mean. So this asks, and renders the page only once the link's org and the
 * active org agree, or the person has chosen to stay.
 *
 *   no org named, or it is the active one   the page
 *   the membership list still loading       a quiet status line
 *   an org the person belongs to            "This link is for B. You're acting
 *                                           in A." [Switch to B] [Stay in A]
 *   an org they do not belong to            "You're not a member…" [Continue in A]
 *
 * Switching is the same view choice as the org switcher (`select`); staying,
 * continuing and a completed switch all drop `?org=` from the URL
 * (`onDismiss`), so the address no longer claims an org the page is not in.
 *
 * The panel is forge-ui (`.forge-ui` scope, forge tokens), so it reads the
 * same in the Reliant settings and inside the forge console.
 */
export function OrgSwitchGate({ requestedOrgId, onDismiss, subject, testIdPrefix = "org-link", children }: OrgSwitchGateProps) {
  const t = testIdPrefix;
  const { orgs, active, activeOrgId, isLoading, error, refetch, select } = useActiveOrg();
  const requested = requestedOrgId?.trim() || "";

  // No control plane: no orgs, so a link's org means nothing here.
  if (!requested || !hasControlPlane || requested === activeOrgId) return <>{children}</>;

  if (isLoading) {
    return (
      <p role="status" className="forge-ui py-6 text-ui text-ink-muted" data-testid={`${t}-gate`} data-state="loading">
        Checking which organization this {subject} is for…
      </p>
    );
  }

  const here = active?.name || "your current organization";

  if (error && orgs.length === 0) {
    return (
      <GatePanel
        testId={`${t}-gate`}
        state="error"
        title={`We couldn't check which organization this ${subject} is for`}
        body="The list of your organizations didn't load. Try again, or carry on in the organization you're in."
        actions={
          <>
            <Button variant="outline" onClick={refetch} data-testid={`${t}-retry`}>
              Try again
            </Button>
            <Button variant="ghost" onClick={onDismiss} data-testid={`${t}-stay`}>
              Continue
            </Button>
          </>
        }
      />
    );
  }

  const target = orgs.find((o) => o.id === requested);
  if (!target) {
    return (
      <GatePanel
        testId={`${t}-gate`}
        state="not-member"
        title={`You're not a member of the organization this ${subject} is for`}
        body={
          active
            ? `Ask someone in it to invite you. Until then, you can carry on in ${here}.`
            : "Ask someone in it to invite you."
        }
        actions={
          <Button variant="ghost" onClick={onDismiss} data-testid={`${t}-stay`}>
            {active ? `Continue in ${here}` : "Continue"}
          </Button>
        }
      />
    );
  }

  const name = target.name || "that organization";
  return (
    <GatePanel
      testId={`${t}-gate`}
      state="switch"
      title={`This ${subject} is for ${name}`}
      body={`You're acting in ${here}. Switch to ${name} to ${subject === "billing link" ? "see and change its billing" : "open this environment"}, or stay in ${here}.`}
      actions={
        <>
          <Button
            variant="primary"
            onClick={() => {
              select(target.id);
              onDismiss();
            }}
            data-testid={`${t}-switch`}
          >
            Switch to {name}
          </Button>
          <Button variant="ghost" onClick={onDismiss} data-testid={`${t}-stay`}>
            Stay in {here}
          </Button>
        </>
      }
    />
  );
}

function GatePanel({
  testId,
  state,
  title,
  body,
  actions,
}: {
  testId: string;
  state: string;
  title: string;
  body: string;
  actions: ReactNode;
}) {
  const titleId = useId();
  return (
    <section
      className="forge-ui mx-auto max-w-[var(--form-max)] py-6"
      aria-labelledby={titleId}
      data-testid={testId}
      data-state={state}
    >
      <Card padding="lg">
        <h2 id={titleId} className="text-section text-ink">
          {title}
        </h2>
        <p className="mt-1.5 text-ui text-ink-muted">{body}</p>
        <div className="mt-4 flex flex-wrap items-center gap-2">{actions}</div>
      </Card>
    </section>
  );
}
