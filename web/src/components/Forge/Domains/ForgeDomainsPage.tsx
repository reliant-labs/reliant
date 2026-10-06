// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/domains — CUSTOM DOMAINS.
 *
 * ── WHY THIS IS A TOP-LEVEL TAB AND NOT PART OF AN ENVIRONMENT PAGE ─────────
 *
 * Every other screen on this surface is organised around an environment,
 * because that is how a reader thinks about deployments. A domain is the one
 * thing here that genuinely is not: it is org-scoped, it has no environment,
 * and it deliberately OUTLIVES the environment it happens to be pointed at
 * today. Filing it under prod would make "move example.com to staging for
 * an hour" look like it belongs to prod, and would hide a claimed-but-unbound
 * domain — which is precisely the state a tenant is stuck in when they need
 * this screen most.
 *
 * ── THE LIST IS THE ORG'S, THE TARGETS ARE THE PROJECT'S ────────────────────
 *
 * ListDomains is org-wide with no project filter, and that is the honest
 * rendering: a second project's domain is still yours, and hiding it would
 * make a hostname look free when adding it would fail. The environments
 * offered as bind TARGETS, though, come from the project in context — that
 * is the list the user is working in.
 *
 * ── THE PAGE MOVES ON ITS OWN ───────────────────────────────────────────────
 *
 * A converging domain is re-read every few seconds (see
 * forge-domain-queries.ts). Nothing here triggers that; the hook decides,
 * from whether anything is actually still converging.
 */

import { useCallback, useMemo, useState } from "react";
import { Globe, Plus } from "lucide-react";
import { useSearch } from "@tanstack/react-router";

import EmptyState from "@/components/forge-ui/empty_state";
import PageHeader from "@/components/forge-ui/page_header";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import {
  useAddDomain,
  useBindDomain,
  useForgeDomains,
  useRemoveDomain,
  useUnbindDomain,
  useVerifyDomain,
} from "@/hooks/forge-domain-queries";
import { useCloudEnvStatuses, useForgeEnvironments } from "@/hooks/forge-queries";
import { cn } from "@/lib/utils";
import { cloudRunIdOf } from "@/services/forge/environments";
import { hostedWorkloadsOf } from "@/services/forge/topology";
import {
  DOMAIN_AVAILABILITY_EXPLANATIONS,
  bindingSummary,
  domainTargetsOf,
  type ForgeDomain,
} from "@/services/forge/domains";
import { useProjectStore } from "@/store/projectStore";

import { AddDomainDialog, type DomainTargetEnv } from "./AddDomainDialog";
import { DomainDetail } from "./DomainDetail";
import { DomainStateBadge } from "./DomainStateBadge";

/** Where the tenant-facing explanation of all this lives. */
export const CUSTOM_DOMAINS_DOCS_URL = "https://docs.reliantlabs.io/features/custom-domains";

