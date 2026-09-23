// Copyright (c) 2025 Reliant Labs

/**
 * THE SCREEN THAT ANSWERS "what is actually running in this environment?"
 *
 * The bug it replaces, stated plainly because it is the whole reason this
 * component has the shape it does: the Environments screen used to render the
 * env-status report's `services` array — the HOST PROCESSES on the reader's
 * own laptop — under a heading that read like a deployment inventory. For
 * control-plane's prod that showed two local dev servers while the cluster ran
 * sixteen workloads, and a user asked, reasonably, why prod only had two
 * services. This renders forge's `workloads` document instead: real
 * deployments, real pods, read from a named cluster.
 *
 * PURE PROPS, like EnvStatusPanel and TopologyView. It takes an outcome and
 * renders it and owns no fetching, which is what lets the visual contract be
 * pinned by handing it a report object instead of standing up a transport.
 *
 * THE CERTAINTY DISCIPLINE, AS LAYOUT. Every branch below is on `posture` —
 * never on `workloads.length` — because forge lists a workload it could NOT
 * see, with status unknown and no pods. An unreachable prod therefore renders
 * sixteen rows that each say "state unknown", above a banner carrying kubectl's
 * verbatim error. It never renders as an empty screen, and never as a green
 * tick. The five postures are five different sentences; none of them is
 * silence.
 *
 * WHERE THE DATA CAME FROM IS PART OF THE ANSWER. The cluster context and
 * namespace are named at the top, always, and again per row when the
 * environment spans more than one scope (control-plane's dev spans
 * k3d-control-plane and k3d-cp-daemon). The old screen's ambiguity about what
 * it was describing is what made it misleading, so this one is never ambiguous
 * about it.
 *
 * SURFACES. Page is `bg-background`; each surface is `bg-card` + `border-border`;
 * anything inset is `bg-background` + `border-border/60`. Cards are never
 * nested, and `bg-muted` is never used for structure — `--muted` inverts
 * direction between light and dark across the ten schemes, so a muted-backed
 * panel recesses in one mode and lifts in the other.
 */

import { useState } from "react";
import { AlertTriangle, Boxes } from "lucide-react";

import { cn } from "@/lib/utils";
import { HelpPopover } from "@/components/ui/HelpPopover";
import DataTable from "@/components/forge-ui/data_table";
import StatGrid from "@/components/forge-ui/stat_grid";
import type { ForgeOutcome } from "@/services/forge/topology";
import type { ForgeEnvStatusReport } from "@/services/forge/status";
import {
  distinctScopes,
  findingsOf,
  inventoryOf,
  inventorySentence,
  podsOf,
  posture,
  replicas,
  scopeOf,
  scopesOf,
  statusTally,
  workloadsOf,
  type ForgeWorkloadState,
} from "@/services/forge/workloads";

import {
  ForgeMalformed,
  ForgeUnreachable,
  ForgeUnsupported,
  NotForgeProject,
} from "../ForgeStates";
import {
  normalizeStatus,
  WorkloadStatus,
  WorkloadStatusDot,
  WORKLOAD_STATUSES,
  WORKLOAD_STATUS_BLURBS,
  WORKLOAD_STATUS_LABELS,
} from "./workloadVocabulary";

export interface WorkloadInventoryProps {
  outcome: ForgeOutcome<ForgeEnvStatusReport> | undefined;
  isLoading: boolean;
  /** A transport/daemon failure — genuinely an error, unlike every outcome above. */
  error?: Error | null;
  /** Which environment was asked about, for copy when the report omits it. */
  env: string;
  projectName?: string;
}

/** The vocabulary, on demand rather than occupying the top of the page permanently. */
function StatusHelp() {
  return (
    <div className="space-y-3">
      <p className="text-xs text-muted-foreground">
        A workload is one pod-owning object this environment&apos;s render declares — a
        Deployment, a Job, a StatefulSet. Its status says what the cluster reported.
      </p>
      {WORKLOAD_STATUSES.map((status) => (
        <div key={status}>
          <p className="flex items-center gap-2 text-xs font-medium text-foreground">
            <WorkloadStatusDot status={status} />
            {WORKLOAD_STATUS_LABELS[status]}
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {WORKLOAD_STATUS_BLURBS[status]}
          </p>
        </div>
      ))}
    </div>
  );
}

