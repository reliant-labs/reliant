// Copyright (c) 2025 Reliant Labs

/**
 * RELEASES — the project's one list of releases, with what is
 * where across environments at a glance.
 *
 *   header      the delivery figures for the scoped environment (Delivery
 *               folds in here as a strip of DORA facts, each with the server's
 *               own definition on hover).
 *   pipeline    one cell per long-lived environment: the release live there,
 *               how far it is from prod, who put it there and when, and any
 *               scoped deploy on top; the previews share one cell.
 *   table       one row per release: commit subject, short version and tags,
 *               sha, checks, ONE COLUMN PER LONG-LIVED
 *               ENVIRONMENT (● live · ◐ partly live · ○ ran before · · never),
 *               previews, author, and a row menu whose actions name their
 *               environment.
 *
 * The top-bar scope (default prod) highlights its column and, by default,
 * narrows the list to the releases that ran there; one click shows the rest.
 */

import { useMemo, useState, type ReactNode } from "react";
import { ChevronDown, Search, Tag as TagIcon } from "lucide-react";

import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Link from "@/components/forge-ui/link";
import { DOCS, GettingStarted } from "@/components/Forge/GettingStarted";
import Mono from "@/components/forge-ui/mono";
import PageHeader from "@/components/forge-ui/page_header";
import { useDoraMetrics, useObservabilityOrg } from "@/hooks/observability-queries";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import { cn } from "@/lib/utils";

import { formatPerDay, formatSpan } from "../Observability/deliveryScope";
import { ENV_ALL } from "../Console/envScope";
import {
  Ago,
  ChecksMark,
  EnvMark,
  Note,
  PreviewsMark,
  PromoteTargetDialog,
  ReleaseTags,
  INK_BUTTON,
  INK_LINK,
  RowMenu,
  scopedLabel,
  type MenuItem,
} from "./releaseParts";
import {
  STATUS_FILTER_LABELS,
  authorsOf,
  filterRows,
  referenceEnv,
  shortVersion,
  type PipelineCell,
  type ReleaseEnv,
  type ReleaseModel,
  type ReleaseRow,
  type StatusFilter,
} from "./releaseModel";
import { useReleaseData } from "./useReleaseData";

export interface ReleasesSearch {
  q?: string;
  author?: string;
  status?: StatusFilter;
  /** Show every release, not only those that ran in the scoped environment. */
  every?: boolean;
  /** The deploy whose output is open in the Deploys section. */
  deploy?: string;
}

export interface PromoteRequest {
  version: string;
  env: string;
  intent: "promote" | "rollback";
}

/** What the page lets a reader do: promote (through the plan), or why not. */
export interface PromoteControl {
  onPromote: (request: PromoteRequest) => void;
  /** Set when promoting is impossible from here, with the reason; the buttons are then disabled and say so. */
  blockedReason: string | null;
}

export interface ReleasesListProps {
  project: string;
  /** The top-bar scope: an environment name or "all". */
  env: string;
  envs: ReleaseEnv[];
  envsLoading: boolean;
  search: ReleasesSearch;
  onSearch: (next: Partial<ReleasesSearch>) => void;
  releaseHref: (version: string, search?: Record<string, unknown>) => string;
  promote: PromoteControl;
  /** The scope names a local environment, which runs the working tree and has no releases by design. */
  scopeIsLocal?: boolean;
}

/** The environment a mutating action targets when the scope is not one long-lived environment. */
function actionEnvOf(env: string, model: ReleaseModel): string | null {
  if (model.longEnvs.some((candidate) => candidate.name === env)) return env;
  return referenceEnv(model.longEnvs)?.name ?? null;
}

