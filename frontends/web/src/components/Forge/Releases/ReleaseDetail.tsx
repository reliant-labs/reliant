// Copyright (c) 2025 Reliant Labs

/**
 * ONE RELEASE: what it is, where it runs, what it changed.
 *
 *   header   the version, its status ("Live on prod, dev"; never plain "live"
 *            for a scoped deploy), the scoped tag, one meta line (cut by,
 *            sha, checks, when) and the two actions, each naming its
 *            environment: "Roll back prod to this…" (only when prod runs a
 *            newer release) and "Promote…".
 *   tabs     Changes · Rollout · Checks · Artifacts · Health (ReleaseTabs).
 *   rail     the promotion timeline across environments, the details (branch,
 *            commit, forge version, bundle, lead time) and the way to its
 *            history.
 *
 * The environment-bound tabs read one environment: the scoped one when the
 * release was promoted there, else the one it reached most recently, and the
 * reader can switch (`?in=`).
 */

import { useMemo, useState } from "react";
import { Tag as TagIcon } from "lucide-react";

import Card from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import PageHeader, { type PageHeaderAction } from "@/components/forge-ui/page_header";
import Status from "@/components/forge-ui/status";
import Tabs from "@/components/forge-ui/tabs";
import Tag from "@/components/forge-ui/tag";
import { useProjectRelease } from "@/hooks/release-queries";
import type { ReleasePromotion } from "@/services/forge/releases";

import { formatSpan } from "../Observability/deliveryScope";
import { ArtifactsTab, ChangesTab, ChecksTab, EnvPicker, HealthTab, RolloutTab } from "./ReleaseTabs";
import { Ago, Chain, ChecksMark, Facts, INK_LINK, Panel, PromoteTargetDialog } from "./releaseParts";
import {
  RELEASE_TABS,
  defaultBaseOf,
  detailEnvOf,
  leadTimeMs,
  promoterOf,
  referenceEnv,
  releaseStatus,
  releaseTimeline,
  shortVersion,
  type ReleaseEnv,
  type ReleaseTab,
} from "./releaseModel";
import { previousReleaseOf } from "../Promote/rollback";
import type { PromoteControl } from "./ReleasesList";
import { useReleaseData } from "./useReleaseData";

const TAB_LABELS: Record<ReleaseTab, string> = {
  changes: "Changes",
  rollout: "Rollout",
  checks: "Checks",
  artifacts: "Artifacts",
  health: "Health",
};

export interface ReleaseDetailSearch {
  tab?: ReleaseTab;
  /** The release compared with on Changes. */
  base?: string;
  /** The environment the environment-bound tabs read. */
  in?: string;
}

export interface ReleaseDetailProps {
  project: string;
  version: string;
  /** The top-bar scope. */
  env: string;
  envs: ReleaseEnv[];
  envsLoading: boolean;
  search: ReleaseDetailSearch;
  onSearch: (next: Partial<ReleaseDetailSearch>) => void;
  listHref: string;
  releaseHref: (version: string, search?: Record<string, unknown>) => string;
  /** The Activity page in `env`, pre-filtered to `target` ("release:<version>"). */
  activityHref: (env: string, target: string) => string;
  promote: PromoteControl;
}

