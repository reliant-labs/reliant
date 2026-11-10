// Copyright (c) 2025 Reliant Labs

/**
 * The environment Overview's Promotions card: the environment's promotion
 * ledger as a timeline, newest first — every promotion, whole-release and
 * scoped, for every workload: the release, who and when, the checks, the
 * health verdict it recorded, rollbacks marked, the bundle it applied. The
 * rows are the workload History tab's (PromotionItem), unfiltered.
 *
 * Built from three reads the console already shares — this environment's
 * ListPromotions (its own id, so no other project's rows), the release list
 * (cut order, which is what judges a rollback) and the promoters' names —
 * and nothing per row: the rollout, the objects and what changed are on the
 * release page each row links to.
 */

import { useMemo, useState } from "react";

import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import Tag from "@/components/forge-ui/tag";
import { useEnvPromotions, useProjectReleases } from "@/hooks/release-queries";
import { PROMOTIONS_PAGE } from "@/services/forge/releases";

import { envPromotionRows, REST, type EnvPromotionRow, type TargetMove } from "../Releases/envPromotionsModel";
import { PromotionHealthMark, PromotionItem } from "../Releases/PromotionItem";
import { INK_BUTTON, INK_LINK, scopedLabel } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import { usePromoterNames } from "../Releases/useReleaseData";
import { Panel } from "./EnvTabPanels";

/** How many promotions the card lists before "Show all". */
export const PROMOTIONS_SHOWN = 10;

export interface EnvPromotionsProps {
  /** The forge project: whose releases and promoters these are. */
  project: string;
  env: { id: string; name: string };
  releaseHref: (version: string) => string;
  /** The project's Releases page, scoped to this environment. */
  releasesHref: string;
}

export function EnvPromotions({ project, env, releaseHref, releasesHref }: EnvPromotionsProps) {
  const ledger = useEnvPromotions(useMemo(() => [env.id], [env.id])).get(env.id);
  const releases = useProjectReleases(project);
  const names = usePromoterNames(project);
  // Keyed on the env, like the ledger read: switching environments while the
  // page stays mounted starts the new one collapsed, on its own rows.
  const [expandedFor, setExpandedFor] = useState<string | null>(null);
  const all = expandedFor === env.id;

  const rows = useMemo(() => {
    const rank = new Map((releases.data ?? []).map((release, index) => [release.version, index]));
    return envPromotionRows(ledger?.data ?? [], rank, names);
  }, [ledger?.data, releases.data, names]);
  const loaded = ledger?.data?.length ?? 0;
  const shown = all ? rows : rows.slice(0, PROMOTIONS_SHOWN);

  return (
    <Panel
      title="Promotions"
      description={`Every release put on ${env.name}, newest first: who, when, the checks and the health it recorded.`}
      actions={
        <Link href={releasesHref} className={`${INK_LINK} text-sm`} data-testid="env-promotions-releases">
          Releases
        </Link>
      }
      testId="env-promotions"
    >
      {ledger?.isLoading || (!ledger?.data && !ledger?.error) ? (
        <div aria-busy="true" data-testid="env-promotions-loading">
          <span className="sr-only">Loading {env.name}&apos;s promotions…</span>
          <SkeletonLoader variant="list-item" count={3} />
        </div>
      ) : ledger?.error && !ledger.data ? (
        <p className="text-sm text-ink-muted" role="status" data-testid="env-promotions-failed">
          {env.name}&apos;s promotion ledger could not be read right now. It retries on its own; the{" "}
          <Link href={releasesHref} className={INK_LINK}>
            Releases page
          </Link>{" "}
          reads the same ledger.
        </p>
      ) : rows.length === 0 ? (
        <p className="text-sm text-ink-muted" data-testid="env-promotions-empty">
          Nothing has been promoted to {env.name} yet. <code className="font-mono text-ink">forge env deploy {env.name}</code> records the first promotion.
        </p>
      ) : (
        <div className="space-y-3">
          {!!releases.error && !releases.data && (
            <p className="text-xs text-ink-muted" data-testid="env-promotions-unranked">
              Rollbacks are not marked: the release list, whose cut order judges them, could not be read.
            </p>
          )}
          <ol className="divide-y divide-border" aria-label={`Promotions in ${env.name}`}>
            {shown.map((row) => (
              <EnvPromotionItem key={row.id} row={row} releaseHref={releaseHref} />
            ))}
          </ol>
          {(rows.length > shown.length || loaded >= PROMOTIONS_PAGE) && (
            <p className="text-xs text-ink-muted" data-testid="env-promotions-more">
              {rows.length > shown.length && (
                <>
                  The {shown.length} newest of {rows.length} loaded.{" "}
                  <button type="button" className={INK_BUTTON} onClick={() => setExpandedFor(env.id)} data-testid="env-promotions-show-all">
                    Show all {rows.length}
                  </button>
                  {loaded >= PROMOTIONS_PAGE ? " " : null}
                </>
              )}
              {loaded >= PROMOTIONS_PAGE && (
                <span data-testid="env-promotions-truncated">
                  Only the newest {PROMOTIONS_PAGE} promotions are loaded, so {env.name}&apos;s ledger may hold older ones; each release keeps its full record on the{" "}
                  <Link href={releasesHref} className={INK_LINK}>
                    Releases page
                  </Link>
                  .
                </span>
              )}
            </p>
          )}
        </div>
      )}
    </Panel>
  );
}