export function ReleasesList({ project, env, envs, envsLoading, search, onSearch, releaseHref, promote, scopeIsLocal = false }: ReleasesListProps) {
  const data = useReleaseData(project, envs);
  const [promoting, setPromoting] = useState<string | null>(null);
  const model = data.model;

  const scopedEnv = envs.find((candidate) => candidate.name === env) ?? null;
  const everyRelease = env === ENV_ALL || !!search.every;
  const rows = useMemo(
    () =>
      model
        ? filterRows(model.rows, { env, everyRelease, query: search.q ?? "", author: search.author, status: search.status })
        : [],
    [model, env, everyRelease, search.q, search.author, search.status]
  );
  const hiddenByScope = useMemo(
    () => (model && !everyRelease ? filterRows(model.rows, { env, everyRelease: true, query: search.q ?? "", author: search.author, status: search.status }).length - rows.length : 0),
    [model, everyRelease, env, search.q, search.author, search.status, rows.length]
  );
  const bases = useMemo(() => new Map(model?.pipeline.map((cell) => [cell.env.name, cell.base]) ?? []), [model]);

  const loading = envsLoading || data.releases.isLoading;
  // No "Cut release" action, though the mockup draws one: a release is cut by
  // `forge env deploy` / `forge release cut` from the checkout, and there is no
  // console flow that cuts without deploying (the mockup's button only says so
  // in a toast). Deploying lives on each environment's page.
  const header = <PageHeader title="Releases" meta={<DeliveryFacts project={project} env={env} scopedEnv={scopedEnv} />} className="mb-0" />;

  if (data.releases.error) {
    return (
      <div className="space-y-6" data-testid="releases" data-state="error">
        {header}
        <Card className="space-y-3" data-testid="releases-error">
          <p className="text-sm font-medium text-ink">The release ledger could not be read</p>
          <p className="text-sm text-ink-muted">
            {(data.releases.error as Error).message || "The control plane did not answer."} Nothing is wrong with the releases themselves; this
            page could not ask for them.
          </p>
          <button
            type="button"
            onClick={() => void data.releases.refetch()}
            className="inline-flex h-8 items-center rounded-md border border-border bg-surface px-3 text-sm font-medium text-ink hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          >
            {data.releases.isFetching ? "Retrying…" : "Try again"}
          </button>
        </Card>
      </div>
    );
  }

  if (!loading && model && model.rows.length === 0) {
    return (
      <div className="space-y-6" data-testid="releases" data-state="empty">
        {header}
        <GettingStarted
          testId="releases-getting-started"
          icon={<TagIcon className="h-4 w-4" />}
          title="No releases yet"
          about={`Releases lists every build of ${project} Reliant has recorded: the commit it came from, what it changed, which environments run it, and how each promotion went.`}
          steps={[
            {
              text: "Deploy from your checkout. It builds, cuts a release, deploys it, and the release appears here.",
              command: `forge env deploy ${env === ENV_ALL || !scopedEnv ? "<env>" : env}`,
            },
            {
              text: "Promote that release to the next environment: exactly what staging runs, with nothing rebuilt.",
              command: "forge env deploy prod --from staging",
            },
          ]}
          docs={{ href: DOCS.deploys, label: "How environments and deploys work" }}
        >
          <p>
            A release is cut the first time {project} is deployed through Reliant. A local environment (forge env up) runs your working tree
            and never has releases.
          </p>
        </GettingStarted>
      </div>
    );
  }

  const authors = model ? authorsOf(model.rows) : [];
  const filtered = !!(search.q || search.author || search.status);

  return (
    <div className="space-y-6" data-testid="releases" data-state={loading ? "loading" : "ready"}>
      {header}

      {loading || !model ? <PipelineSkeleton /> : <Pipeline model={model} env={env} releaseHref={releaseHref} />}

      {data.ledgerFailed.length > 0 && (
        <Note tone="warning" testId="releases-ledger-failed">
          The promotion history of {data.ledgerFailed.join(", ")} could not be read, so its column shows only what is live there now, not what
          ran before.
        </Note>
      )}

      <div className="space-y-4">
        <div className="flex flex-wrap items-center gap-2" data-testid="releases-toolbar">
          <label className="flex h-8 min-w-72 items-center gap-1.5 rounded-md border border-border bg-surface px-2.5 text-ink-muted focus-within:ring-2 focus-within:ring-accent">
            <Search className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span className="sr-only">Filter releases</span>
            <input
              value={search.q ?? ""}
              onChange={(event) => onSearch({ q: event.target.value || undefined })}
              placeholder="Filter by version, sha or subject"
              className="min-w-0 flex-1 bg-transparent text-sm text-ink placeholder:text-ink-muted focus:outline-none"
              data-testid="releases-filter"
            />
          </label>
          {env !== ENV_ALL && (
            <Chip
              on={!search.every}
              onClick={() => onSearch({ every: search.every ? undefined : true })}
              testId="releases-chip-env"
              title={search.every ? `Show only the releases that ran in ${env}` : `Showing the releases that ran in ${env}. Click to show every release.`}
            >
              Ran in {env}
            </Chip>
          )}
          <ChipMenu
            label="Author"
            value={search.author}
            options={authors.map((author) => ({ value: author, label: author }))}
            onChange={(author) => onSearch({ author })}
            testId="releases-chip-author"
          />
          <ChipMenu
            label="Status"
            value={search.status}
            options={(Object.keys(STATUS_FILTER_LABELS) as StatusFilter[]).map((status) => ({ value: status, label: STATUS_FILTER_LABELS[status] }))}
            onChange={(status) => onSearch({ status: status as StatusFilter | undefined })}
            testId="releases-chip-status"
          />
          {filtered && (
            <button
              type="button"
              onClick={() => onSearch({ q: undefined, author: undefined, status: undefined })}
              className="inline-flex h-7 items-center rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink"
            >
              Clear
            </button>
          )}
        </div>

        <ReleaseTable
          model={model}
          rows={rows}
          loading={loading}
          env={env}
          releaseHref={releaseHref}
          menuFor={(row) => (model ? rowMenu(row, model, env, bases, promote, setPromoting, releaseHref) : [])}
          empty={
            filtered ? (
              <>
                No release matches these filters.{" "}
                <button type="button" className={INK_BUTTON} onClick={() => onSearch({ q: undefined, author: undefined, status: undefined })}>
                  Clear them
                </button>
              </>
            ) : scopeIsLocal ? (
              <span data-testid="releases-local-env">
                {env} runs locally from your working tree (forge env up), so it never has releases.{" "}
                <button type="button" className={INK_BUTTON} onClick={() => onSearch({ every: true })}>
                  Show every release
                </button>
              </span>
            ) : (
              <>
                No release has run in {env} yet.{" "}
                <button type="button" className={INK_BUTTON} onClick={() => onSearch({ every: true })}>
                  Show every release
                </button>
              </>
            )
          }
          footer={
            <>
              <span aria-hidden="true">● live · ◐ live for some components (scoped) · ○ ran before · · never.</span>{" "}
              {!everyRelease && (
                <>
                  Showing the releases that ran in {env}.
                  {hiddenByScope > 0 && (
                    <>
                      {" "}
                      <button type="button" className={INK_BUTTON} onClick={() => onSearch({ every: true })} data-testid="releases-show-hidden">
                        Show {hiddenByScope} more that never did
                      </button>
                    </>
                  )}
                </>
              )}
            </>
          }
        />
      </div>

      {promoting && model && (
        <PromoteTargetDialog
          version={promoting}
          envs={model.longEnvs}
          bases={bases}
          initialEnv={env}
          onCancel={() => setPromoting(null)}
          onConfirm={(target) => {
            setPromoting(null);
            promote.onPromote({ version: promoting, env: target, intent: "promote" });
          }}
        />
      )}
    </div>
  );
}