export function WorkloadInventory({
  outcome,
  isLoading,
  error,
  env,
  projectName,
}: WorkloadInventoryProps) {
  /**
   * Pods are collapsed by default and this is not a space saving. Sixteen
   * workloads expand to twenty-odd pods whose names are churning hashes; shown
   * always, they bury the one row that matters. Findings — the actionable
   * sentences — are never behind this toggle.
   */
  const [showPods, setShowPods] = useState(false);

  if (isLoading && !outcome) {
    return (
      <p data-testid="workloads-loading" className="text-sm text-muted-foreground">
        Reading what <span className="font-mono">{env}</span> deploys…
      </p>
    );
  }

  if (error && !outcome) {
    return (
      <p data-testid="workloads-error" className="text-sm text-destructive">
        Could not reach your daemon to ask what {env} deploys: {error.message}
      </p>
    );
  }

  if (!outcome) return null;

  // Each of these is a SUCCESSFUL RPC with a different meaning. See ForgeStates.
  if (outcome.kind === "not-forge-project") return <NotForgeProject projectName={projectName} />;
  if (outcome.kind === "unsupported") return <ForgeUnsupported meta={outcome.meta} />;
  if (outcome.kind === "unreachable") return <ForgeUnreachable meta={outcome.meta} />;
  if (outcome.kind === "malformed") return <ForgeMalformed meta={outcome.meta} />;

  const inventory = inventoryOf(outcome.report);
  const stance = posture(inventory);
  const workloads = workloadsOf(inventory);
  const scopes = scopesOf(inventory);
  const tally = statusTally(inventory);
  const multiScope = distinctScopes(inventory) > 1;

  return (
    <section className="space-y-6" data-testid="workload-inventory" data-posture={stance}>
      <header className="space-y-1">
        <div className="flex items-center gap-2">
          <h2 className="text-sm font-medium text-foreground">Deployed workloads</h2>
          <HelpPopover title="Workload status" content={<StatusHelp />} />
        </div>
        {/* The one sentence that names WHAT was counted and WHERE. */}
        <p data-testid="workloads-sentence" className="text-sm text-muted-foreground">
          {inventorySentence(inventory, env)}
        </p>
      </header>

      <ScopeList scopes={scopes} />

      {stance === "not-reported" || stance === "nothing" || stance === "inconsistent" ? (
        <EmptyInventory posture={stance} env={env} />
      ) : (
        <>
          <StatGrid
            columns={4}
            stats={[
              { value: String(workloads.length), label: "Workloads rendered" },
              {
                value: stance === "undetermined" ? "—" : String(tally.pass),
                label: "Running as declared",
              },
              {
                value: stance === "undetermined" ? "—" : String(tally.fail + tally.warn),
                label: "Not running as declared",
              },
              { value: String(tally.unknown), label: "State unknown" },
            ]}
          />

          <WorkloadTable workloads={workloads} showScope={multiScope} />

          <Findings workloads={workloads} />

          <div className="flex justify-start">
            <button
              type="button"
              data-testid="workloads-toggle-pods"
              onClick={() => setShowPods((previous) => !previous)}
              className="text-sm text-muted-foreground underline-offset-4 transition-colors hover:text-foreground hover:underline"
            >
              {showPods ? "Hide pods" : "Show pods"}
            </button>
          </div>

          {showPods && <PodList workloads={workloads} />}
        </>
      )}
    </section>
  );
}

/**
 * The (context, namespace) pairs forge read, named explicitly and always.
 *
 * A scope that failed carries kubectl's error VERBATIM, and its
 * `rendered_workloads` count beside it — "we could not see 16 things" is the
 * fact an empty array cannot express, and it is the difference between an
 * environment that is empty and one that is merely unreadable.
 */
