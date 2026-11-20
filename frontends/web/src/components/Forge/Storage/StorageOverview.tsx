// Copyright (c) 2026 Reliant Labs

/**
 * Storage's lists (U6c): the managed Postgres databases, every environment's
 * buckets, and the same across several projects. The bucket browser itself
 * is StorageBrowser; these are the ways into it.
 */

import { type ReactNode } from "react";
import { Database, HardDrive } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useApps } from "@/hooks/apps-queries";
import { useCloudEnvStatus } from "@/hooks/forge-queries";
import { withSearch } from "@/lib/paths";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { consolePath } from "../Console/paths";
import { ProjectFilter, useOnlyFilter, type SeveralApp } from "../Console/severalProjects";
import { DATABASE_STATUS_NOTE } from "../LiveState/liveStateModel";
import { useAppLiveState } from "../LiveState/useAppLiveState";
import type { StorageBucket } from "@/services/controlPlane/storageBrowser/storageBrowserApi";
import { useBrowsableBuckets } from "./useStorageBrowser";

const TH = "h-9 px-3 text-left text-meta font-medium text-ink-muted whitespace-nowrap";
const TD = "h-11 px-3 align-middle text-ui whitespace-nowrap";

const RUNTIME_LABEL: Record<StorageBucket["runtime"], string> = { minio: "MinIO", s3: "S3", gcs: "Google Cloud Storage", hosted: "Reliant hosted", unknown: "—" };

export function EmptyRow({ children }: { children: ReactNode }) {
  return <p className="rounded-xl border border-dashed border-border px-4 py-5 text-ui text-ink-muted">{children}</p>;
}

export function BucketsSection({ children }: { children: ReactNode }) {
  return (
    <section className="space-y-2" data-testid="storage-buckets">
      <h2 className="flex items-center gap-2 text-section text-ink">
        <HardDrive className="size-4 text-ink-muted" aria-hidden="true" />
        Buckets
      </h2>
      {children}
    </section>
  );
}