function rowMenu(
  row: ReleaseRow,
  model: ReleaseModel,
  env: string,
  bases: ReadonlyMap<string, string | null>,
  promote: PromoteControl,
  openPromote: (version: string) => void,
  releaseHref: ReleasesListProps["releaseHref"]
): MenuItem[] {
  const version = row.release.version;
  const target = actionEnvOf(env, model);
  const base = target ? (bases.get(target) ?? null) : null;
  const rank = (v: string | null) => (v ? model.rows.findIndex((candidate) => candidate.release.version === v) : -1);
  const blocked = promote.blockedReason ?? undefined;
  const items: MenuItem[] = [
    { label: "Promote to…", disabled: !!blocked, hint: blocked, testId: "release-menu-promote", onSelect: () => openPromote(version) },
  ];
  // Rolling back is promoting an OLDER release than the one live there. When
  // the live release is outside the loaded list (rank -1) nothing here is
  // known to be older than it, so no rollback is offered.
  if (target && base && rank(base) >= 0 && rank(version) > rank(base)) {
    items.push({
      label: `Roll back ${target} to this`,
      detail: `now ${shortVersion(base)}`,
      disabled: !!blocked,
      hint: blocked,
      testId: "release-menu-rollback",
      onSelect: () => promote.onPromote({ version, env: target, intent: "rollback" }),
    });
  }
  if (target && base && base !== version) {
    items.push({
      label: `Compare with ${target}`,
      detail: shortVersion(base),
      testId: "release-menu-compare",
      href: releaseHref(version, { tab: "changes", base }),
    });
  }
  return items;
}