export function ReleaseDetail({
  project,
  version,
  env,
  envs,
  envsLoading,
  search,
  onSearch,
  listHref,
  activityHref,
  promote,
}: ReleaseDetailProps) {
  const detail = useProjectRelease(project, version);
  const record = detail.data?.release ?? null;
  const data = useReleaseData(project, envs, record);
  const [promoting, setPromoting] = useState(false);

  const row = data.model?.rows.find((candidate) => candidate.release.version === version) ?? null;
  const reference = referenceEnv(envs);
  const promotedIn = useMemo(
    () => envs.filter((candidate) => candidate.kind === "long" && data.promotions.get(candidate.id)?.some((p) => p.releaseVersion === version)),
    [envs, data.promotions, version]
  );
  const target = useMemo(() => {
    const pinned = promotedIn.find((candidate) => candidate.name === search.in);
    if (pinned) {
      const mine = data.promotions.get(pinned.id)?.filter((p) => p.releaseVersion === version) ?? [];
      const promotion = mine.find((p) => p.targets.length === 0) ?? mine[0];
      if (promotion) return { env: pinned, promotion };
    }
    return detailEnvOf(version, env, envs, data.promotions);
  }, [promotedIn, search.in, data.promotions, version, env, envs]);

  const crumbs = [{ label: "Releases", href: listHref }, { label: shortVersion(version) }];

  if (detail.isLoading || envsLoading || (data.releases.isLoading && !data.model)) {
    return <DetailSkeleton crumbs={crumbs} version={version} />;
  }
  if (detail.error) {
    return (
      <div className="space-y-6" data-testid="release-detail" data-state="error">
        <PageHeader breadcrumbs={crumbs} title={version} className="mb-0" />
        <Card className="space-y-3">
          <p className="text-sm font-medium text-ink">This release could not be read</p>
          <p className="text-sm text-ink-muted">{(detail.error as Error).message || "The control plane did not answer."}</p>
          <button
            type="button"
            onClick={() => void detail.refetch()}
            className="inline-flex h-8 items-center rounded-md border border-border bg-surface px-3 text-sm font-medium text-ink hover:bg-ink/5"
          >
            {detail.isFetching ? "Retrying…" : "Try again"}
          </button>
        </Card>
      </div>
    );
  }
  if (!record || !row) {
    return (
      <div className="space-y-6" data-testid="release-detail" data-state="not-found">
        <PageHeader breadcrumbs={crumbs} title={version} className="mb-0" />
        <EmptyState
          icon={<TagIcon className="h-6 w-6" aria-hidden="true" />}
          title="No such release"
          description={`${project} has no release ${version}. It may have been cut in another project, or the link is mistyped.`}
        />
        <Link href={listHref} className={`text-sm ${INK_LINK}`}>
          All releases
        </Link>
      </div>
    );
  }

  const tab: ReleaseTab = search.tab ?? "changes";
  const status = releaseStatus(row);
  const bases = new Map(data.model?.pipeline.map((cell) => [cell.env.name, cell.base]) ?? []);
  const actionEnv = envs.find((candidate) => candidate.kind === "long" && candidate.name === env) ?? reference;
  const actionBase = actionEnv ? (bases.get(actionEnv.name) ?? null) : null;
  const rank = (v: string | null) => (v ? data.ordered.findIndex((r) => r.version === v) : -1);
  // Only to a release known to be older than the live one: a live release
  // outside the loaded list (rank -1) is older than nothing we can name.
  const canRollBack =
    !!actionEnv && !!actionBase && actionBase !== version && rank(actionBase) >= 0 && rank(version) > rank(actionBase);
  const blocked = promote.blockedReason;
  // THIS release is what the environment runs: offer the way back, named.
  const rollbackTo =
    actionEnv && actionBase === version ? previousReleaseOf(data.promotions.get(actionEnv.id), version) : null;

  const actions: PageHeaderAction[] = [
    ...(rollbackTo && actionEnv
      ? [
          {
            label: `Roll back ${actionEnv.name} to ${shortVersion(rollbackTo)}…`,
            onClick: () => promote.onPromote({ version: rollbackTo, env: actionEnv.name, intent: "rollback" }),
            disabled: !!blocked,
          },
        ]
      : []),
    ...(canRollBack && actionEnv
      ? [
          {
            label: `Roll back ${actionEnv.name} to this…`,
            onClick: () => promote.onPromote({ version, env: actionEnv.name, intent: "rollback" }),
            disabled: !!blocked,
          },
        ]
      : []),
    { label: "Promote…", variant: "primary" as const, onClick: () => setPromoting(true), disabled: !!blocked },
  ];

  const base = search.base ?? defaultBaseOf(version, data.ordered, target ? data.promotions.get(target.env.id) : reference ? data.promotions.get(reference.id) : undefined);
  const baseOptions = data.ordered.filter((r) => r.version !== version);
  const baseReason = search.base
    ? "chosen by you"
    : target && base && data.promotions.get(target.env.id)?.some((p) => p.releaseVersion === base)
      ? `the release ${target.env.name} ran before`
      : "the release cut before it";
  const creator = data.names.get(record.createdByUserId) || record.author;
  const checks = row.checks;
  const lead = reference ? leadTimeMs(record, data.promotions.get(reference.id)) : null;
  const checkEntries = promotedIn.flatMap((candidate) =>
    (data.promotions.get(candidate.id) ?? []).filter((p) => p.releaseVersion === version).map((promotion) => ({ env: candidate, promotion }))
  );
  const envBound = tab === "rollout" || tab === "artifacts" || tab === "health" || tab === "changes";

  return (
    <div className="space-y-6" data-testid="release-detail" data-state="ready">
      <div>
        <PageHeader
          breadcrumbs={crumbs}
          title={version}
          titleAccessory={
            <>
              <span title={status.previews.length ? `Previews: ${status.previews.join(", ")}` : undefined}>
                <Status tone={status.tone} label={status.label} />
              </span>
              {row.scoped.length > 0 && <Tag title={`A scoped deploy moved only ${row.scoped.join(", ")}`}>scoped: {row.scoped.join(", ")}</Tag>}
              {row.rolledBack.map((name) => (
                <Tag key={name} tone="danger">
                  rolled back · {name}
                </Tag>
              ))}
            </>
          }
          meta={
            <>
              <span>
                cut{creator ? ` by ${creator}` : ""}
                {record.branch ? ` from ${record.branch}` : ""}
              </span>
              {record.gitCommit && <Mono value={record.gitCommit} />}
              {checks && (
                <span className="inline-flex items-center gap-1.5">
                  checks <ChecksMark checks={checks} />
                </span>
              )}
              <Ago at={record.createdAt} />
            </>
          }
          actions={actions}
          className="mb-0"
        />
        {blocked && (
          <p className="mt-2 text-xs text-ink-muted" data-testid="release-promote-blocked">
            {blocked}
          </p>
        )}
      </div>

      <Tabs
        tabs={RELEASE_TABS.map((id) => ({ id, label: TAB_LABELS[id] }))}
        activeTab={tab}
        onChange={(id) => onSearch({ tab: id === "changes" ? undefined : (id as ReleaseTab) })}
        ariaLabel="Release"
      />

      {/* The rail is 340px; its text is text-xs (the mockup's .8125rem). */}
      <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
        <div className="min-w-0 space-y-4" role="tabpanel" aria-label={TAB_LABELS[tab]}>
          {envBound && (
            <EnvPicker envs={promotedIn} value={target?.env.name ?? null} onChange={(name) => onSearch({ in: name })} />
          )}
          {tab === "changes" && (
            <ChangesTab
              project={project}
              release={record}
              base={base}
              baseReason={baseReason}
              baseOptions={baseOptions}
              onBaseChange={(next) => onSearch({ base: next })}
              env={target?.env ?? null}
              note={noteOf(target ? [target.promotion, ...(data.promotions.get(target.env.id) ?? []).filter((p) => p.releaseVersion === version)] : [], data.names)}
            />
          )}
          {tab === "rollout" && <RolloutTab env={target?.env ?? null} promotion={target?.promotion ?? null} />}
          {tab === "checks" && <ChecksTab entries={checkEntries} />}
          {tab === "artifacts" && <ArtifactsTab release={record} env={target?.env ?? null} promotion={target?.promotion ?? null} />}
          {tab === "health" && <HealthTab project={project} env={target?.env ?? null} promotion={target?.promotion ?? null} />}
        </div>

        <aside className="space-y-4" aria-label="About this release">
          <Panel title="Promotion history" testId="release-timeline">
            <p className="mb-3 text-xs text-ink-muted">The canonical record of this release’s promotions, supersessions and rollbacks across environments.</p>
            <Chain entries={releaseTimeline(record, envs, data.promotions, data.ordered, data.names)} />
          </Panel>
          <Panel title="Details" testId="release-facts">
            <Facts
              items={[
                ["Branch", record.branch || <span className="text-ink-muted">not recorded</span>],
                ["Commit", record.gitCommit ? <Mono value={record.gitCommit} /> : <span className="text-ink-muted">not recorded</span>],
                ...(record.dirty ? ([["Tree", <span key="dirty" className="text-warning-ink">uncommitted changes</span>]] as [string, React.ReactNode][]) : []),
                ["Forge version", record.forgeVersion ? <Mono value={record.forgeVersion} /> : <span className="text-ink-muted">not recorded</span>],
                ...(target?.promotion.bundleDigest ? ([["Bundle digest", <Mono key="b" value={target.promotion.bundleDigest} />]] as [string, React.ReactNode][]) : []),
                [
                  "Lead time",
                  lead != null ? (
                    <span>
                      {formatSpan(lead)} <span className="text-ink-muted">commit to {reference?.name}</span>
                    </span>
                  ) : (
                    <span className="text-ink-muted">{record.commitTime ? `not in ${reference?.name ?? "prod"} yet` : "commit time not recorded"}</span>
                  ),
                ],
                ...(record.run?.url
                  ? ([
                      [
                        "CI run",
                        <a key="run" href={record.run.url} target="_blank" rel="noreferrer" className={INK_LINK}>
                          {record.run.provider || "run"} {record.run.id}
                        </a>,
                      ],
                    ] as [string, React.ReactNode][])
                  : []),
              ]}
            />
          </Panel>
          <Panel title="History" testId="release-history">
            <div className="divide-y divide-border/70 text-xs">
              <Link
                href={activityHref(target?.env.name ?? env, `release:${version}`)}
                className="no-color flex h-8 items-center justify-between text-ink-muted hover:text-ink"
              >
                Activity in {target?.env.name ?? (env === "all" ? "every environment" : env)}
                <span aria-hidden="true">›</span>
              </Link>
              <button type="button" onClick={() => onSearch({ tab: "health" })} className="flex h-8 w-full items-center justify-between text-left text-ink-muted hover:text-ink">
                Health after release
                <span aria-hidden="true">›</span>
              </button>
            </div>
          </Panel>
        </aside>
      </div>

      {promoting && data.model && (
        <PromoteTargetDialog
          version={version}
          envs={data.model.longEnvs}
          bases={bases}
          initialEnv={env}
          onCancel={() => setPromoting(false)}
          onConfirm={(name) => {
            setPromoting(false);
            promote.onPromote({ version, env: name, intent: "promote" });
          }}
        />
      )}
    </div>
  );
}