export function BucketTable({ showProject, children }: { showProject: boolean; children: ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-xl border border-border bg-surface">
      <table className="w-full min-w-[40rem] border-collapse">
        <thead>
          <tr className="border-b border-border">
            {showProject && <th className={TH}>Project</th>}
            <th className={TH}>Bucket</th>
            <th className={TH}>Environment</th>
            <th className={TH}>Where</th>
            <th className={TH}>Access</th>
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

/** One environment's buckets, as rows of a BucketTable. */
export function EnvBucketRows({ envId, envName, href, project }: { envId: string; envName: string; href: (bucket: string) => string; project?: string }) {
  const listed = useBrowsableBuckets(envId);
  const cols = project ? 5 : 4;
  if (listed.isLoading) {
    return (
      <tr className="border-b border-border-subtle last:border-b-0">
        <td colSpan={cols} className={TD}>
          <span className="inline-block h-3 w-40 animate-pulse rounded bg-ink/5" aria-label={`Loading ${envName}'s buckets`} />
        </td>
      </tr>
    );
  }
  const buckets = listed.data?.buckets ?? [];
  if (listed.error || buckets.length === 0) {
    return (
      <tr className="border-b border-border-subtle last:border-b-0">
        {project && <td className={cn(TD, "font-medium text-ink")}>{project}</td>}
        <td className={cn(TD, "text-ink-muted")} colSpan={project ? 4 : 4}>
          {listed.error ? `${envName}'s buckets could not be read.` : `${envName} declares no bucket.`}
        </td>
      </tr>
    );
  }
  return (
    <>
      {buckets.map((bucket) => (
        <tr key={`${envId}/${bucket.name}`} className="border-b border-border-subtle last:border-b-0 hover:bg-ink/5" data-testid={`storage-bucket-${envName}-${bucket.name}`}>
          {project && <td className={cn(TD, "font-medium text-ink")}>{project}</td>}
          <td className={TD}>
            <Link href={href(bucket.name)} className="no-color font-medium text-ink hover:underline">
              {bucket.name}
            </Link>
          </td>
          <td className={TD}>
            <Tag>{envName}</Tag>
          </td>
          <td className={cn(TD, "text-ink-muted")}>{RUNTIME_LABEL[bucket.runtime]}</td>
          <td className={TD}>
            {!bucket.reachable ? (
              <span className="text-warning-ink" title={bucket.unreachableReason}>
                Unreachable
              </span>
            ) : bucket.publicRead ? (
              <span className="text-ink">Public read</span>
            ) : (
              <span className="text-ink-muted">Private</span>
            )}
          </td>
        </tr>
      ))}
    </>
  );
}

function DatabasesSection({ children }: { children: ReactNode }) {
  return (
    <section className="space-y-2" data-testid="storage-databases">
      <h2 className="flex items-center gap-2 text-section text-ink">
        <Database className="size-4 text-ink-muted" aria-hidden="true" />
        Databases
      </h2>
      {children}
    </section>
  );
}

/** The app's managed Postgres databases (GetStatus, through the live state): status, and where to look at each. */
export function ProjectDatabases() {
  const { forgeProject, env, href } = useForgeConsole();
  const live = useAppLiveState(forgeProject, env, "24h");
  const databases = live.rows.filter((row) => row.type === "database");
  if (live.envsLoading || live.hostedLoading) return null;
  return (
    <DatabasesSection>
      {databases.length === 0 ? (
        <EmptyRow>No managed Postgres in this scope. A forge.Postgres in the app&apos;s deploy config gets one per environment.</EmptyRow>
      ) : (
        <ul className="divide-y divide-border-subtle rounded-xl border border-border bg-surface">
          {databases.map((row) => (
            <li key={row.key} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 text-ui" data-testid={`storage-database-${row.envName}-${row.name}`}>
              <Link href={href({ kind: "service", name: row.name }, { env: row.envName })} className="no-color font-medium text-ink hover:underline">
                {row.name}
              </Link>
              <span className="text-meta text-ink-muted">Managed Postgres</span>
              <Tag>{row.envName}</Tag>
              <span className="text-meta text-ink-muted">{row.statusNote ?? row.status}</span>
              <span className="flex-1" />
              <Link href={href({ kind: "data" }, { env: row.envName })} className="no-color text-ink-muted hover:text-ink hover:underline">
                Browse tables
              </Link>
            </li>
          ))}
        </ul>
      )}
    </DatabasesSection>
  );
}

// ── Several projects ────────────────────────────────────────────────────────

/** Storage across the shell's projects: every project's databases and buckets in one list, a Project column, `?only=`. */
export function StorageAcrossProjects({ apps }: { apps: readonly SeveralApp[] }) {
  const { only, shown, setOnly } = useOnlyFilter(apps);
  const summaries = useApps();
  const envsOf = (app: string) => summaries.apps.find((candidate) => candidate.name === app)?.envs ?? [];
  const anyEnv = shown.some((candidate) => envsOf(candidate.app).length > 0);
  return (
    <div className="min-w-0 space-y-6" data-testid="console-storage" data-mode="several">
      <div>
        <h1 className="text-section text-ink">Storage</h1>
        <p className="mt-1 text-ui text-ink-muted">Buckets and managed databases across {only ? "one project" : `${apps.length} projects`}.</p>
      </div>
      <ProjectFilter apps={apps} only={only} onChange={setOnly} />
      <DatabasesSection>
        {summaries.loading ? (
          <div className="h-16 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" />
        ) : !anyEnv ? (
          <EmptyRow>None of these projects is deployed yet.</EmptyRow>
        ) : (
          <div className="overflow-x-auto rounded-xl border border-border bg-surface">
            <table className="w-full min-w-[40rem] border-collapse">
              <thead>
                <tr className="border-b border-border">
                  <th className={TH}>Project</th>
                  <th className={TH}>Database</th>
                  <th className={TH}>Environment</th>
                  <th className={TH}>Status</th>
                  <th className={TH} />
                </tr>
              </thead>
              <tbody>
                {shown.flatMap((candidate) => envsOf(candidate.app).map((env) => <EnvDatabaseRows key={`${candidate.app}/${env.id}`} project={candidate} envId={env.id} envName={env.name} />))}
              </tbody>
            </table>
          </div>
        )}
      </DatabasesSection>
      <BucketsSection>
        {!summaries.loading && anyEnv && (
          <BucketTable showProject>
            {shown.flatMap((candidate) =>
              envsOf(candidate.app).map((env) => (
                <EnvBucketRows
                  key={`${candidate.app}/${env.id}`}
                  envId={env.id}
                  envName={env.name}
                  project={candidate.name}
                  href={(bucket) => withSearch(consolePath(candidate.app, { kind: "storage" }), { env: env.name, bucket })}
                />
              ))
            )}
          </BucketTable>
        )}
      </BucketsSection>
    </div>
  );
}

function EnvDatabaseRows({ project, envId, envName }: { project: SeveralApp; envId: string; envName: string }) {
  const status = useCloudEnvStatus(envId);
  const databases = (status.data?.workloads ?? []).filter((w) => w.tier === "database" && w.name);
  return (
    <>
      {databases.map((db) => (
        <tr key={db.name} className="border-b border-border-subtle last:border-b-0 hover:bg-ink/5">
          <td className={cn(TD, "font-medium text-ink")}>{project.name}</td>
          <td className={TD}>
            <Link href={withSearch(consolePath(project.app, { kind: "service", name: db.name! }), { env: envName })} className="no-color font-medium text-ink hover:underline">
              {db.name}
            </Link>
          </td>
          <td className={TD}>
            <Tag>{envName}</Tag>
          </td>
          <td className={cn(TD, "text-ink-muted")}>
            {db.verdict === "degraded" ? <Status tone="danger" label="Degraded" /> : DATABASE_STATUS_NOTE}
          </td>
          <td className={cn(TD, "text-right")}>
            <Link href={withSearch(consolePath(project.app, { kind: "data" }), { env: envName })} className="no-color text-ink-muted hover:text-ink hover:underline">
              Browse tables
            </Link>
          </td>
        </tr>
      ))}
    </>
  );
}
