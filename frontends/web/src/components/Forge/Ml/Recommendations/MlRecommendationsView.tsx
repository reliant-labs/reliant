// Copyright (c) 2026 Reliant Labs

/**
 * The ML "Recommendations" view: an INBOX (02-views §9), not a wall of
 * cards. Header numbers (open, money and GPU-hours a day, runs at risk,
 * telemetry coverage), a filter bar whose state is in the URL (`?filter`,
 * `?group`, `?q`), then one dense table grouped under category headers with
 * counts and impact, sorted by money. Coverage findings fold into one
 * "Telemetry coverage" row so they do not drown incidents. A row opens its
 * finding in a side panel (`?sel=<finding>`).
 */

import { Fragment, useMemo, useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";

import { FilterBar, FilterMenu, FilterSearch } from "@/components/forge-ui/filter_bar";
import { MlFindingCategory, type ListMlFindingsResponse, type MlFinding } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { cn } from "@/lib/utils";

import { MlFrame, MlRead } from "../MlParts";
import { Chip, EmptyNote, findingStateOf, fmtAgo, fmtGpuHours, fmtPct, fmtUsd, KpiStrip, severityOf, StatusDot, SubjectLink, tsMs, useMl, useMlSearch, findingCategoryOf, tail } from "../kit";
import Card from "@/components/forge-ui/card";
import { FindingPanel } from "./FindingPanel";
import {
  activeFilterCount,
  CATEGORY_SLUG_OF,
  filterFindings,
  fixKindLabel,
  groupFindings,
  inboxKpis,
  needsClosed,
  parseFilter,
  parseGroup,
  serializeFilter,
  sortFindings,
  type FindingGroup,
  type RecFilters,
} from "./recsModel";

export function MlRecommendationsView({ sub: _sub }: { sub: readonly string[] }) {
  const [search, setSearch] = useMlSearch();
  const filters = parseFilter(search.filter);
  const q = useMl("listMlFindings", { includeClosed: needsClosed(filters) });
  return (
    <MlFrame view="recommendations">
      <MlRead query={q} what="recommendations">
        {(data) => <Inbox data={data} filters={filters} search={search} setSearch={setSearch} />}
      </MlRead>
    </MlFrame>
  );
}

function Inbox({ data, filters, search, setSearch }: { data: ListMlFindingsResponse; filters: RecFilters; search: ReturnType<typeof useMlSearch>[0]; setSearch: ReturnType<typeof useMlSearch>[1] }) {
  const group = parseGroup(search.group);
  const kpis = inboxKpis(data.findings, data.coverageRatio);
  const shown = useMemo(() => sortFindings(filterFindings(data.findings, filters, search.q), filters.sort), [data.findings, filters, search.q]);
  const groups = useMemo(() => groupFindings(shown, group), [shown, group]);
  const selected = data.findings.find((f) => f.id === search.sel);
  const setFilter = (patch: Partial<RecFilters>) => setSearch({ filter: serializeFilter({ ...filters, ...patch }) });
  const clusters = [...new Set(data.findings.flatMap((f) => f.clusterIds))].sort();
  const count = (pred: (f: MlFinding) => boolean) => filterFindings(data.findings, { ...filters, severity: undefined, category: undefined }, search.q).filter(pred).length;
  return (
    <div className="space-y-4">
      <KpiStrip
        testId="ml-recs-kpis"
        items={[
          { label: "Open findings", value: kpis.open, sub: `${kpis.critical} critical`, tone: kpis.critical ? "danger" : undefined },
          { label: "Impact", value: `${fmtUsd(kpis.usdPerDay)}/day`, sub: "money at stake, where it is known", hero: true },
          { label: "GPU time", value: `${fmtGpuHours(kpis.gpuHoursPerDay)}/day`, sub: "idle, lost or slowed" },
          { label: "Runs at risk", value: kpis.runsAtRisk, tone: kpis.runsAtRisk ? "warning" : undefined, sub: "named by a health or training finding" },
          { label: "Telemetry coverage", value: fmtPct(kpis.coverageRatio), sub: `${kpis.coverageGaps} gaps`, href: undefined },
        ]}
      />
      <FilterBar
        label="Filter recommendations"
        active={activeFilterCount(filters, search.q)}
        onClear={() => setSearch({ filter: serializeFilter({ state: "active", sort: filters.sort }), q: undefined })}
        testId="ml-recs-filters"
        trailing={
          <>
            <FilterMenu
              label="Group"
              value={group}
              options={[
                { value: "category", label: "Category" },
                { value: "severity", label: "Severity" },
                { value: "none", label: "None" },
              ]}
              onChange={(v) => setSearch({ group: v && v !== "category" ? v : undefined })}
              testId="ml-recs-group"
            />
            <FilterMenu
              label="Sort"
              value={filters.sort}
              options={[
                { value: "impact", label: "Impact" },
                { value: "severity", label: "Severity" },
                { value: "age", label: "Newest" },
              ]}
              onChange={(v) => setFilter({ sort: (v as RecFilters["sort"]) ?? "impact" })}
              testId="ml-recs-sort"
            />
          </>
        }
      >
        <FilterSearch value={search.q ?? ""} onChange={(v) => setSearch({ q: v || undefined })} placeholder="Search findings, subjects" testId="ml-recs-search" />
        <FilterMenu
          label="Severity"
          value={filters.severity}
          options={(["critical", "warning", "info"] as const).map((s) => ({ value: s, label: s[0].toUpperCase() + s.slice(1), count: count((f) => severityOf(f.severity).label.toLowerCase() === s) }))}
          onChange={(v) => setFilter({ severity: v })}
          testId="ml-recs-severity"
        />
        <FilterMenu
          label="Category"
          value={filters.category}
          options={Object.entries(CATEGORY_SLUG_OF).map(([cat, slug]) => ({ value: slug, label: findingCategoryOf(Number(cat) as MlFindingCategory).label, count: count((f) => f.category === Number(cat)) }))}
          onChange={(v) => setFilter({ category: v })}
          testId="ml-recs-category"
        />
        <FilterMenu label="Cluster" value={filters.cluster} options={clusters.map((c) => ({ value: c, label: c, count: count((f) => f.clusterIds.includes(c)) }))} onChange={(v) => setFilter({ cluster: v })} testId="ml-recs-cluster" />
        <FilterMenu
          label="State"
          value={filters.state === "active" ? undefined : filters.state}
          options={[
            { value: "snoozed", label: "Snoozed" },
            { value: "fixed", label: "Fixed" },
            { value: "dismissed", label: "Dismissed" },
            { value: "all", label: "All, closed included" },
          ]}
          onChange={(v) => setFilter({ state: (v as RecFilters["state"]) ?? "active" })}
          testId="ml-recs-state"
        />
      </FilterBar>
      <Card padding="none" className="overflow-hidden" data-testid="ml-recs-inbox">
        {groups.length === 0 ? (
          <div className="px-4">
            <EmptyNote>Nothing matches these filters.</EmptyNote>
          </div>
        ) : (
          <InboxTable groups={groups} selected={search.sel} coverageRatio={data.coverageRatio} onOpen={(id) => setSearch({ sel: id })} showGroups={group !== "none"} />
        )}
      </Card>
      {selected && <FindingPanel finding={selected} onClose={() => setSearch({ sel: undefined })} />}
    </div>
  );
}

function InboxTable({ groups, selected, coverageRatio, onOpen, showGroups }: { groups: FindingGroup[]; selected?: string; coverageRatio: number; onOpen: (id: string) => void; showGroups: boolean }) {
  const [coverageOpen, setCoverageOpen] = useState(false);
  const nowMs = Date.now();
  return (
    <div className="min-w-0 overflow-x-auto">
      <table className="w-full border-collapse text-ui" data-testid="ml-recs-table">
        <thead>
          <tr className="border-b border-border text-left text-meta text-ink-muted">
            <th className="h-8 w-8 pl-4 font-medium" aria-label="Severity" />
            <th className="h-8 w-full px-3 font-medium">Finding</th>
            <th className="h-8 px-3 text-right font-medium">Impact</th>
            <th className="h-8 px-3 font-medium">Subjects</th>
            <th className="h-8 px-3 font-medium">Fix</th>
            <th className="h-8 px-3 font-medium">State</th>
            <th className="h-8 px-3 text-right font-medium">Age</th>
            <th className="h-8 pr-4" aria-label="Open" />
          </tr>
        </thead>
        <tbody>
          {groups.map((g) => {
            if (g.coverage) {
              return (
                <Fragment key={g.key}>
                  <tr className="h-9 cursor-pointer border-b border-border-subtle bg-surface-sunken hover:bg-ink/5" onClick={() => setCoverageOpen((v) => !v)} data-testid="ml-recs-coverage-row">
                    <td className="pl-4">{coverageOpen ? <ChevronDown className="h-3.5 w-3.5 text-ink-muted" /> : <ChevronRight className="h-3.5 w-3.5 text-ink-muted" />}</td>
                    <td colSpan={7} className="px-3 text-ui">
                      <span className="font-medium text-ink">Telemetry coverage</span>
                      <span className="text-ink-muted">
                        {" "}
                        · {g.findings.length} gap{g.findings.length === 1 ? "" : "s"} · {fmtPct(coverageRatio)} covered
                      </span>
                    </td>
                  </tr>
                  {coverageOpen && g.findings.map((f) => <FindingRow key={f.id} f={f} selected={selected === f.id} onOpen={onOpen} nowMs={nowMs} />)}
                </Fragment>
              );
            }
            return (
              <Fragment key={g.key}>
                {showGroups && (
                  <tr className="h-8 border-b border-border-subtle bg-surface-sunken" data-testid={`ml-recs-group-${g.key}`}>
                    <td />
                    <td colSpan={1} className="px-3 text-meta font-medium text-ink">
                      {g.label} <span className="font-normal tabular-nums text-ink-muted">· {g.findings.length}</span>
                    </td>
                    <td className="px-3 text-right text-meta tabular-nums text-ink-muted">{g.usdPerDay > 0 ? `${fmtUsd(g.usdPerDay)}/day` : ""}</td>
                    <td colSpan={5} />
                  </tr>
                )}
                {g.findings.map((f) => (
                  <FindingRow key={f.id} f={f} selected={selected === f.id} onOpen={onOpen} nowMs={nowMs} />
                ))}
              </Fragment>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function FindingRow({ f, selected, onOpen, nowMs }: { f: MlFinding; selected: boolean; onOpen: (id: string) => void; nowMs: number }) {
  const sev = severityOf(f.severity);
  const state = findingStateOf(f.state);
  const first = f.subjects[0];
  return (
    <tr
      className={cn("h-11 cursor-pointer border-b border-border-subtle last:border-0 hover:bg-ink/5", selected && "bg-ink/5")}
      aria-selected={selected || undefined}
      onClick={(e) => {
        if ((e.target as HTMLElement).closest("a,button")) return;
        onOpen(f.id);
      }}
      data-testid={`ml-recs-row-${f.id}`}
    >
      <td className="pl-4 align-middle">
        <StatusDot tone={sev.tone} label={sev.label} hideLabel />
      </td>
      <td className="max-w-0 px-3 py-1 align-middle">
        <div className="truncate text-ui text-ink" title={f.title}>
          {f.title}
        </div>
        <div className="truncate text-meta text-ink-muted" title={f.summary}>
          {f.summary}
        </div>
      </td>
      <td className="whitespace-nowrap px-3 text-right align-middle tabular-nums text-ink">{f.impact?.text || "—"}</td>
      <td className="whitespace-nowrap px-3 align-middle text-meta">
        {first ? <SubjectLink subject={first} className="inline-block max-w-40 truncate align-bottom">{first.label || tail(first.id)}</SubjectLink> : "—"}
        {f.subjects.length > 1 && <span className="text-ink-muted"> +{f.subjects.length - 1}</span>}
      </td>
      <td className="whitespace-nowrap px-3 align-middle">
        <Chip>{fixKindLabel(f.fix?.kind)}</Chip>
      </td>
      <td className="whitespace-nowrap px-3 align-middle text-meta">
        <StatusDot tone={state.tone} label={`${state.label}${f.assignee && f.state === 2 ? ` · ${f.assignee}` : ""}`} />
      </td>
      <td className="whitespace-nowrap px-3 text-right align-middle text-meta tabular-nums text-ink-muted">{fmtAgo(tsMs(f.firstSeenAt), nowMs).replace(" ago", "")}</td>
      <td className="whitespace-nowrap pr-4 text-right align-middle">
        <button type="button" className="rounded-md px-2 py-1 text-meta text-primary-ink hover:bg-ink/5" onClick={() => onOpen(f.id)}>
          Open ›
        </button>
      </td>
    </tr>
  );
}