/** A component's name in a sentence: a whole-release deploy's remainder is "the rest". */
function targetName(target: string): string {
  return target === REST ? "the rest" : target;
}

/** How one move reads: "rolled back from v1.42", "the same release again", "replaced v1.40". */
function moveWords(move: TargetMove): string {
  const from = move.from ? shortVersion(move.from) : null;
  switch (move.change) {
    case "rollback":
      return `rolled back from ${from}`;
    case "redeploy":
      return "the same release again";
    case "first":
      return "";
    default:
      return `replaced ${from}`;
  }
}

/**
 * What the promotion replaced. When every component moved the same way from
 * the same release, one phrase; otherwise one per way, naming its components
 * ("rolled back from v1.42: api · the same release again: web"), so a mixed
 * deploy never reads as a blanket redeploy or a whole rollback.
 */
function replacedLine(row: EnvPromotionRow): string {
  const groups = new Map<string, string[]>();
  for (const move of row.moves) {
    const words = moveWords(move);
    if (words) groups.set(words, [...(groups.get(words) ?? []), move.target]);
  }
  if (groups.size <= 1) return [...groups.keys()][0] ?? "";
  return [...groups].map(([words, targets]) => `${words}: ${targets.map(targetName).join(", ")}`).join(" · ");
}

function EnvPromotionItem({ row, releaseHref }: { row: EnvPromotionRow; releaseHref: (version: string) => string }) {
  const context = [replacedLine(row), row.from && `promoted from ${row.from}`].filter(Boolean);
  const partial = row.mixed;
  const rolledBack = row.rollbackTargets.map(targetName);
  const redeployed = row.moves.filter((move) => move.change === "redeploy").map((move) => targetName(move.target));
  return (
    <PromotionItem
      testId="env-promotion-row"
      version={row.version}
      href={releaseHref(row.version)}
      rollback={row.rollback}
      marks={
        <>
          {row.rollback && (
            <Tag tone="warning" title={`Put ${row.version} back in place of a newer release for ${rolledBack.join(", ")}`}>
              {partial ? `rollback: ${scopedLabel(rolledBack)}` : "rollback"}
            </Tag>
          )}
          {row.targets.length > 0 ? (
            <Tag title={`A scoped deploy: it moved only ${row.targets.join(", ")}`}>scoped: {scopedLabel(row.targets)}</Tag>
          ) : (
            <Tag title="A whole-release deploy: it moved every component">full</Tag>
          )}
          {row.redeploy ? (
            <Tag>redeploy</Tag>
          ) : (
            partial && redeployed.length > 0 && <Tag title={`The same release again for ${redeployed.join(", ")}`}>redeploy: {scopedLabel(redeployed)}</Tag>
          )}
        </>
      }
      by={row.by}
      at={row.at}
      checks={row.checks}
      health={<PromotionHealthMark health={row.health} />}
      digest={<DigestSummary row={row} />}
    >
      {(context.length > 0 || row.note) && (
        <p className="text-ink-muted" data-testid="env-promotion-context">
          {context.join(" · ")}
          {row.note && (
            <>
              {context.length > 0 ? " · " : ""}
              <span className="text-ink">“{row.note}”</span>
            </>
          )}
        </p>
      )}
    </PromotionItem>
  );
}

/**
 * The bundle the promotion applied, else the first artifact digest it froze,
 * with how many more (each one in the title): a digest is always on screen
 * when one was recorded.
 */
function DigestSummary({ row }: { row: EnvPromotionRow }) {
  const images = row.artifacts.length;
  const list = row.artifacts.map(([name, digest]) => `${name}: ${digest}`).join("\n");
  if (!row.bundleDigest && images === 0) return <>digests not recorded</>;
  const [first] = row.artifacts;
  return (
    <>
      {row.bundleDigest && (
        <>
          bundle <Mono value={row.bundleDigest} />
        </>
      )}
      {first && (
        <span title={list} data-testid="env-promotion-artifacts">
          {row.bundleDigest ? (
            ` · ${images} ${images === 1 ? "artifact" : "artifacts"} pinned`
          ) : (
            <>
              {first[0]} <Mono value={first[1]} />
              {images > 1 && ` +${images - 1} more`}
            </>
          )}
        </span>
      )}
    </>
  );
}
