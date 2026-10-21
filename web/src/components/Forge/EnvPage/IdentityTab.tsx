// Copyright (c) 2025 Reliant Labs

/**
 * THE IDENTITY TAB: the sign-in service (a managed Zitadel instance) Reliant
 * runs for a deployed environment — its status, its users, their roles.
 *
 * It reads only the control plane (controlplane.v1.IdentityService) and needs
 * no daemon, like Overview and Observability.
 *
 * ── STATES, AND WHY EACH IS DESIGNED RATHER THAN AN ERROR ───────────────────
 *
 *   not enabled   Unavailable: this control plane does not run identity
 *                 management yet (prod, until the reconciler's internal API is
 *                 switched on). A fact about the platform, not a fault: no
 *                 toast, no retry.
 *   none          the environment declares no identity. Says how to declare one.
 *   refused       its declaration was refused on the reconciler's last sweep;
 *                 the server's reason is the remedy.
 *   denied        not a member of the org.
 *   error         anything else, with the server's words and a retry.
 *
 * WHO MAY CHANGE WHAT. Any member may read; the verbs that change users need
 * the org admin role. The status says which the caller is (callerCanManage),
 * and a member sees no controls they would be refused — the server checks
 * every call again, so this is a courtesy, not the control.
 *
 * EMAIL IS NOT ASSUMED. An instance sends mail only when its environment
 * declares smtp (`mailDelivery`). Without it the tab says so, and every verb
 * that would have emailed someone offers a one-time link to hand over instead
 * (IdentityPasswordCodeDialog) rather than pretending a message went out.
 *
 * A LOCAL environment runs forge's dev IdP on the developer's machine. It
 * reports nothing to the control plane, so the tab says so plainly instead of
 * inventing a status.
 */

import { useState, type ReactNode } from "react";
import { AlertTriangle, Download, Fingerprint, KeyRound, Laptop, Lock, ShieldOff } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { CardInset } from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Card from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useIdentityMutation, useIdentityOrg, useIdentityStatus } from "@/hooks/identity-queries";
import type {
  IdentityDeliveryState,
  IdentityExportURL,
  IdentityState,
  IdentityStatus,
} from "@/services/controlPlane/identity/api";
import { identityErrorKind, identityErrorMessage } from "@/services/controlPlane/identity/errors";
import type { LiveEnv } from "@/services/forge/live";
import type { EnvLifecycle } from "@/services/forge/roster";
import { cn } from "@/lib/utils";

import { formatAgo, formatIn } from "../Observability/format";
import { Panel } from "./EnvTabPanels";
import { IdentityUsers } from "./IdentityUsers";

export interface IdentityTabProps {
  envName: string;
  lifecycle: EnvLifecycle;
  live: LiveEnv | null;
  liveLoading: boolean;
  /** Names an environment of this project by its control-plane id (owners and sharers arrive as ids). */
  envNameById: (id: string) => string | undefined;
}

export function IdentityTab({ envName, lifecycle, live, liveLoading, envNameById }: IdentityTabProps) {
  if (lifecycle === "local") return <LocalIdentity envName={envName} />;
  if (liveLoading && !live) return <IdentitySkeleton />;
  if (!live) {
    return (
      <p className="text-sm text-muted-foreground" data-testid="identity-unregistered">
        Reliant has no record of <span className="font-mono text-foreground">{envName}</span>, so it runs no sign-in service
        for it. Register it from Overview.
      </p>
    );
  }
  return <DeployedIdentity envId={live.id} envName={envName} envNameById={envNameById} />;
}

