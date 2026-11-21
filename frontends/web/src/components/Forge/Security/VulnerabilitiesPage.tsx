// Copyright (c) 2025 Reliant Labs

/**
 * /forge/p/:project/security/vulnerabilities — Security › Vulnerabilities:
 * the known vulnerabilities in what the scope's environments run, from the
 * SBOMs of their releases (control-plane VulnerabilityService).
 *
 * The page answers, in order: can these findings be trusted (the summary's
 * scan state per environment, said in words when it is anything but
 * "scanned"), then what is affected (one row per advisory and package, most
 * severe first, as the server orders them). UNKNOWN IS NEVER CLEAN: before
 * any scan the page is "Not scanned yet", and "No known vulnerabilities" is
 * said only when everything in scope was scanned against a current database.
 *
 * The environment is the top bar's scope. The page's own filters are in the
 * URL (vulnModel.ts VulnSearch) and map one to one onto ListVulnerabilities'
 * optional fields. A row opens the advisory's own page.
 */

import { useCallback, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { FilterBar, FilterChip, FilterMenu } from "@/components/forge-ui/filter_bar";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useReleaseSboms, useVulnerabilities, useVulnerabilitySummary } from "@/hooks/vulnerability-queries";
import type { EnvVulnSummary, Finding, MinSeverity, SbomSource, VulnSummary } from "@/services/controlPlane/vulnerabilities/types";

import { useForgeConsole } from "../Console/consoleContext";
import { formatAgo, formatWhen } from "../Observability/format";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";
import { SecurityFrame, useSecurityScope, type SecurityScope } from "./securityScope";
import { ScanStateNote, VulnError, VulnNoneKnown, VulnNotScannedYet } from "./VulnStates";
import {
  activeFilterCount,
  deployedEnvs,
  envVouchedClean,
  filtersOf,
  fixLabel,
  formatEpss,
  pickVulnSearch,
  releaseTrust,
  scopeTrust,
  sbomSourceLabel,
  SEVERITY_LABEL,
  SEVERITY_TONE,
  totalFindings,
  untrustedEnvs,
  type ReleaseTrust,
  type VulnSearch,
} from "./vulnModel";

const SUBTITLE = "Known vulnerabilities in what each environment runs, from the SBOMs of its releases.";

export function VulnerabilitiesPage() {
  const { href } = useForgeConsole();
  const resolved = useSecurityScope("vulnerability findings");
  const raw = useSearch({ strict: false }) as Record<string, unknown>;
  const search = useMemo(() => pickVulnSearch(raw), [raw]);
  const navigate = useNavigate();
  const onSearch = useCallback(
    (next: VulnSearch) =>
      void navigate({
        to: ".",
        // Only the scope survives from the old search: every filter is the new view's.
        search: (prev: Record<string, unknown>) => ({ env: prev.env as string | undefined, ...next }),
        replace: true,
      }),
    [navigate]
  );
  return (
    <SecurityFrame title="Vulnerabilities" subtitle={SUBTITLE} testId="console-vulnerabilities">
      {"blocked" in resolved ? (
        resolved.blocked
      ) : (
        <VulnerabilitiesView
          scope={resolved.scope}
          search={search}
          onSearch={onSearch}
          advisoryHref={(id) => href({ kind: "vulnerability", advisory: id })}
          sbomsHref={(release) => href({ kind: "sboms" }, release ? { release } : {})}
        />
      )}
    </SecurityFrame>
  );
}

export interface VulnerabilitiesViewProps {
  scope: SecurityScope;
  search: VulnSearch;
  onSearch: (next: VulnSearch) => void;
  advisoryHref: (advisoryId: string) => string;
  sbomsHref: (release?: string) => string;
}

