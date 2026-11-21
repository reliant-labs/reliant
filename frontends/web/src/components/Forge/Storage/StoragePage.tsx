// Copyright (c) 2025 Reliant Labs

/**
 * /forge/p/:project/storage — the storage browser, as a console page.
 *
 * A page beside Observability (and Data), not an environment tab, for the
 * reason the data browser gives: what an environment HAS is a project page
 * scoped by the top bar's environment control, the console's one environment
 * selector. Switching prod → staging keeps the bucket and folder open when
 * the other environment declares the same bucket.
 *
 * The browser itself (StorageBrowser) is free of the console; this page only
 * resolves the scope to one recorded environment and binds the URL.
 *
 * U6c (owner: "not seeing storage buckets"): Storage is a top-level
 * destination. With every environment in scope it lists each environment's
 * buckets instead of asking for one; with several projects in the shell, one
 * list across them with a Project column and an in-page project filter
 * (`?only=`).
 *
 * U6e (owner: "data and storage got mixed up"): Storage is object storage —
 * buckets — only. The managed Postgres databases are Data's.
 */

import { useCallback, useMemo, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { HardDrive } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import { DeployWithAgentButton } from "@/components/Forge/Observability/AskReliant/AskReliant";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";

import { useForgeConsole } from "../Console/consoleContext";
import { ENV_ALL } from "../Console/envScope";
import { CloudNotice } from "../SourceNotices";
import { StorageBrowser } from "./StorageBrowser";
import { StorageNote } from "./StorageStates";
import { pickStorageSearch, type StorageSearch } from "./storageUrl-model";
import { BucketsSection, BucketTable, EmptyRow, EnvBucketRows, StorageAcrossProjects } from "./StorageOverview";
import { useSeveralApps } from "../Console/severalProjects";

/** Storage's own line (U6e): buckets only. The nav's shared subtitle still names the databases until it moves too. */
const STORAGE_SUBTITLE = "Your app's buckets: uploads, assets and every file it keeps";

function Frame({ children }: { children: ReactNode }) {
  return (
    <div className="min-w-0 space-y-6" data-testid="console-storage">
      <PageHeader density="compact" title="Storage" subtitle={STORAGE_SUBTITLE} className="mb-0" />
      {children}
    </div>
  );
}

export function StoragePage() {
  const several = useSeveralApps();
  if (several) return <StorageAcrossProjects apps={several.apps} />;
  return <ProjectStoragePage />;
}

function ProjectStoragePage() {
  const { env, roster, href } = useForgeConsole();
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const search = useMemo(() => pickStorageSearch(raw), [raw]);
  const navigate = useNavigate();

  const onNavigate = useCallback(
    (next: StorageSearch, options: { replace?: boolean } = {}) =>
      void navigate({
        to: ".",
        // Only the scope survives from the old search: every storage key is the new view's.
        search: (prev: Record<string, unknown>) => ({ env: prev.env as string | undefined, ...next }),
        replace: options.replace ?? false,
      }),
    [navigate]
  );
  const storageHref = useCallback((next: StorageSearch) => href({ kind: "storage" }, { ...next }), [href]);

  if (roster.isLoading || roster.resolvingName) {
    return (
      <Frame>
        <Card padding="none" aria-label="Loading" aria-busy="true">
          <SkeletonLoader variant="table-row" count={4} />
        </Card>
      </Frame>
    );
  }

  const recorded = roster.envs.filter((candidate) => !!candidate.live);
  const availability = roster.live.data?.availability;

  if (env === ENV_ALL) {
    return (
      <Frame>
        <CloudNotice availability={availability} detail={roster.live.data?.detail} />
        <BucketsSection>
          {recorded.length === 0 ? (
            <>
              <EmptyRow>No environment is deployed yet, so there are no buckets to list.</EmptyRow>
              <div data-testid="storage-empty-cta">
                <DeployWithAgentButton env={null} />
              </div>
            </>
          ) : (
            <BucketTable showProject={false}>
              {recorded.map((candidate) => (
                <EnvBucketRows key={candidate.name} envId={candidate.live!.id} envName={candidate.name} href={(bucket) => href({ kind: "storage" }, { env: candidate.name, bucket })} />
              ))}
            </BucketTable>
          )}
          <p className="text-meta text-ink-muted">Each environment has buckets of its own: open one to browse its files, or pick an environment in the top bar.</p>
        </BucketsSection>
      </Frame>
    );
  }

  const scoped = roster.envs.find((candidate) => candidate.name === env) ?? null;
  if (!scoped?.live) {
    const envLink = (
      <Link href={href({ kind: "environment", env })} className="font-medium text-ink underline-offset-2 hover:underline">
        {env}
      </Link>
    );
    return (
      <Frame>
        <CloudNotice availability={availability} detail={roster.live.data?.detail} />
        <StorageNote testId="storage-env-unrecorded" icon={<HardDrive className="h-4 w-4" />} title={`Reliant has no record of ${env}`}>
          <p>
            {!scoped ? (
              <>There is no environment named {env} in this project.</>
            ) : scoped.lifecycle === "local" ? (
              <>{envLink} runs on your machine (forge env up), and its buckets are there too, in the MinIO forge runs beside it.</>
            ) : (
              <>The browser reads an environment&apos;s buckets through its record here. Deploy {envLink} through Reliant to register it.</>
            )}
          </p>
        </StorageNote>
      </Frame>
    );
  }

  return (
    <Frame>
      <h2 className="-mb-2 text-section text-ink">Buckets</h2>
      <StorageBrowser key={scoped.live.id} env={{ id: scoped.live.id, name: scoped.name }} search={search} onNavigate={onNavigate} href={storageHref} />
    </Frame>
  );
}