function DeployedIdentity({
  envId,
  envName,
  envNameById,
}: {
  envId: string;
  envName: string;
  envNameById: (id: string) => string | undefined;
}) {
  const org = useIdentityOrg();
  const status = useIdentityStatus(org.orgId, envId);

  const error = org.error ?? status.error;
  if (error) {
    switch (identityErrorKind(error)) {
      case "not-enabled":
        return <NotEnabled envName={envName} />;
      case "denied":
        return (
          <Frame icon={<ShieldOff className="h-4 w-4" />} title="You can't view this environment's identity" testId="identity-denied">
            <p>
              Only members of the organization that owns <span className="font-mono text-foreground">{envName}</span> can.
              Ask an organization admin to add you.
            </p>
          </Frame>
        );
      default:
        return (
          <Frame
            icon={<AlertTriangle className="h-4 w-4" />}
            title="Couldn't load this environment's identity"
            testId="identity-error"
            tone="bad"
          >
            {identityErrorMessage(error) && <p className="font-mono">{identityErrorMessage(error)}</p>}
            <div>
              <Button variant="outline" size="sm" onClick={() => void status.refetch()} loading={status.isFetching}>
                Try again
              </Button>
            </div>
          </Frame>
        );
    }
  }
  if (org.isLoading || (!status.data && (status.isLoading || !!org.orgId))) return <IdentitySkeleton />;
  if (!org.orgId) {
    return (
      <Frame icon={<Fingerprint className="h-4 w-4" />} title="Your organization is still being set up" testId="identity-no-org">
        <p>Identity is managed per organization. Once yours is ready, this environment&apos;s sign-in service shows here.</p>
      </Frame>
    );
  }

  const s = status.data as IdentityStatus;
  if (!s.hasInstance) {
    if (s.refused) {
      return (
        <Frame
          icon={<AlertTriangle className="h-4 w-4" />}
          title="Reliant refused this environment's identity declaration"
          testId="identity-refused"
          tone="bad"
        >
          <p className="font-mono text-foreground">{s.refused}</p>
          <p>Fix the declaration and deploy again; the next pass picks it up.</p>
        </Frame>
      );
    }
    return (
      <Frame
        icon={<Fingerprint className="h-4 w-4" />}
        title={`${envName} doesn't declare a managed identity`}
        testId="identity-none"
      >
        <p>
          Declare one with <code className="font-mono text-foreground">forge.ManagedIdentity</code> in{" "}
          <code className="font-mono text-foreground">deploy/kcl/identity.k</code> and deploy. Reliant then runs its sign-in
          service — issuer, users, roles — and they show here.
        </p>
      </Frame>
    );
  }

  return (
    <div className="space-y-4" data-testid="identity-tab">
      {!s.callerCanManage && (
        <p className="inline-flex items-center gap-1.5 text-xs text-muted-foreground" role="status" data-testid="identity-read-only">
          <Lock className="h-3 w-3" aria-hidden="true" />
          You can view this environment&apos;s users. Inviting and changing them needs an organization admin.
        </p>
      )}
      <StatusPanel envId={envId} status={s} envNameById={envNameById} />
      <IdentityUsers orgId={org.orgId} envId={envId} envName={envName} status={s} />
      <ExportPanel orgId={org.orgId} envId={envId} status={s} />
    </div>
  );
}

// ── Status ───────────────────────────────────────────────────────────────────

const STATE_LABEL: Record<IdentityState, string> = {
  pending: "Pending",
  creating: "Creating",
  provisioning: "Provisioning",
  ready: "Ready",
  degraded: "Degraded",
  failing: "Failing",
  withdrawn: "Withdrawn",
  grace: "In grace period",
  deleting: "Deleting",
  deleted: "Deleted",
  unknown: "Unknown",
};

const STATE_VARIANT: Record<IdentityState, "success" | "warning" | "error" | "info" | "neutral"> = {
  pending: "info",
  creating: "info",
  provisioning: "info",
  ready: "success",
  degraded: "warning",
  failing: "error",
  withdrawn: "warning",
  grace: "warning",
  deleting: "neutral",
  deleted: "neutral",
  unknown: "neutral",
};

const DELIVERY_VARIANT: Record<IdentityDeliveryState, "success" | "warning" | "error" | "info" | "neutral"> = {
  delivered: "success",
  pending: "info",
  failing: "error",
  unbound: "warning",
  withdrawn: "neutral",
  disabled: "neutral",
  unknown: "neutral",
};