function ScopeList({ scopes }: { scopes: ReturnType<typeof scopesOf> }) {
  if (scopes.length === 0) return null;

  return (
    <ul data-testid="workload-scopes" className="space-y-2">
      {scopes.map((scope, index) => {
        const failed = scope.status !== "pass";
        const named = (scope.cluster ?? "") !== "";
        return (
          <li
            key={`${scope.cluster ?? ""}/${scope.namespace ?? ""}/${index}`}
            data-testid="workload-scope"
            data-scope-status={failed ? "unknown" : "pass"}
            className={cn(
              "rounded-lg border px-4 py-3",
              failed ? "border-destructive/30 bg-card" : "border-border bg-card"
            )}
          >
            <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
              <WorkloadStatusDot status={failed ? "unknown" : "pass"} />
              {named ? (
                <span className="font-mono text-sm text-foreground">
                  {scope.cluster}
                  {scope.namespace ? <span className="text-muted-foreground">/{scope.namespace}</span> : null}
                </span>
              ) : (
                <span className="text-sm text-muted-foreground">
                  No cluster context — forge did not resolve where this deploys
                </span>
              )}
              {typeof scope.rendered_workloads === "number" && (
                <span className="text-sm tabular-nums text-muted-foreground">
                  {scope.rendered_workloads}{" "}
                  {scope.rendered_workloads === 1 ? "workload" : "workloads"}
                </span>
              )}
            </div>

            {failed && scope.error && (
              /* Inset inside a card: bg-background, never bg-muted. */
              <p
                data-testid="workload-scope-error"
                className="mt-2 rounded-md border border-border/60 bg-background px-3 py-2 font-mono text-xs leading-relaxed text-muted-foreground"
              >
                {scope.error}
              </p>
            )}
          </li>
        );
      })}
    </ul>
  );
}

/** The three postures that render no table, each saying which one it is. */
function EmptyInventory({
  posture: stance,
  env,
}: {
  posture: "not-reported" | "nothing" | "inconsistent";
  env: string;
}) {
  const dashed = stance !== "nothing";
  return (
    <div
      data-testid="workloads-empty"
      data-empty-posture={stance}
      className={cn(
        "rounded-lg border px-6 py-12 text-center",
        dashed ? "border-dashed border-border" : "border-border bg-card"
      )}
    >
      <Boxes className="mx-auto h-5 w-5 text-muted-foreground" aria-hidden="true" />
      <p className="mt-3 text-sm text-muted-foreground">
        {stance === "nothing"
          ? `${env} deploys nothing to Kubernetes.`
          : stance === "not-reported"
            ? "This forge did not report cluster workloads."
            : "Forge reported a reading but listed no workloads."}
      </p>
    </div>
  );
}

/** A DataTable row. Keys match the column keys; values are rendered, not stringified. */
type WorkloadRow = Record<string, unknown> & { workload: ForgeWorkloadState & { name: string } };

/**
 * The inventory grid.
 *
 * The replica cell is where "omitted is not zero" becomes pixels: a Job has no
 * `desired_replicas` at all, so it renders an em dash and the word Job's own
 * ephemerality explains — never "0/0", which reads as a workload that should
 * have pods and does not. An unknown workload renders a dash too, because its
 * `ready_replicas` is 0 for want of a reading rather than for want of pods.
 */
