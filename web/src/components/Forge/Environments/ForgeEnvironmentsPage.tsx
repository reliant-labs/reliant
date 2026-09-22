// Copyright (c) 2025 Reliant Labs

/**
 * Route component for /forge/environments.
 *
 * WHAT THIS SCREEN IS FOR, and why it is not the Releases matrix again. The
 * matrix answers "which release is each environment running, and does the
 * cluster agree?" — a comparison ACROSS environments, image by image. This
 * screen answers two prior questions: "what environments does this project
 * have, and where does each one point?", and then, for the selected one,
 * "WHAT IS ACTUALLY RUNNING THERE?"
 *
 * THE SECOND QUESTION IS WHY THIS SCREEN WAS REBUILT. It used to render the
 * env-status report's `services` array, which is the list of HOST PROCESSES on
 * the reader's own laptop, under a heading that read like a deployment
 * inventory. For control-plane's prod that showed two local dev servers while
 * the cluster ran sixteen workloads, and a user asked why prod only had two
 * services. Forge now emits a structured `workloads` document beside
 * `services`, and WorkloadInventory renders it. `services` is not shown here
 * at all — it is a real fact about a real thing, and it belongs on the Status
 * screen where it is labelled as what it is.
 *
 * TWO QUERIES, DIFFERENT COSTS, SO TWO SURFACES. The environment list comes
 * from the topology ledger — a local file read, shared with the Releases
 * screen through the same cache, so it paints immediately. The inventory is a
 * live cluster probe for ONE environment, so it is scoped to the selection
 * rather than run for every environment at once.
 *
 * Every non-report outcome is delegated to ForgeStates, for the reason spelled
 * out there: "no forge.yaml", "your forge is too old" and "the cluster was
 * unreachable" are ANSWERS, not failures, and each has to render as itself. In
 * particular an unreachable cluster must not render as an empty list, which
 * would read as "this project has no environments".
 *
 * `declared` and `bound` are shown as plain words rather than as a pass/fail
 * treatment. An environment that is declared but never promoted to is the
 * normal state of a new environment — it has nothing to be wrong about — and
 * painting it as a failure is how a status screen teaches people to ignore it.
 */