function StatusPanel({
  envId,
  status: s,
  envNameById,
}: {
  envId: string;
  status: IdentityStatus;
  envNameById: (id: string) => string | undefined;
}) {
  const name = (id: string) => envNameById(id) ?? id;
  const sharesAnother = !!s.ownerEnvironmentId && s.ownerEnvironmentId !== envId;
  return (
    <Panel
      title="Sign-in service"
      description="The identity provider Reliant runs for this environment, as of the reconciler's last pass."
      actions={<Badge label={STATE_LABEL[s.state]} variant={STATE_VARIANT[s.state]} dot />}
      testId="identity-status"
    >
      <div className="space-y-4">
        <dl className="grid grid-cols-1 gap-x-6 gap-y-3 text-sm sm:grid-cols-[max-content_1fr]">
          <dt className="text-xs text-muted-foreground">Issuer</dt>
          <dd className="min-w-0 break-all font-mono text-foreground" data-testid="identity-issuer">
            {s.issuer || "—"}
          </dd>
          <dt className="text-xs text-muted-foreground">Reachable</dt>
          <dd className="text-foreground" data-testid="identity-visibility">
            {s.public ? "Public — anyone can reach the sign-in pages" : "Private — only from your clusters"}
          </dd>
          {s.mailDelivery !== "unknown" && (
            <>
              <dt className="text-xs text-muted-foreground">Email</dt>
              <dd className="text-foreground" data-testid="identity-mail">
                {s.mailDelivery === "smtp" ? (
                  "Sent through this environment's own SMTP server"
                ) : (
                  <div className="space-y-1">
                    <p>None — Reliant sends no email for this environment: no invitations, no password-reset links.</p>
                    <p className="text-xs text-muted-foreground" data-testid="identity-mail-none-hint">
                      To send them, add <code className="font-mono text-foreground">smtp = forge.IdentitySMTP {"{…}"}</code> to its{" "}
                      <code className="font-mono text-foreground">forge.ManagedIdentity</code> in{" "}
                      <code className="font-mono text-foreground">deploy/kcl/identity.k</code> and deploy. Until then, an admin
                      invites people and resets passwords by getting a one-time link under Users and handing it over.
                    </p>
                  </div>
                )}
              </dd>
            </>
          )}
          <dt className="text-xs text-muted-foreground">Last reconciled</dt>
          <dd className="text-foreground">{s.lastReconciledAtMs ? formatAgo(s.lastReconciledAtMs) : "not yet"}</dd>
          {(sharesAnother || s.sharerEnvironmentIds.length > 0) && (
            <>
              <dt className="text-xs text-muted-foreground">Shared</dt>
              <dd className="text-foreground" data-testid="identity-sharing">
                {sharesAnother ? (
                  <>
                    Uses the identity of <span className="font-mono">{name(s.ownerEnvironmentId)}</span>, which owns it.
                  </>
                ) : (
                  <>
                    Also used by{" "}
                    {s.sharerEnvironmentIds.map((id, i) => (
                      <span key={id}>
                        {i > 0 && ", "}
                        <span className="font-mono">{name(id)}</span>
                      </span>
                    ))}
                    .
                  </>
                )}
              </dd>
            </>
          )}
          {s.adopted && (
            <>
              <dt className="text-xs text-muted-foreground">Adopted</dt>
              <dd className="text-muted-foreground">This instance existed before Reliant managed it; its ids were kept.</dd>
            </>
          )}
          {s.graceUntilMs && (
            <>
              <dt className="text-xs text-muted-foreground">Deleted after</dt>
              <dd className="text-foreground">
                {new Date(s.graceUntilMs).toLocaleString()} — this environment no longer declares it.
              </dd>
            </>
          )}
        </dl>

        {(s.lastError || s.failures > 0) && (
          <div
            role="alert"
            data-testid="identity-last-error"
            className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive-ink"
          >
            <p className="font-medium">
              {s.failures > 0 ? `${s.failures} failed attempt${s.failures === 1 ? "" : "s"}` : "The last attempt failed"}
              {s.nextAttemptAtMs ? `; next try ${formatIn(s.nextAttemptAtMs)}` : ""}
            </p>
            {s.lastError && <p className="mt-1 break-words font-mono">{s.lastError}</p>}
          </div>
        )}

        <Section title="Applications">
          {s.apps.length === 0 ? (
            <p className="text-xs text-muted-foreground">No applications are registered yet.</p>
          ) : (
            <table className="w-full text-left text-sm" data-testid="identity-apps">
              <thead>
                <tr className="text-xs text-muted-foreground">
                  <th className="pb-1.5 pr-4 font-normal">Name</th>
                  <th className="pb-1.5 pr-4 font-normal">Client id</th>
                  <th className="pb-1.5 font-normal">Broker token</th>
                </tr>
              </thead>
              <tbody>
                {s.apps.map((app) => (
                  <tr key={app.name} className="border-t border-border/60">
                    <td className="py-1.5 pr-4 text-foreground">{app.name}</td>
                    <td className="py-1.5 pr-4 font-mono text-xs text-foreground">{app.clientId || "—"}</td>
                    <td className="py-1.5 text-xs text-muted-foreground">
                      {app.brokerExpiresAtMs === null ? (
                        "none"
                      ) : (
                        <span className={cn(app.brokerExpiresAtMs <= Date.now() && "text-destructive-ink")}>
                          {app.brokerExpiresAtMs <= Date.now() ? "expired" : `renews ${formatIn(app.brokerExpiresAtMs)}`}
                        </span>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Section>

        <Section title="Deliveries">
          {s.deliveries.length === 0 ? (
            <p className="text-xs text-muted-foreground">Nothing is delivered to a cluster yet.</p>
          ) : (
            <ul className="space-y-2" data-testid="identity-deliveries">
              {s.deliveries.map((d) => (
                <li key={`${d.kind}/${d.cluster}/${d.namespace}/${d.name}`} className="space-y-0.5">
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <Badge label={d.state} variant={DELIVERY_VARIANT[d.state]} size="sm" />
                    <span className="font-mono text-xs text-foreground">
                      {d.cluster} · {d.namespace}/{d.name}
                    </span>
                    <span className="text-xs text-muted-foreground">{d.kind}</span>
                    {d.lastVerifiedAtMs && (
                      <span className="text-xs text-muted-foreground">verified {formatAgo(d.lastVerifiedAtMs)}</span>
                    )}
                  </div>
                  {d.lastError && <p className="break-words font-mono text-xs text-destructive-ink">{d.lastError}</p>}
                </li>
              ))}
            </ul>
          )}
        </Section>
      </div>
    </Panel>
  );
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="space-y-2">
      <h3 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{title}</h3>
      <CardInset padding="sm">{children}</CardInset>
    </section>
  );
}

// ── Export ───────────────────────────────────────────────────────────────────

function ExportPanel({ orgId, envId, status: s }: { orgId: string; envId: string; status: IdentityStatus }) {
  const [link, setLink] = useState<IdentityExportURL | null>(null);
  const fetchLink = useIdentityMutation(orgId, envId, (api, o, e, _: void) => api.getExportURL(o, e));

  let body: ReactNode;
  if (!s.exportEnabled) {
    body = (
      <p data-testid="identity-export-not-enabled">
        Export isn&apos;t enabled for this deployment yet. When it is, an environment&apos;s users and roles can be downloaded
        here, and an export is written automatically before a withdrawn identity is deleted.
      </p>
    );
  } else if (!s.exportUrlAvailable) {
    body = (
      <p data-testid="identity-export-none">
        No export yet. One is written when this environment stops declaring its identity, and kept until the instance is
        deleted.
      </p>
    );
  } else if (!s.callerCanManage) {
    body = <p data-testid="identity-export-admin-only">An export is ready. An organization admin can download it.</p>;
  } else {
    body = (
      <div className="space-y-2" data-testid="identity-export-ready">
        <p>An export of this identity is ready. Its download link is short-lived and works for whoever holds it.</p>
        {link ? (
          <p className="text-foreground">
            <a href={link.url} className="text-primary underline-offset-2 hover:underline" rel="noreferrer" target="_blank">
              Download the export
            </a>
            {link.expiresAtMs && <span className="text-muted-foreground"> — expires {formatIn(link.expiresAtMs)}</span>}
          </p>
        ) : (
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Download className="h-3.5 w-3.5" />}
            loading={fetchLink.isPending}
            onClick={() => fetchLink.mutate(undefined, { onSuccess: setLink })}
          >
            Get download link
          </Button>
        )}
        {fetchLink.error && (
          <p role="alert" className="text-destructive-ink">
            {identityErrorMessage(fetchLink.error) || "Couldn't sign a download link."}
          </p>
        )}
      </div>
    );
  }
  return (
    <Panel title="Export" description="A copy of this environment's users and roles." testId="identity-export">
      <div className="text-xs text-muted-foreground">{body}</div>
    </Panel>
  );
}

// ── States ───────────────────────────────────────────────────────────────────

function NotEnabled({ envName }: { envName: string }) {
  return (
    <Frame icon={<KeyRound className="h-4 w-4" />} title="Managed identity isn't enabled for this environment yet" testId="identity-not-enabled">
      <p>
        Reliant can run the sign-in service for an environment — its issuer, users and roles — but it isn&apos;t switched on
        for this control plane yet. Nothing is wrong with <span className="font-mono text-foreground">{envName}</span>: once
        it is, its identity shows here.
      </p>
    </Frame>
  );
}

function LocalIdentity({ envName }: { envName: string }) {
  return (
    <Frame icon={<Laptop className="h-4 w-4" />} title="This environment signs in against your local IdP" testId="identity-local">
      <p>
        <code className="font-mono text-foreground">forge env up {envName}</code> runs forge&apos;s dev IdP on your machine
        and converges the same identity declaration into it. That IdP reports nothing to Reliant, so there is no status to
        show here: its issuer and seed users are in the output of <code className="font-mono text-foreground">forge env up</code>.
      </p>
    </Frame>
  );
}

function IdentitySkeleton() {
  return (
    <Card padding="md" data-testid="identity-loading" aria-busy="true">
      <SkeletonLoader variant="list-item" count={4} />
    </Card>
  );
}

export function Frame({
  icon,
  title,
  children,
  testId,
  tone = "neutral",
}: {
  icon: ReactNode;
  title: string;
  children?: ReactNode;
  testId: string;
  tone?: "neutral" | "bad";
}) {
  return (
    <div data-testid={testId} role="status" className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-4 py-5">
      <span className={cn("mt-0.5 shrink-0", tone === "bad" ? "text-destructive-ink" : "text-muted-foreground")} aria-hidden="true">
        {icon}
      </span>
      <div className="min-w-0 space-y-1.5">
        <p className="text-sm font-medium text-foreground">{title}</p>
        {children && <div className="space-y-2 text-xs text-muted-foreground">{children}</div>}
      </div>
    </div>
  );
}