// ── Delivery facts (DORA) ───────────────────────────────────────────────────

const DORA_DAYS = 30;

function DeliveryFacts({ project, env, scopedEnv }: { project: string; env: string; scopedEnv: ReleaseEnv | null }) {
  const org = useObservabilityOrg();
  const scope = useMemo(
    () => (org.orgId ? { orgId: org.orgId, projectId: project, envIds: env === ENV_ALL || !scopedEnv ? [] : [scopedEnv.id] } : null),
    [org.orgId, project, env, scopedEnv]
  );
  const dora = useDoraMetrics(scope, DORA_DAYS, 24);
  const where = env === ENV_ALL ? "all environments" : env;
  if (dora.isLoading || org.isLoading) {
    return (
      <span className="inline-flex h-4 w-80 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-label="Loading delivery figures" data-testid="releases-dora-loading" />
    );
  }
  if (!dora.data) {
    return (
      <span data-testid="releases-dora-unavailable" title={(dora.error as Error | null)?.message}>
        Delivery figures for {where} are unavailable right now
      </span>
    );
  }
  const { deploymentFrequency: df, leadTime: lt, changeFailureRate: cfr, timeToRestore: ttr, alertTimeToRestore: alerts } = dora.data;
  // Each figure carries the server's own definition, plus what it leaves out.
  const notCounted = [df.rollbacks && `${df.rollbacks} rollback${df.rollbacks === 1 ? "" : "s"}`, df.reapplies && `${df.reapplies} re-apply`].filter(Boolean);
  return (
    <span className="contents" data-testid="releases-dora">
      <span title={`${df.definition} ${formatPerDay(df.perDay)}.${notCounted.length ? ` Not counted: ${notCounted.join(", ")}.` : ""}`}>
        {df.deployments} {df.deployments === 1 ? "deployment" : "deployments"} to {where} in the last {DORA_DAYS} days
      </span>
      <span
        title={`${lt.definition}${lt.unknownChanges ? ` ${lt.unknownChanges} deployments (${lt.unknownReleases} releases) recorded no commit time and are not in the figure.` : ""}`}
      >
        Lead time, median {lt.medianMs == null ? "unknown" : formatSpan(lt.medianMs)}
      </span>
      <span title={cfr.definition}>Change failure {cfr.changes ? `${cfr.failedChanges} of ${cfr.changes}` : "—"}</span>
      {(ttr.failures > 0 || (alerts && alerts.failures > 0)) && (
        <span
          title={[ttr.definition, alerts && `From alerts: ${alerts.failures ? formatSpan(alerts.medianMs) : "nothing resolved"}${alerts.ongoing ? `, ${alerts.ongoing} ongoing` : ""}.`]
            .filter(Boolean)
            .join(" ")}
        >
          Restore, median {formatSpan(ttr.failures > 0 ? ttr.medianMs : (alerts?.medianMs ?? null))}
        </span>
      )}
    </span>
  );
}

