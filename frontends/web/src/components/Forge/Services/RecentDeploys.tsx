// Copyright (c) 2026 Reliant Labs

/**
 * An environment's recent promotions that moved one service or site: the
 * release, where it came from, who promoted it and when. A workload's page
 * (ServiceDetail) and a static site's page (SiteDetail) both show it; a site
 * passes its artifact prefix (`sites/<name>/`), so a promotion that froze
 * other artifacts only does not count as one of its deploys.
 */

import Link from "@/components/forge-ui/link";
import { useEnvPromotions } from "@/hooks/release-queries";

import { useForgeConsole } from "../Console/consoleContext";
import { Ago } from "../Releases/releaseParts";
import { shortVersion } from "../Releases/releaseModel";
import { Section } from "./serviceParts";
import { promotionMoves } from "./servicesModel";

/** How many deploys the section lists. */
export const RECENT_DEPLOYS_SHOWN = 5;

export function RecentDeploys({ env, name, artifactPrefix, testId = "service-deploys" }: { env: { id: string; name: string }; name: string; artifactPrefix?: string; testId?: string }) {
  const { href } = useForgeConsole();
  const ledgers = useEnvPromotions([env.id]);
  const entry = ledgers.get(env.id);
  const moved = (entry?.data ?? []).filter((p) => promotionMoves(p, name, artifactPrefix)).slice(0, RECENT_DEPLOYS_SHOWN);
  return (
    <Section title="Recent deploys" meta={`in ${env.name}`} actions={<Link href={href({ kind: "releases" })} className="no-color text-meta text-ink-muted hover:text-ink">All releases →</Link>} testId={testId}>
      {entry?.isLoading ? (
        <div className="h-16 animate-pulse rounded-xl bg-ink/5 motion-reduce:animate-none" />
      ) : moved.length === 0 ? (
        <p className="text-ui text-ink-muted">No deploy has moved {name} in {env.name} yet.</p>
      ) : (
        <ol className="divide-y divide-border-subtle rounded-xl border border-border bg-surface">
          {moved.map((p) => (
            <li key={p.id} className="flex items-center gap-3 px-3 py-2.5 text-ui" data-testid="recent-deploy">
              <Link href={href({ kind: "release", version: p.releaseVersion })} className="no-color font-medium tabular-nums text-ink hover:underline" title={p.releaseVersion}>
                {shortVersion(p.releaseVersion)}
              </Link>
              {p.fromEnvironmentName && <span className="text-meta text-ink-muted">from {p.fromEnvironmentName}</span>}
              <span className="ml-auto text-meta text-ink-muted">
                {p.promotedByActor && <span className="text-ink">{p.promotedByActor} · </span>}
                {p.createdAt && <Ago at={p.createdAt} />}
              </span>
            </li>
          ))}
        </ol>
      )}
    </Section>
  );
}