/** The page below its header, free of the console: what tests and the preview render. */
export function VulnerabilitiesView({ scope, search, onSearch, advisoryHref, sbomsHref }: VulnerabilitiesViewProps) {
  const summary = useVulnerabilitySummary(scope.project, scope.environmentId);

  if (summary.isLoading) {
    return (
      <Card padding="none">
        <ObsLoadingRows testId="vuln-loading" />
      </Card>
    );
  }
  if (summary.error || !summary.data) {
    return <VulnError error={summary.error} subject="this project's vulnerabilities" onRetry={() => void summary.refetch()} retrying={summary.isFetching} />;
  }
  return <Findings scope={scope} summary={summary.data} search={search} onSearch={onSearch} advisoryHref={advisoryHref} sbomsHref={sbomsHref} />;
}

function Findings({ scope, summary, search, onSearch, advisoryHref, sbomsHref }: VulnerabilitiesViewProps & { summary: VulnSummary }) {
  const envs = summary.environments;
  const deployed = deployedEnvs(envs);
  const trust = scopeTrust(envs, summary.database?.stale ?? false);
  const filtered = activeFilterCount(search) > 0;
  const envName = useMemo(() => new Map(envs.map((env) => [env.environmentId, env.environmentName])), [envs]);

  // The releases in scope: the scope's current ones, or the one the URL names. Their artifacts say where
  // each SBOM came from (a lockfile is labelled) and why a failing scan fails.
  const releases = useMemo(() => {
    if (search.release) return [{ version: search.release, environmentId: scope.environmentId }];
    const seen = new Map<string, { version: string; environmentId?: string }>();
    for (const env of deployed) if (!seen.has(env.releaseVersion)) seen.set(env.releaseVersion, { version: env.releaseVersion, environmentId: env.environmentId });
    return [...seen.values()];
  }, [deployed, search.release, scope.environmentId]);
  const releaseReads = useReleaseSboms(scope.project, releases);
  const sourceOf = useMemo(() => {
    const map = new Map<string, SbomSource>();
    for (const read of releaseReads) for (const artifact of read.data?.artifacts ?? []) if (artifact.source) map.set(artifact.artifactName, artifact.source);
    return map;
  }, [releaseReads]);
  const readOf = (version: string) => releaseReads[releases.findIndex((r) => r.version === version)];
  const artifactsOf = (version: string) => readOf(version)?.data?.artifacts;

  const nothingScanned = trust === "not-scanned-yet" && !filtered;
  const list = useVulnerabilities(scope.project, nothingScanned ? null : filtersOf(scope.environmentId, search));
  const rows = useMemo(() => list.data?.pages.flatMap((page) => page.findings) ?? [], [list.data]);

  const untrusted = untrustedEnvs(envs);
  const noteOf = (env: EnvVulnSummary) => (
    <ScanStateNote key={env.environmentId} env={env} database={summary.database} artifacts={artifactsOf(env.releaseVersion)} sbomsHref={sbomsHref(env.releaseVersion)} />
  );

  if (nothingScanned) {
    return (
      <div className="space-y-3">
        <VulnNotScannedYet envs={envs} />
        {untrusted.filter((env) => env.scanState === "failed").map(noteOf)}
      </div>
    );
  }

  // An empty answer is clean only when what it covers is vouched for: the scope's summary, or — for a
  // release named in the URL — a successful read of that release with every one of its artifacts
  // scanned against a current database. Anything else is loading, an error or unknown; never clean.
  const namedRelease = search.release ? readOf(search.release) : undefined;
  const emptyVerdict: ReleaseTrust = search.release ? releaseTrust(namedRelease) : trust === "trusted" ? "trusted" : "unknown";

  return (
    <div className="space-y-3">
      <ScopeCounts envs={deployed} databaseStale={summary.database?.stale ?? false} />
      {untrusted.map(noteOf)}
      <VulnFilters search={search} onSearch={onSearch} releases={[...new Set(deployed.map((env) => env.releaseVersion))]} />
      {/* No answer for this view yet, or only the previous view's (a placeholder): an empty one says nothing. */}
      {list.isPending || (list.isPlaceholderData && rows.length === 0) ? (
        <Card padding="none">
          <ObsLoadingRows testId="vuln-list-loading" />
        </Card>
      ) : list.error && !list.data ? (
        <VulnError error={list.error} subject={search.release ? `release ${search.release}` : "the findings"} onRetry={() => void list.refetch()} retrying={list.isFetching} />
      ) : rows.length === 0 ? (
        filtered && !(search.release && activeFilterCount(search) === 1) ? (
          <StateFrame
            testId="vuln-no-match"
            icon={null}
            title="No findings match these filters"
            actions={
              <Button variant="outline" size="sm" onClick={() => onSearch({})} data-testid="vuln-no-match-clear">
                Clear filters
              </Button>
            }
          >
            <p>Other findings may exist in scope. Exempt findings are hidden unless you show them.</p>
          </StateFrame>
        ) : emptyVerdict === "trusted" ? (
          <VulnNoneKnown
            database={list.data?.pages[0]?.database ?? summary.database}
            envs={deployed}
            subject={search.release ? `release ${search.release}` : undefined}
          />
        ) : emptyVerdict === "loading" ? (
          <Card padding="none">
            <ObsLoadingRows testId="vuln-release-loading" />
          </Card>
        ) : emptyVerdict === "error" ? (
          <VulnError
            error={namedRelease?.error ?? null}
            subject={`release ${search.release}`}
            onRetry={() => void namedRelease?.refetch()}
            retrying={namedRelease?.isFetching}
            testId="vuln-release-error"
          />
        ) : (
          <StateFrame testId="vuln-none-in-scanned" icon={null} title="Nothing found in what was scanned">
            <p>
              {search.release ? `Not every artifact of release ${search.release} has an SBOM scanned against a current database` : "Some of what runs in scope is not fully scanned"}, so this
              is not a clean bill: its vulnerabilities are unknown.
            </p>
          </StateFrame>
        )
      ) : (
        <FindingsTable
          rows={rows}
          envName={envName}
          sourceOf={sourceOf}
          advisoryHref={advisoryHref}
          hasMore={!!list.hasNextPage}
          loadingMore={list.isFetchingNextPage}
          onLoadMore={() => void list.fetchNextPage()}
        />
      )}
    </div>
  );
}