export function ForgeDomainsPage() {
  const { project: projectParam } = useSearch({ from: "/_authenticated/_forge/forge/domains" });
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = projectParam ?? currentProject?.id ?? null;

  const { data, isLoading } = useForgeDomains();
  const { envs } = useForgeEnvironments(projectId);

  const [expanded, setExpanded] = useState<string | null>(null);
  const [addOpen, setAddOpen] = useState(false);
  /** The domain being re-bound, or null. Reuses the add dialog's picker. */
  const [rebinding, setRebinding] = useState<ForgeDomain | null>(null);
  const [removing, setRemoving] = useState<ForgeDomain | null>(null);

  const add = useAddDomain();
  const bind = useBindDomain();
  const unbind = useUnbindDomain();
  const remove = useRemoveDomain();
  const verify = useVerifyDomain();

  const cloudRunIds = useMemo(
    () => envs.map(cloudRunIdOf).filter((id): id is string => !!id),
    [envs]
  );
  const statuses = useCloudEnvStatuses(cloudRunIds);

  /**
   * The environments a domain can be bound into: the ones the control plane
   * RUNS, because a binding is written against a control-plane environment id
   * and a local env has nothing to serve.
   *
   * Their targets come from the control plane's own deployment status when it
   * has answered — it is what will route the domain, and it needs no daemon —
   * and from forge's report until then. Either way only what can serve HTTP
   * is offered (domainTargetsOf). Not memoised: the status map is rebuilt per
   * render, and the dialog does not reset on a new list.
   */
  const targetEnvs: DomainTargetEnv[] = envs.flatMap((env) => {
    const environmentId = cloudRunIdOf(env);
    if (!environmentId) return [];
    const status = statuses.get(environmentId);
    const targets = domainTargetsOf(status?.data?.workloads ?? hostedWorkloadsOf(env.forge));
    return [
      {
        name: env.name,
        environmentId,
        targets,
        targetsLoading: targets.length === 0 && !status?.data && !!status?.isLoading,
      },
    ];
  });

  /** A binding stores an environment id; the reader knows a name. */
  const envNameFor = useCallback(
    (environmentId: string) =>
      targetEnvs.find((env) => env.environmentId === environmentId)?.name ?? environmentId,
    [targetEnvs]
  );

  const domains = data?.domains ?? [];
  const availability = data?.availability ?? "available";

  const onAdd = useCallback(
    async (args: {
      hostname: string;
      environmentId?: string;
      target?: string;
      redirectTo?: string;
    }) => {
      const result = await add.mutateAsync(args);
      setAddOpen(false);
      // Open the new domain straight away: its records are the next thing
      // the user needs, and making them find the row first would be a step
      // whose only content is "the thing you just created is here".
      setExpanded(result.domain.id);
    },
    [add]
  );

  const onRebind = useCallback(
    async (args: {
      hostname: string;
      environmentId?: string;
      target?: string;
      redirectTo?: string;
    }) => {
      if (!rebinding || !args.environmentId) return;
      await bind.mutateAsync({
        domainId: rebinding.id,
        environmentId: args.environmentId,
        target: args.target,
        redirectTo: args.redirectTo,
      });
      setRebinding(null);
    },
    [bind, rebinding]
  );

  if (availability !== "available") {
    return (
      <div className="p-6">
        <PageHeader title="Custom domains" subtitle="Serve your project on a hostname you own." />
        <div className="rounded-lg border border-border bg-card p-4">
          <p className="text-sm text-foreground">
            {DOMAIN_AVAILABILITY_EXPLANATIONS[availability]}
          </p>
          {availability === "unreachable" && data?.detail && (
            <p className="mt-1 text-xs text-muted-foreground">{data.detail}</p>
          )}
        </div>
      </div>
    );
  }

  return (
    <div className="p-6">
      <PageHeader
        title="Custom domains"
        subtitle={
          <>
            Serve this project on a hostname you own. Adding one tells you the DNS to publish;
            Reliant verifies it, obtains a certificate and starts serving.{" "}
            <a
              href={CUSTOM_DOMAINS_DOCS_URL}
              target="_blank"
              rel="noreferrer"
              className="text-primary underline underline-offset-2"
            >
              How custom domains work
            </a>
          </>
        }
        actions={[
          {
            label: "Add domain",
            onClick: () => setAddOpen(true),
            variant: "primary",
            icon: <Plus className="h-4 w-4" aria-hidden="true" />,
          },
        ]}
      />

      {isLoading && domains.length === 0 ? (
        <p className="text-sm text-muted-foreground">Loading domains…</p>
      ) : domains.length === 0 ? (
        <EmptyState
          icon={<Globe className="h-7 w-7" aria-hidden="true" />}
          title="No custom domains yet"
          description="Your organization holds no domains. Add one to serve this project on a hostname you own instead of the generated one."
          actionLabel="Add domain"
          onAction={() => setAddOpen(true)}
        />
      ) : (
        <ul className="flex flex-col gap-2" aria-label="Custom domains">
          {domains.map((domain) => {
            const open = expanded === domain.id;
            return (
              <li
                key={domain.id}
                className="overflow-hidden rounded-lg border border-border bg-card"
                data-testid={`domain-row-${domain.hostname}`}
              >
                <button
                  type="button"
                  onClick={() => setExpanded(open ? null : domain.id)}
                  aria-expanded={open}
                  className="flex w-full flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-left transition hover:bg-muted/40"
                >
                  <span className="font-mono text-sm text-foreground">{domain.hostname}</span>
                  <DomainStateBadge state={domain.state} />
                  <span
                    className={cn(
                      "ml-auto text-xs",
                      domain.binding ? "text-muted-foreground" : "text-muted-foreground/70"
                    )}
                  >
                    {domain.binding && !domain.binding.redirectTo
                      ? `${domain.binding.target} in ${envNameFor(domain.binding.environmentId)}`
                      : bindingSummary(domain.binding)}
                  </span>
                </button>
                {open && (
                  <div className="border-t border-border/60 px-4 py-4">
                    <DomainDetail
                      domain={domain}
                      envNameFor={envNameFor}
                      onVerify={() => verify.mutate({ domainId: domain.id })}
                      onRebind={() => setRebinding(domain)}
                      onUnbind={() => unbind.mutate({ domainId: domain.id })}
                      onRemove={() => setRemoving(domain)}
                      isVerifying={verify.isPending && verify.variables?.domainId === domain.id}
                      actionError={
                        [verify.error, unbind.error, bind.error].find(Boolean)?.message ?? null
                      }
                    />
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}

      <AddDomainDialog
        open={addOpen}
        onClose={() => setAddOpen(false)}
        envs={targetEnvs}
        takenHostnames={domains.map((domain) => domain.hostname)}
        onSubmit={onAdd}
        isSubmitting={add.isPending}
        error={add.error}
      />

      {/* Rebinding reuses the add dialog's picker, with the hostname fixed and
          the current binding pre-selected. The same decision is being made,
          so it should look the same. */}
      {rebinding && (
        <AddDomainDialog
          open
          onClose={() => setRebinding(null)}
          envs={targetEnvs}
          takenHostnames={[]}
          hostname={rebinding.hostname}
          current={rebinding.binding}
          onSubmit={onRebind}
          isSubmitting={bind.isPending}
          error={bind.error}
        />
      )}

      <ConfirmationDialog
        open={!!removing}
        title={`Remove ${removing?.hostname ?? ""}?`}
        description="This stops serving the domain and releases the hostname — any organization can then claim it. Its verification and certificate are lost, so re-adding it means publishing DNS and waiting again."
        confirmLabel="Remove domain"
        variant="danger"
        loading={remove.isPending}
        onCancel={() => setRemoving(null)}
        onConfirm={() => {
          if (!removing) return;
          remove.mutate(
            { domainId: removing.id },
            {
              onSuccess: () => {
                setRemoving(null);
                setExpanded(null);
              },
            }
          );
        }}
      />
    </div>
  );
}