// ── Pipeline strip ──────────────────────────────────────────────────────────

function relationText(cell: PipelineCell): { text: string; warn: boolean } {
  const r = cell.relation;
  switch (r.kind) {
    case "current":
      return { text: "current", warn: false };
    case "newer":
      return { text: `${r.count} newer ${r.count === 1 ? "release" : "releases"} not here`, warn: false };
    case "behind":
      return { text: `${r.count} behind ${r.of}`, warn: true };
    case "ahead":
      return { text: `${r.count} ahead of ${r.of}`, warn: false };
    case "unknown":
      return { text: cell.base ? "older than the releases listed" : "nothing deployed yet", warn: false };
  }
}

function Pipeline({
  model,
  env,
  releaseHref,
}: {
  model: ReleaseModel;
  env: string;
  releaseHref: ReleasesListProps["releaseHref"];
}) {
  if (model.pipeline.length === 0 && model.previews.length === 0) return null;
  const previewSelected = model.previews.some((candidate) => candidate.name === env);
  return (
    <div
      className="grid overflow-hidden rounded-lg border border-border bg-surface"
      style={{ gridTemplateColumns: `repeat(${model.pipeline.length + (model.previews.length ? 1 : 0)}, minmax(0, 1fr))` }}
      data-testid="releases-pipeline"
    >
      {model.pipeline.map((cell, index) => {
        const relation = relationText(cell);
        const selected = cell.env.name === env;
        return (
          <PipelineCellFrame key={cell.env.name} first={index === 0} selected={selected} testId={`pipeline-${cell.env.name}`}>
            <div className={cn("text-xs", selected ? "font-medium text-ink" : "text-ink-muted")}>{cell.env.name}</div>
            <div className="mt-1 truncate text-sm font-medium text-ink">
              {cell.base ? (
                <Link href={releaseHref(cell.base)} className={INK_LINK} title={cell.base}>
                  {cell.base}
                </Link>
              ) : (
                <span className="font-normal text-ink-muted">No release</span>
              )}
            </div>
            <div className={cn("text-xs", relation.warn ? "text-warning-ink" : "text-ink-muted")}>{relation.text}</div>
            <div className="truncate text-xs text-ink-muted">
              {cell.base && (
                <>
                  {cell.by ? `promoted by ${cell.by}` : "promoted"}
                  {cell.at && " · "}
                  <Ago at={cell.at} />
                </>
              )}
              {cell.followUps[0] && (
                <span title={cell.followUps.map((f) => `${shortVersion(f.version)}: ${f.targets.join(", ")}`).join("\n")}>
                  {" "}
                  · {scopedLabel(cell.followUps[0].targets)} on a scoped release
                </span>
              )}
            </div>
          </PipelineCellFrame>
        );
      })}
      {model.previews.length > 0 && (
        <PipelineCellFrame first={model.pipeline.length === 0} selected={previewSelected} testId="pipeline-previews">
          <div className="text-xs text-ink-muted">previews</div>
          <div className="mt-1 text-sm font-medium text-ink">
            {model.previews.length} running
          </div>
          <div className="flex flex-wrap gap-x-1.5 text-xs text-ink-muted">
            {model.previews.map((preview, index) => (
              <span key={preview.name}>
                {index > 0 && <span aria-hidden="true">· </span>}
                <span className={cn(preview.name === env && "font-medium text-ink")}>{preview.name}</span>
              </span>
            ))}
          </div>
          <div className="text-xs text-ink-muted">one column in the table</div>
        </PipelineCellFrame>
      )}
    </div>
  );
}