/** The deploy note: the reading promotion's, else the newest other promotion of the release there that has one. */
function noteOf(promotions: ReleasePromotion[], names: ReadonlyMap<string, string>): { text: string; by: string } | null {
  const withNote = promotions.find((p) => p.note.trim() !== "");
  return withNote ? { text: withNote.note, by: promoterOf(withNote, names) } : null;
}

function DetailSkeleton({ crumbs, version }: { crumbs: { label: string; href?: string }[]; version: string }) {
  return (
    <div className="space-y-6" data-testid="release-detail" data-state="loading" aria-busy="true">
      <PageHeader breadcrumbs={crumbs} title={version} meta={<span className="inline-flex h-4 w-72 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />} className="mb-0" />
      <div className="h-8 border-b border-border" />
      {/* The rail is 340px; its text is text-xs (the mockup's .8125rem). */}
      <div className="grid grid-cols-[minmax(0,1fr)] items-start gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
        <div className="space-y-4">
          {[0, 1].map((i) => (
            <Card key={i} className="space-y-3">
              <div className="h-3 w-32 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
              <div className="h-3 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
              <div className="h-3 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
            </Card>
          ))}
        </div>
        <Card className="space-y-3">
          <div className="h-3 w-24 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
          <div className="h-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
        </Card>
      </div>
    </div>
  );
}