/**
 * Per environment in scope: its release and what its findings add up to. Zero
 * findings reads as "no known vulnerabilities" only for an environment whose
 * release is fully scanned against a current database.
 */
function ScopeCounts({ envs, databaseStale }: { envs: EnvVulnSummary[]; databaseStale: boolean }) {
  const now = Date.now();
  if (envs.length === 0) return null;
  return (
    <Card padding="none" data-testid="vuln-scope-counts">
      <ul className="divide-y divide-border-subtle">
        {envs.map((env) => {
          const parts = (["critical", "high", "medium", "low"] as const).filter((sev) => env.counts[sev] > 0).map((sev) => `${env.counts[sev]} ${SEVERITY_LABEL[sev].toLowerCase()}`);
          const total = totalFindings(env.counts);
          return (
            <li key={env.environmentId} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-4 py-2 text-sm" data-testid={`vuln-env-${env.environmentName}`}>
              <span className="font-medium text-ink">{env.environmentName}</span>
              <span className="font-mono text-xs text-ink-muted">{env.releaseVersion}</span>
              <span className="text-ink">
                {env.coverage.scanned === 0
                  ? "no findings yet"
                  : total > 0
                    ? parts.join(" · ") || `${total} findings`
                    : envVouchedClean(env, databaseStale)
                      ? "no known vulnerabilities"
                      : "nothing found in what was scanned"}
              </span>
              {env.kev > 0 && <Tag tone="danger">{env.kev} known exploited</Tag>}
              {env.fixable > 0 && <span className="text-xs text-ink-muted">{env.fixable} fixable</span>}
              {env.blocking > 0 && <span className="text-xs text-ink-muted">{env.blocking} blocking</span>}
              {env.exempt > 0 && <span className="text-xs text-ink-muted">{env.exempt} exempt</span>}
              <span className="ml-auto text-xs text-ink-muted" title={env.lastScannedMs ? formatWhen(env.lastScannedMs, now) : undefined}>
                {env.lastScannedMs ? `scanned ${formatAgo(env.lastScannedMs, now)}` : "never scanned"}
              </span>
            </li>
          );
        })}
      </ul>
    </Card>
  );
}