function PipelineCellFrame({ first, selected, testId, children }: { first: boolean; selected: boolean; testId: string; children: ReactNode }) {
  return (
    <div data-testid={testId} data-selected={selected ? "true" : undefined} className={cn("relative min-w-0 px-4 py-3", !first && "border-l border-border")}>
      {children}
      {/* The scoped environment: an ink rule along the cell's foot, as the table highlights its column. */}
      {selected && <span aria-hidden="true" className="absolute inset-x-0 bottom-0 h-0.5 bg-ink" />}
    </div>
  );
}

function PipelineSkeleton() {
  return (
    <div className="grid grid-cols-4 overflow-hidden rounded-lg border border-border bg-surface" aria-hidden="true" data-testid="releases-pipeline-loading">
      {[0, 1, 2, 3].map((i) => (
        <div key={i} className={cn("space-y-2 px-4 py-3", i > 0 && "border-l border-border")}>
          <div className="h-3 w-10 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
          <div className="h-4 w-40 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
          <div className="h-3 w-24 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
        </div>
      ))}
    </div>
  );
}

// ── Toolbar chips ───────────────────────────────────────────────────────────

const CHIP =
  "inline-flex h-7 items-center gap-1 rounded-full border bg-surface px-2.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";
/** Solid border on the surface fill, as the mockup's chips; a set filter reads in ink with a stronger border. */
const chipState = (on: boolean) => (on ? "border-ink-subtle text-ink" : "border-border text-ink-muted hover:border-ink-subtle hover:text-ink");

function Chip({ on, onClick, children, title, testId }: { on: boolean; onClick: () => void; children: ReactNode; title?: string; testId?: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={on}
      title={title}
      data-testid={testId}
      className={cn(CHIP, chipState(on))}
    >
      {children}
    </button>
  );
}