function WorkloadTable({
  workloads,
  showScope,
}: {
  workloads: Array<ForgeWorkloadState & { name: string }>;
  showScope: boolean;
}) {
  const rows: WorkloadRow[] = workloads.map((workload) => ({ workload }));

  const columns = [
    {
      key: "workload",
      header: "Workload",
      render: (_value: unknown, row: WorkloadRow) => (
        // A workload name is an identifier; its kind is a category, so prose.
        <span className="font-mono text-sm text-foreground">{row.workload.name}</span>
      ),
    },
    {
      key: "kind",
      header: "Kind",
      render: (_value: unknown, row: WorkloadRow) => (
        <span className="text-sm capitalize text-muted-foreground">
          {row.workload.kind || "—"}
        </span>
      ),
    },
    {
      key: "status",
      header: "Status",
      render: (_value: unknown, row: WorkloadRow) => (
        <WorkloadStatus
          status={normalizeStatus(row.workload.status)}
          ephemeral={row.workload.ephemeral === true}
        />
      ),
    },
    {
      key: "replicas",
      header: "Ready",
      render: (_value: unknown, row: WorkloadRow) => <ReplicaCell workload={row.workload} />,
    },
    {
      key: "restarts",
      header: "Restarts",
      render: (_value: unknown, row: WorkloadRow) => <RestartCell workload={row.workload} />,
    },
    ...(showScope
      ? [
          {
            key: "scope",
            header: "Cluster",
            render: (_value: unknown, row: WorkloadRow) => <ScopeCell workload={row.workload} />,
          },
        ]
      : []),
  ];

  return (
    <div data-testid="workloads-table">
      <DataTable<WorkloadRow>
        columns={columns}
        data={rows}
        // The whole set, on one page, with NO pagination footer: this is a
        // complete inventory of a finite thing. Paging it would let a reader
        // conclude prod runs ten workloads because the eleventh was on page
        // two — the same false-shortfall this screen exists to end. The
        // footer is suppressed for the same reason rather than merely
        // disabled: "Rows per page: 10" above sixteen rows is a claim that
        // the list is truncated.
        pageSize={Math.max(rows.length, 1)}
        totalItems={rows.length}
        paginated={false}
        emptyMessage="Forge listed no workloads for this environment."
      />
    </div>
  );
}

function ReplicaCell({ workload }: { workload: ForgeWorkloadState }) {
  const reading = replicas(workload);

  if (reading.kind === "unmeasured") {
    return (
      <span
        data-replicas="unmeasured"
        title="Forge could not read this workload, so no replica count was obtained. This is not zero replicas."
        className="text-sm text-muted-foreground"
      >
        —
      </span>
    );
  }

  if (reading.kind === "no-replicas") {
    return (
      <span
        data-replicas="no-replicas"
        title={
          reading.ephemeral
            ? "This kind has no replica count. Its pods are expected to come and go, so no pods is not an alarm."
            : "This kind has no replica count."
        }
        className="text-sm text-muted-foreground"
      >
        —
      </span>
    );
  }

  return (
    <span
      data-replicas="counted"
      className={cn(
        "text-sm tabular-nums",
        reading.short ? "text-destructive" : "text-muted-foreground"
      )}
    >
      {reading.ready}/{reading.desired}
    </span>
  );
}

/**
 * Restarts, but only when there are any, and never for an unmeasured workload
 * — forge reports 0 there because it read nothing, and printing that zero
 * would be a measurement the screen does not have.
 */
function RestartCell({ workload }: { workload: ForgeWorkloadState }) {
  if (workload.status === "unknown") {
    return <span className="text-sm text-muted-foreground">—</span>;
  }
  const restarts = typeof workload.restarts === "number" ? workload.restarts : 0;
  if (restarts === 0) {
    return (
      <span aria-hidden="true" className="text-sm text-muted-foreground">
        —
      </span>
    );
  }
  return (
    <span
      data-restarts={restarts}
      title="The highest restart count across this workload's pods."
      className={cn("text-sm tabular-nums", restarts > 5 ? "text-warning" : "text-muted-foreground")}
    >
      {restarts}
    </span>
  );
}

/** Where this row was read from — shown only when the env spans several scopes. */
function ScopeCell({ workload }: { workload: ForgeWorkloadState }) {
  const scope = scopeOf(workload);
  if (!scope.routed) {
    return (
      <span
        data-scope="unrouted"
        title="The render did not say which cluster this workload deploys to. Forge never falls back to kubectl's current context, so neither does this screen."
        className="text-sm text-muted-foreground"
      >
        Unrouted
      </span>
    );
  }
  return (
    <span className="font-mono text-xs text-muted-foreground">
      {scope.cluster}
      {scope.namespace ? `/${scope.namespace}` : ""}
    </span>
  );
}