const SEVERITY_OPTIONS: { value: MinSeverity; label: string }[] = [
  { value: "critical", label: "Critical" },
  { value: "high", label: "High and above" },
  { value: "medium", label: "Medium and above" },
  { value: "low", label: "Low and above" },
];

function VulnFilters({ search, onSearch, releases }: { search: VulnSearch; onSearch: (next: VulnSearch) => void; releases: string[] }) {
  const set = (patch: Partial<VulnSearch>) => onSearch({ ...search, ...patch });
  const releaseOptions = [...new Set([...releases, ...(search.release ? [search.release] : [])])].map((version) => ({ value: version, label: version }));
  return (
    <FilterBar label="Filter vulnerabilities" active={activeFilterCount(search)} onClear={() => onSearch({})} testId="vuln-filters">
      <FilterMenu label="Release" value={search.release} options={releaseOptions} onChange={(release) => set({ release })} testId="vuln-filter-release" />
      <FilterMenu
        label="Severity"
        value={search.severity}
        options={SEVERITY_OPTIONS}
        onChange={(severity) => set({ severity: severity as MinSeverity | undefined })}
        testId="vuln-filter-severity"
      />
      <FilterMenu
        label="Fix"
        value={search.fixable}
        options={[
          { value: "yes", label: "Has a fix" },
          { value: "no", label: "No fix" },
        ]}
        onChange={(fixable) => set({ fixable: fixable as VulnSearch["fixable"] })}
        testId="vuln-filter-fixable"
      />
      <FilterChip on={!!search.kev} onClick={() => set({ kev: search.kev ? undefined : "1" })} title="On CISA's Known Exploited Vulnerabilities list" testId="vuln-filter-kev">
        Known exploited
      </FilterChip>
      <FilterChip on={!!search.exempt} onClick={() => set({ exempt: search.exempt ? undefined : "1" })} title="Findings an environment's KCL exempts" testId="vuln-filter-exempt">
        Show exempt
      </FilterChip>
    </FilterBar>
  );
}

type Row = { finding: Finding };

function rowKey(f: Finding): string {
  return `${f.advisory.id}|${f.package.purl || `${f.package.name}@${f.package.version}`}`;
}

