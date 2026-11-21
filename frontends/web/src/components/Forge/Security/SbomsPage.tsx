// Copyright (c) 2025 Reliant Labs

/**
 * /forge/p/:project/security/sboms — Security › SBOMs: for each release the
 * scope's environments run (or the one `?release=` names), every artifact,
 * the SBOM recorded for it — where it came from, its format and size — and
 * how far its scan got (GetReleaseVulnerabilities).
 *
 * An artifact with no SBOM is listed, as "not scanned": what is missing is
 * the point of the page. A static site's SBOM is made from its lockfile and
 * says so ("from lockfile"): it lists what the site depends on, build tools
 * included, which can be more than the built bundle ships.
 */

import { useMemo } from "react";
import { useSearch } from "@tanstack/react-router";

import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useReleaseSboms, useVulnerabilitySummary } from "@/hooks/vulnerability-queries";
import type { ArtifactSbom, ReleaseVulnerabilities, ScanState } from "@/services/controlPlane/vulnerabilities/types";

import { useForgeConsole } from "../Console/consoleContext";
import { formatAgo, formatWhen } from "../Observability/format";
import { ObsLoadingRows } from "../Observability/ObsStates";
import { SecurityFrame, useSecurityScope, type SecurityScope } from "./securityScope";
import { VulnError, VulnNotScannedYet } from "./VulnStates";
import { deployedEnvs, SCAN_STATE_LABEL, sbomSourceLabel } from "./vulnModel";

const SUBTITLE = "The software bill of materials of every artifact your environments run, and how far each scan got.";

export function SbomsPage() {
  const { href } = useForgeConsole();
  const resolved = useSecurityScope("SBOMs");
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const release = typeof raw.release === "string" && raw.release ? raw.release : undefined;
  return (
    <SecurityFrame title="SBOMs" subtitle={SUBTITLE} testId="console-sboms">
      {"blocked" in resolved ? (
        resolved.blocked
      ) : (
        <SbomsView scope={resolved.scope} release={release} releaseHref={(version) => href({ kind: "release", version })} />
      )}
    </SecurityFrame>
  );
}

export interface SbomsViewProps {
  scope: SecurityScope;
  /** One release instead of the scope's current ones. */
  release?: string;
  releaseHref: (version: string) => string;
}

export function SbomsView({ scope, release, releaseHref }: SbomsViewProps) {
  const summary = useVulnerabilitySummary(scope.project, scope.environmentId);
  const releases = useMemo(() => {
    if (release) return [{ version: release, environmentId: scope.environmentId, envNames: [] as string[] }];
    const byVersion = new Map<string, { version: string; environmentId?: string; envNames: string[] }>();
    for (const env of deployedEnvs(summary.data?.environments ?? [])) {
      const entry = byVersion.get(env.releaseVersion) ?? { version: env.releaseVersion, environmentId: env.environmentId, envNames: [] };
      entry.envNames.push(env.environmentName);
      byVersion.set(env.releaseVersion, entry);
    }
    return [...byVersion.values()];
  }, [release, scope.environmentId, summary.data]);
  const reads = useReleaseSboms(scope.project, releases);

  if (summary.isLoading && !release) {
    return (
      <Card padding="none">
        <ObsLoadingRows testId="sboms-loading" />
      </Card>
    );
  }
  if (summary.error && !release) {
    return <VulnError error={summary.error} subject="this project's SBOMs" onRetry={() => void summary.refetch()} retrying={summary.isFetching} />;
  }
  if (releases.length === 0) return <VulnNotScannedYet envs={summary.data?.environments ?? []} />;

  return (
    <div className="space-y-4">
      {releases.map((rel, index) => {
        const read = reads[index]!;
        return (
          <section key={rel.version} className="space-y-2" data-testid={`sboms-release-${rel.version}`}>
            <h2 className="flex flex-wrap items-center gap-2 text-section text-ink">
              <Link href={releaseHref(rel.version)} className="font-mono underline-offset-2 hover:underline">
                {rel.version}
              </Link>
              {rel.envNames.map((name) => (
                <Tag key={name}>{name}</Tag>
              ))}
              {read.data && <CoverageLine release={read.data} />}
            </h2>
            {read.isLoading ? (
              <Card padding="none">
                <ObsLoadingRows rows={3} testId={`sboms-release-loading-${rel.version}`} />
              </Card>
            ) : read.error || !read.data ? (
              <VulnError error={read.error} subject={`release ${rel.version}`} onRetry={() => void read.refetch()} retrying={read.isFetching} testId={`sboms-error-${rel.version}`} />
            ) : (
              <ArtifactTable artifacts={read.data.artifacts} />
            )}
          </section>
        );
      })}
    </div>
  );
}

