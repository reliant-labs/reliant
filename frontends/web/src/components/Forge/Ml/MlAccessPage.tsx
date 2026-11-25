// Copyright (c) 2026 Reliant Labs

/**
 * "Reach out to Reliant for access": the one page every ML route renders for
 * an org without the "ml" entitlement (forge docs/proposals/ml-workloads.md
 * §4.1, §8). It says what ML does, links the public demo, and offers ONE
 * action, Request access (OrganizationService.RequestOrgEntitlement).
 *
 *   never asked       Request access, the page's one primary button
 *   asked (by anyone  "Requested on <date>; we'll reach out", the button
 *   in the org)       disabled: the server records one request per org
 *   the call failed   a support email with the org id, instead
 *
 * The server emits `ml_access_requested` when it records a request, so this
 * page emits only its own view (`ml_access_page_viewed`), never a second
 * copy of the request.
 */

import { useEffect, useRef } from "react";
import { Boxes, Cpu, ExternalLink, Layers, Lightbulb, Rocket, Server, Sparkles } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Tag from "@/components/forge-ui/tag";
import { useRequestMlAccess } from "@/hooks/ml-queries";
import { trackEvent } from "@/lib/analytics";
import { demoAppOrigin } from "@/lib/shell";
import type { OrgEntitlement } from "@/services/controlPlane/ml/types";

import type { MlView } from "../Console/paths";

/** The fallback when the request cannot be sent: support, with the org named (mirrors HOSTING_SUPPORT_MAILTO). */
export function mlSupportMailto(orgId: string): string {
  const body = `Please enable ML early access for organization ${orgId}.`;
  return `mailto:support@reliantlabs.io?subject=${encodeURIComponent("ML early access")}&body=${encodeURIComponent(body)}`;
}

/** Where the demo's ML tab opens. */
export const ML_DEMO_URL = `${demoAppOrigin()}/forge/ml/machines`;

const WHAT: { icon: typeof Cpu; title: string; body: string }[] = [
  { icon: Server, title: "Machines", body: "Every accelerator node in your clusters, by pool: what is allocated, what is busy, and which devices are failing." },
  { icon: Layers, title: "Stack", body: "The ML components already in each cluster, adopted as they are, and only what is missing installed from pinned charts." },
  { icon: Lightbulb, title: "Recommendations", body: "Bad machines, slow pod starts and missing telemetry, worst first, each with the change that fixes it." },
  { icon: Rocket, title: "Runs", body: "Training runs with their parameters, metrics, restarts and preemptions." },
  { icon: Boxes, title: "Models", body: "Model versions by digest, the run that produced each, and where each serves." },
  { icon: Sparkles, title: "Inference", body: "Time to first token, queue wait, throughput and cold starts, with a verdict on every release." },
];

const dateWords = (ms: number) => new Date(ms).toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });

export interface MlAccessPageProps {
  orgId: string;
  /** The org's ml entitlement as the server last reported it; null when it could not be read. */
  entitlement: OrgEntitlement | null;
  /** The ML page that was asked for. */
  view: MlView;
  /** Why the page is shown: the entitlement says so, or an ML read refused (a revocation since). */
  source: "entitlement" | "refused";
}

export function MlAccessPage({ orgId, entitlement, view, source }: MlAccessPageProps) {
  const request = useRequestMlAccess(orgId);
  const requestedAtMs = request.data?.entitlement.requestedAtMs ?? entitlement?.requestedAtMs ?? null;
  const requested = requestedAtMs !== null;

  // One view event per page shown, not per render.
  const tracked = useRef(false);
  useEffect(() => {
    if (tracked.current) return;
    tracked.current = true;
    trackEvent("ml_access_page_viewed", { org_id: orgId, view, source, requested });
  }, [orgId, view, source, requested]);

  return (
    <div className="mx-auto w-full min-w-0 max-w-[var(--form-max)] space-y-6 py-2" data-testid="ml-access-page" data-requested={requested}>
      <header className="space-y-2">
        <Tag>Early access, by request</Tag>
        <h1 className="text-title text-ink">Reach out to Reliant for access</h1>
        <p className="text-copy text-ink-muted">
          ML brings your training and inference workloads into the console: the clusters you already run, read as they are. It is available by request
          while we work with the first teams using it.
        </p>
      </header>

      <Card padding="none" className="overflow-hidden">
        <ul className="divide-y divide-border-subtle" aria-label="What ML shows">
          {WHAT.map(({ icon: Icon, title, body }) => (
            <li key={title} className="flex items-start gap-3 px-4 py-3">
              <Icon className="mt-0.5 h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
              <div className="min-w-0">
                <p className="text-ui font-medium text-ink">{title}</p>
                <p className="text-ui text-ink-muted">{body}</p>
              </div>
            </li>
          ))}
        </ul>
      </Card>

      <div className="space-y-3" data-testid="ml-access-actions">
        <div className="flex flex-wrap items-center gap-3">
          <Button
            variant="primary"
            onClick={() => request.mutate(undefined)}
            loading={request.isPending}
            disabled={requested || request.data?.alreadyEnabled === true}
            data-testid="ml-access-request"
          >
            {requested ? "Access requested" : "Request access"}
          </Button>
          <Button variant="ghost" href={ML_DEMO_URL} rightIcon={<ExternalLink className="h-3.5 w-3.5" />} data-testid="ml-access-demo">
            View the demo
          </Button>
        </div>
        {request.data?.alreadyEnabled ? (
          <p className="text-ui text-ink" role="status" data-testid="ml-access-enabled">
            ML is already enabled for this organization. Opening it…
          </p>
        ) : requested ? (
          <p className="text-ui text-ink" role="status" data-testid="ml-access-requested">
            Requested on {dateWords(requestedAtMs!)}; we&apos;ll reach out.
          </p>
        ) : request.isError ? (
          <p className="text-ui text-ink" role="alert" data-testid="ml-access-failed">
            The request didn&apos;t go through. Email{" "}
            <a href={mlSupportMailto(orgId)} className="font-medium text-primary-ink underline-offset-2 hover:underline" data-testid="ml-access-mailto">
              support@reliantlabs.io
            </a>{" "}
            and we&apos;ll turn it on for this organization.
          </p>
        ) : (
          <p className="text-meta text-ink-muted">Anyone in the organization can ask. We&apos;ll email you when it is on.</p>
        )}
      </div>
    </div>
  );
}