function FindingsTable({
  rows,
  envName,
  sourceOf,
  advisoryHref,
  hasMore,
  loadingMore,
  onLoadMore,
}: {
  rows: Finding[];
  envName: Map<string, string>;
  sourceOf: Map<string, SbomSource>;
  advisoryHref: (id: string) => string;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
}) {
  const now = Date.now();
  const columns: DataTableColumn<Row>[] = [
    {
      key: "severity",
      header: "Severity",
      render: (_, { finding }) => (
        <span className="flex flex-col gap-0.5">
          <Status tone={SEVERITY_TONE[finding.advisory.severity]} label={SEVERITY_LABEL[finding.advisory.severity]} />
          {finding.advisory.cvss !== null && <span className="text-xs tabular-nums text-ink-muted">CVSS {finding.advisory.cvss.toFixed(1)}</span>}
        </span>
      ),
    },
    {
      key: "advisory",
      header: "Advisory",
      className: "w-full max-w-0",
      render: (_, { finding }) => (
        <>
          <span className="block truncate font-mono font-medium" data-testid={`vuln-open-${finding.advisory.id}`}>
            {finding.advisory.id}
          </span>
          {finding.advisory.aliases.length > 0 && <span className="block truncate font-mono text-xs text-ink-muted">{finding.advisory.aliases.join(", ")}</span>}
          {finding.advisory.summary && <span className="block truncate text-xs text-ink-muted">{finding.advisory.summary}</span>}
        </>
      ),
    },
    {
      key: "package",
      header: "Package",
      render: (_, { finding }) => (
        <span className="block min-w-[12rem]">
          <span className="block font-mono text-xs text-ink">
            {finding.package.name}@{finding.package.version}
          </span>
          <span className={finding.fixState === "fixed" ? "block text-xs text-ink" : "block text-xs text-ink-muted"}>→ {fixLabel(finding.fixState, finding.fixedVersions)}</span>
          {finding.artifactNames.length > 0 && (
            <span className="mt-0.5 flex flex-wrap items-center gap-1 text-xs text-ink-muted">
              {finding.artifactNames.map((name) => (
                <span key={name} className="inline-flex items-center gap-1">
                  {name}
                  {sourceOf.get(name) === "lockfile" && (
                    <Tag title="This artifact's SBOM was made from its lockfile, which can list more than the built bundle ships.">{sbomSourceLabel("lockfile")}</Tag>
                  )}
                </span>
              ))}
            </span>
          )}
        </span>
      ),
    },
    {
      key: "signals",
      header: "Exploit",
      render: (_, { finding }) => (
        <span className="flex flex-col items-start gap-1">
          {finding.advisory.kev && (
            <Tag tone="danger" title="On CISA's Known Exploited Vulnerabilities list">
              KEV
            </Tag>
          )}
          {finding.advisory.epss !== null && (
            <span className="text-xs tabular-nums text-ink-muted" title="EPSS: the probability of exploitation in the next 30 days">
              EPSS {formatEpss(finding.advisory.epss)}
            </span>
          )}
          {!finding.advisory.kev && finding.advisory.epss === null && <span className="text-xs text-ink-muted">—</span>}
        </span>
      ),
    },
    {
      key: "envs",
      header: "Environments",
      render: (_, { finding }) => (
        <span className="flex flex-wrap gap-1">
          {finding.environmentIds.length === 0 ? (
            <span className="text-xs text-ink-muted">none</span>
          ) : (
            finding.environmentIds.map((id) => <Tag key={id}>{envName.get(id) ?? id}</Tag>)
          )}
        </span>
      ),
    },
    {
      key: "firstSeen",
      header: "First seen",
      render: (_, { finding }) => (
        <span className="whitespace-nowrap text-xs text-ink-muted" title={finding.firstSeenMs ? formatWhen(finding.firstSeenMs, now) : undefined}>
          {finding.firstSeenMs ? formatAgo(finding.firstSeenMs, now) : "—"}
        </span>
      ),
    },
    {
      key: "policy",
      header: "Policy",
      render: (_, { finding }) =>
        finding.exemption ? (
          <span className="flex flex-col items-start gap-0.5" data-testid={`vuln-exempt-${finding.advisory.id}`}>
            <Tag title={finding.exemption.reason}>exempt</Tag>
            {finding.exemption.expiresMs && <span className="whitespace-nowrap text-xs text-ink-muted">until {formatWhen(finding.exemption.expiresMs, now)}</span>}
          </span>
        ) : finding.blocking ? (
          <span title="The environment's vuln_gate policy blocks a promotion with this finding (when the gate enforces)">
            <Status tone="danger" label="Blocking" />
          </span>
        ) : (
          <span className="text-xs text-ink-muted">—</span>
        ),
    },
  ];
  return (
    <Card padding="none" className="overflow-hidden" data-testid="vuln-list">
      <DataTable<Row>
        flush
        stacked
        ariaLabel="Vulnerabilities in scope"
        columns={columns}
        data={rows.map((finding) => ({ finding }))}
        getRowKey={({ finding }) => rowKey(finding)}
        getRowHref={({ finding }) => advisoryHref(finding.advisory.id)}
        getRowLabel={({ finding }) => `${finding.advisory.id} in ${finding.package.name}`}
        getRowProps={({ finding }) => ({ className: "align-top", "data-testid": `vuln-row-${finding.advisory.id}`, "data-severity": finding.advisory.severity })}
      />
      {hasMore && (
        <div className="border-t border-border/60 px-4 py-2">
          <Button variant="ghost" size="sm" onClick={onLoadMore} loading={loadingMore} data-testid="vuln-more">
            Load more findings
          </Button>
        </div>
      )}
    </Card>
  );
}