function CoverageLine({ release }: { release: ReleaseVulnerabilities }) {
  const { artifacts, withSbom, scanned } = release.coverage;
  return (
    <span className="text-xs font-normal text-ink-muted" data-testid="sboms-coverage">
      {withSbom} of {artifacts} with an SBOM, {scanned} scanned
    </span>
  );
}

const STATE_TONE: Record<ScanState, "success" | "warning" | "danger" | "neutral"> = {
  scanned: "success",
  pending: "neutral",
  failed: "danger",
  stale: "warning",
  "no-sbom": "warning",
  none: "neutral",
};

function ArtifactTable({ artifacts }: { artifacts: ArtifactSbom[] }) {
  const now = Date.now();
  const columns: DataTableColumn<{ a: ArtifactSbom }>[] = [
    { key: "artifact", header: "Artifact", render: (_, { a }) => <span className="font-medium text-ink">{a.artifactName}</span> },
    {
      key: "sbom",
      header: "SBOM",
      className: "w-full max-w-0",
      render: (_, { a }) =>
        a.sbomDigest ? (
          <span className="block">
            <span className="flex flex-wrap items-center gap-1.5">
              {a.source && (
                <Tag
                  title={
                    a.source === "lockfile"
                      ? "Made from the lockfile: what the app depends on, build tools included, which can be more than the built bundle ships."
                      : undefined
                  }
                >
                  {sbomSourceLabel(a.source)}
                </Tag>
              )}
              <span className="text-xs text-ink-muted">
                {a.format}
                {a.generator ? ` · ${a.generator}` : ""}
              </span>
            </span>
            <span className="block truncate font-mono text-meta text-ink-muted" title={a.sbomDigest}>
              {a.sbomDigest}
            </span>
          </span>
        ) : (
          <span className="text-xs text-ink-muted">None recorded. Built by a forge that predates SBOMs, or its SBOM could not be produced.</span>
        ),
    },
    { key: "components", header: "Components", numeric: true, render: (_, { a }) => (a.sbomDigest ? a.componentCount.toLocaleString() : "—") },
    {
      key: "scan",
      header: "Scan",
      render: (_, { a }) => (
        <span className="flex flex-col gap-0.5">
          <Status tone={STATE_TONE[a.scanState]} label={SCAN_STATE_LABEL[a.scanState]} />
          {a.lastScannedMs && (
            <span className="whitespace-nowrap text-xs text-ink-muted" title={formatWhen(a.lastScannedMs, now)}>
              {formatAgo(a.lastScannedMs, now)}
            </span>
          )}
          {a.scanError && <span className="break-words font-mono text-meta text-danger-ink">{a.scanError}</span>}
        </span>
      ),
    },
  ];
  return (
    <Card padding="none" className="overflow-hidden">
      <DataTable
        flush
        stacked
        ariaLabel="Artifacts and their SBOMs"
        columns={columns}
        data={artifacts.map((a) => ({ a }))}
        getRowKey={({ a }) => a.artifactName}
        getRowProps={({ a }) => ({ "data-testid": `sbom-${a.artifactName}`, "data-state": a.scanState })}
      />
    </Card>
  );
}