import { useCallback, useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { useProjectStore } from "@/store/projectStore";
import { useForgeEnvStatus, useForgeTopology } from "@/hooks/forge-queries";
import { environments } from "@/services/forge/topology";
import PageHeader from "@/components/forge-ui/page_header";
import StatusDot from "@/components/forge-ui/status_dot";

import {
  ForgeMalformed,
  ForgeUnreachable,
  ForgeUnsupported,
  NotForgeProject,
} from "../ForgeStates";
import { WorkloadInventory } from "./WorkloadInventory";

/** A label/value pair. Values that are IDENTIFIERS render mono; nothing else does. */
function Field({
  label,
  value,
  mono,
}: {
  label: string;
  value?: string;
  mono?: boolean;
}) {
  return (
    <div className="min-w-0">
      <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
        {label}
      </dt>
      <dd
        className={`mt-1 truncate text-sm ${
          value ? "text-foreground" : "text-muted-foreground"
        } ${value && mono ? "font-mono" : ""}`}
      >
        {value ?? "—"}
      </dd>
    </div>
  );
}

export function ForgeEnvironmentsPage() {
  const navigate = useNavigate();
  const { project: projectParam, env: envParam } = useSearch({
    from: "/_authenticated/_forge/forge/environments",
  });
  const currentProject = useProjectStore((state) => state.currentProject);

  // The URL is the source of truth once the layout has resolved it; the store
  // is the fallback for the tick before the param lands.
  const projectId = projectParam ?? currentProject?.id ?? null;

  const topology = useForgeTopology(projectId);
  const outcome = topology.data;

  /**
   * The selection lives in the URL, not in component state, so a refresh keeps
   * it and the sidebar can carry it to the Status and Secrets screens. `dev`
   * is the pre-ledger default because every forge project has one, which means
   * the inventory starts loading on first paint instead of waiting for the
   * ledger to name an environment.
   */
  const selectedEnv = envParam ?? "dev";

  const selectEnv = useCallback(
    (env: string) => {
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, env }),
        replace: true,
      });
    },
    [navigate]
  );

  const envs = useMemo(
    () => (outcome?.kind === "report" ? environments(outcome.report) : []),
    [outcome]
  );

  /**
   * If the ledger does not declare the selected env, move to the first one it
   * does. Without this a project whose environments are named staging/prod
   * would sit on a `dev` forge will answer about with nothing useful — and the
   * same applies to a stale or hand-typed `?env=`.
   */
  useEffect(() => {
    if (envs.length === 0) return;
    if (!envs.some((candidate) => candidate.env === selectedEnv)) selectEnv(envs[0].env);
  }, [envs, selectedEnv, selectEnv]);

  // Scoped to the SELECTED env only: this one probes a live cluster, unlike
  // the ledger read above.
  const status = useForgeEnvStatus(projectId, selectedEnv);

  const header = (
    <PageHeader
      title="Environments"
      subtitle="Where this project deploys, and what is actually running there."
    />
  );

  if (topology.isLoading && !outcome) {
    return (
      <>
        {header}
        <p data-testid="environments-loading" className="text-sm text-muted-foreground">
          Reading this project&apos;s environments…
        </p>
      </>
    );
  }

  if (topology.error) {
    return (
      <>
        {header}
        <p data-testid="environments-error" className="text-sm text-destructive">
          {(topology.error as Error).message}
        </p>
      </>
    );
  }

  if (outcome && outcome.kind !== "report") {
    return (
      <>
        {header}
        {outcome.kind === "not-forge-project" ? (
          <NotForgeProject projectName={currentProject?.name} />
        ) : outcome.kind === "unsupported" ? (
          <ForgeUnsupported meta={outcome.meta} />
        ) : outcome.kind === "unreachable" ? (
          <ForgeUnreachable meta={outcome.meta} />
        ) : (
          <ForgeMalformed meta={outcome.meta} />
        )}
      </>
    );
  }

  if (envs.length === 0) {
    return (
      <>
        {header}
        <div
          data-testid="environments-empty"
          className="rounded-lg border border-dashed border-border px-6 py-12 text-center"
        >
          <p className="text-sm text-muted-foreground">
            This project declares no environments yet.
          </p>
        </div>
      </>
    );
  }

  const selected = envs.find((candidate) => candidate.env === selectedEnv);

  return (
    <div className="space-y-10">
      {header}

      {/*
       * The environment list. Selecting a row loads its inventory below
       * rather than navigating away — the question this screen answers is
       * "what is running in THIS environment", so leaving the page to answer
       * it would be the wrong move. The row still offers an explicit link to
       * the Status screen, which answers a different question (forge's
       * runtime checks) about the same environment.
       */}
      <ul data-testid="environments-list" className="space-y-2">
        {envs.map((env) => {
          const active = env.env === selectedEnv;
          return (
            <li key={env.env}>
              <button
                type="button"
                data-testid={`environment-card-${env.env}`}
                data-selected={active}
                aria-pressed={active}
                onClick={() => selectEnv(env.env)}
                className={`w-full rounded-lg border bg-card px-5 py-4 text-left transition-colors ${
                  active ? "border-primary" : "border-border hover:border-border-strong"
                }`}
              >
                <div className="flex items-center justify-between gap-3">
                  {/* An env name is an identifier. */}
                  <span className="font-mono text-sm font-medium text-foreground">
                    {env.env}
                  </span>
                  <StatusDot
                    variant={env.bound ? "active" : "neutral"}
                    label={env.bound ? "Promoted" : "Never promoted"}
                    size="sm"
                  />
                </div>
                {env.note && (
                  <p className="mt-2 text-sm text-muted-foreground">{env.note}</p>
                )}
                <dl className="mt-4 grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
                  <Field label="Release" value={env.release} mono />
                  <Field label="Cluster" value={env.kube_context} mono />
                  <Field label="Namespace" value={env.namespace} mono />
                  <Field
                    label="Declared"
                    value={env.declared ? "In this checkout" : "Not in this checkout"}
                  />
                </dl>
              </button>
            </li>
          );
        })}
      </ul>

      {selected && (
        <div className="space-y-4">
          <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-2">
            <p className="text-sm text-muted-foreground">
              Showing what <span className="font-mono text-foreground">{selected.env}</span>{" "}
              deploys.
            </p>
            <button
              type="button"
              onClick={() =>
                void navigate({
                  to: "/forge/status",
                  search: { project: projectId ?? undefined, env: selected.env },
                })
              }
              className="text-sm text-muted-foreground underline-offset-4 transition-colors hover:text-foreground hover:underline"
            >
              Runtime checks for {selected.env}
            </button>
          </div>

          <WorkloadInventory
            outcome={status.data}
            isLoading={status.isLoading}
            error={status.error as Error | null}
            env={selected.env}
            projectName={currentProject?.name}
          />
        </div>
      )}
    </div>
  );
}