function ChipMenu({
  label,
  value,
  options,
  onChange,
  testId,
}: {
  label: string;
  value?: string;
  options: { value: string; label: string }[];
  onChange: (value: string | undefined) => void;
  testId?: string;
}) {
  const [open, setOpen] = useState(false);
  const escapeLayer = useEscapeLayer(open, () => setOpen(false));
  const current = options.find((option) => option.value === value);
  return (
    <div
      className="relative"
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false);
      }}
    >
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((prev) => !prev)}
        data-testid={testId}
        className={cn(CHIP, chipState(!!current))}
      >
        {current ? `${label}: ${current.label}` : label}
        <ChevronDown className="h-3 w-3" aria-hidden="true" />
      </button>
      {open && (
        <div role="menu" {...escapeLayer} className="absolute left-0 top-8 z-50 min-w-44 rounded-lg border border-border bg-surface p-1 shadow-md">
          {options.length === 0 && <div className="px-2 py-1.5 text-xs text-ink-muted">Nothing to filter by</div>}
          {current && (
            <button
              type="button"
              role="menuitem"
              onClick={() => {
                setOpen(false);
                onChange(undefined);
              }}
              className="flex h-8 w-full items-center rounded-md px-2 text-left text-sm text-ink-muted hover:bg-ink/5 hover:text-ink"
            >
              Any {label.toLowerCase()}
            </button>
          )}
          {options.map((option) => (
            <button
              key={option.value}
              type="button"
              role="menuitemradio"
              aria-checked={option.value === value}
              onClick={() => {
                setOpen(false);
                onChange(option.value);
              }}
              className={cn("flex h-8 w-full items-center rounded-md px-2 text-left text-sm text-ink hover:bg-ink/5", option.value === value && "font-medium")}
            >
              {option.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ── Table ───────────────────────────────────────────────────────────────────

const ENV_TD = "w-16 text-center";

type ReleaseTableRow = { version: string; row: ReleaseRow };

function ReleaseTable({
  model,
  rows,
  loading,
  env,
  releaseHref,
  menuFor,
  empty,
  footer,
}: {
  model: ReleaseModel | null;
  rows: ReleaseRow[];
  loading: boolean;
  env: string;
  releaseHref: ReleasesListProps["releaseHref"];
  menuFor: (row: ReleaseRow) => MenuItem[];
  empty: ReactNode;
  footer: ReactNode;
}) {
  const longEnvs = model?.longEnvs ?? [];
  const hasPreviews = (model?.previews.length ?? 0) > 0;
  const previewSelected = !!model?.previews.some((candidate) => candidate.name === env);
  // The scoped environment's column is a SELECTION, the one structural use
  // the web-styling contract allows `bg-surface-sunken` (interaction state): it lifts
  // the column in dark and tints it in light, as the mockup's muted-55% does.
  // `bg-surface-muted` (= --background) would recess it into a black stripe.
  const sel = (on: boolean) => (on ? "bg-surface-sunken" : "");
  const columns: DataTableColumn<ReleaseTableRow>[] = [
    {
      key: "release",
      header: "Release",
      className: "w-full max-w-0",
      render: (_, { row, version }) => (
        <>
          <span className="block truncate font-medium" title={version}>
            {row.release.subject || "No commit subject recorded"}
          </span>
          <span className="mt-1 flex items-center gap-2 text-xs font-normal text-ink-muted">
            <span title={version}>{shortVersion(version)}</span>
            <ReleaseTags row={row} />
          </span>
        </>
      ),
    },
    {
      key: "commit",
      header: "Commit",
      interactive: true,
      render: (_, { row }) =>
        row.release.gitCommit ? <Mono value={row.release.gitCommit} /> : <span className="text-ink-muted">No commit recorded</span>,
    },
    { key: "checks", header: "Checks", render: (_, { row }) => <ChecksMark checks={row.checks} /> },
    ...longEnvs.map(
      (candidate): DataTableColumn<ReleaseTableRow> => ({
        key: `env:${candidate.name}`,
        header: candidate.name,
        className: cn(ENV_TD, sel(candidate.name === env)),
        headerClassName: cn(ENV_TD, sel(candidate.name === env)),
        render: (_, { row }) => <EnvMark env={candidate.name} cell={row.cells[candidate.name]!} />,
      }),
    ),
    ...(hasPreviews
      ? [
          {
            key: "previews",
            header: "previews",
            className: cn(ENV_TD, sel(previewSelected)),
            headerClassName: cn(ENV_TD, sel(previewSelected)),
            render: (_: unknown, { row }: ReleaseTableRow) => <PreviewsMark names={row.previewsLive} />,
          },
        ]
      : []),
    {
      key: "author",
      header: "Author",
      render: (_, { row }) => (
        <>
          {row.release.author ? <span>{row.release.author}</span> : <span className="text-ink-muted">unknown</span>}
          <span className="text-ink-muted">
            {" · "}
            <Ago at={row.release.createdAt} />
          </span>
        </>
      ),
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      interactive: true,
      width: "2.5rem",
      className: "px-2",
      render: (_, { row, version }) => <RowMenu label={`Actions for ${version}`} items={menuFor(row)} testId={`release-menu-${version}`} />,
    },
  ];
  return (
    <Card padding="none" className="overflow-hidden">
      <div data-testid="releases-table">
        <DataTable<ReleaseTableRow>
          flush
          stacked
          ariaLabel="Releases"
          columns={columns}
          data={rows.map((row) => ({ version: row.release.version, row }))}
          loading={loading}
          pageSize={6}
          emptyMessage={<span data-testid="releases-empty-filtered">{empty}</span>}
          getRowKey={(r) => r.version}
          getRowHref={(r) => releaseHref(r.version)}
          getRowLabel={({ row, version }) => `${row.release.subject || "No commit subject recorded"} · ${version}`}
          getRowProps={(r) => ({ "data-testid": `release-row-${r.version}` })}
        />
      </div>
      <div className="border-t border-border px-4 py-2.5 text-xs text-ink-muted" data-testid="releases-footer">
        {footer}
      </div>
    </Card>
  );
}