/**
 * Forge's own diagnoses, deduplicated.
 *
 * Dedup is not tidying. When a cluster is unreachable all sixteen workloads
 * carry the SAME sentence, and sixteen copies of one error is a wall that
 * hides how many distinct things are actually wrong. Grouped, it reads as one
 * problem affecting sixteen workloads — which is what it is.
 *
 * The text is DISPLAYED, never parsed. It is forge's prose and its format is a
 * display concern; branching on it is the workaround this whole screen exists
 * to replace.
 */
function Findings({ workloads }: { workloads: Array<ForgeWorkloadState & { name: string }> }) {
  const grouped = new Map<string, string[]>();
  for (const workload of workloads) {
    for (const finding of findingsOf(workload)) {
      const names = grouped.get(finding) ?? [];
      names.push(workload.name);
      grouped.set(finding, names);
    }
  }

  if (grouped.size === 0) return null;

  return (
    <section className="space-y-2" data-testid="workload-findings">
      <h3 className="flex items-center gap-2 text-sm font-medium text-foreground">
        <AlertTriangle className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
        What forge found
      </h3>
      <ul className="space-y-2">
        {[...grouped.entries()].map(([finding, names]) => (
          <li
            key={finding}
            data-testid="workload-finding"
            className="rounded-lg border border-border bg-card px-4 py-3"
          >
            <p className="text-sm leading-relaxed text-foreground">{finding}</p>
            <p className="mt-1.5 text-sm text-muted-foreground">
              {names.length === 1 ? (
                <span className="font-mono text-xs">{names[0]}</span>
              ) : (
                <>
                  {names.length} workloads:{" "}
                  <span className="font-mono text-xs">{names.join(", ")}</span>
                </>
              )}
            </p>
          </li>
        ))}
      </ul>
    </section>
  );
}

/**
 * The pods behind the workloads, on demand.
 *
 * A workload with no pods gets a line saying so IN ITS OWN TERMS: a completed
 * Job legitimately has none, an unreadable workload has none because nothing
 * was read, and collapsing those two into a blank row would put the screen's
 * central mistake back in a smaller place.
 */
function PodList({ workloads }: { workloads: Array<ForgeWorkloadState & { name: string }> }) {
  return (
    <div data-testid="workload-pods" className="space-y-3">
      {workloads.map((workload) => {
        const pods = podsOf(workload);
        return (
          <div
            key={workload.name}
            className="rounded-lg border border-border bg-card px-4 py-3"
          >
            <p className="font-mono text-sm text-foreground">{workload.name}</p>
            {pods.length === 0 ? (
              <p className="mt-1.5 text-sm text-muted-foreground">
                {workload.status === "unknown"
                  ? "No pods read — forge could not reach this workload's cluster."
                  : workload.ephemeral
                    ? "No pods running. This kind's pods are expected to come and go."
                    : "No pods matched this workload."}
              </p>
            ) : (
              <ul className="mt-2 space-y-1.5">
                {pods.map((pod) => (
                  <li
                    key={pod.name}
                    data-testid="workload-pod"
                    /* Inset inside a card: bg-background + border-border/60. */
                    className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-md border border-border/60 bg-background px-3 py-2"
                  >
                    <WorkloadStatusDot status={pod.ready ? "pass" : "warn"} />
                    <span className="font-mono text-xs text-foreground">{pod.name}</span>
                    <span className="text-sm tabular-nums text-muted-foreground">
                      {pod.containers_ready ?? 0}/{pod.containers ?? 0}
                    </span>
                    <span className="text-sm text-muted-foreground">{pod.phase || "—"}</span>
                    {typeof pod.restarts === "number" && pod.restarts > 0 && (
                      <span className="text-sm tabular-nums text-muted-foreground">
                        {pod.restarts} {pod.restarts === 1 ? "restart" : "restarts"}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        );
      })}
    </div>
  );
}
