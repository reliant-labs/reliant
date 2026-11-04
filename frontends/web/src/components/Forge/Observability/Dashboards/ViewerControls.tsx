// Copyright (c) 2025 Reliant Labs

import { useMemo } from "react";
import { useAttributeValues } from "@/hooks/observability-queries";
import type { Dashboard, DashboardFilter, ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { attributeLabel } from "../attributes";
import { attributeKey, breakdownFields, toggledHidden, viewerFilterValue } from "./viewerModel";
import type { DashboardSearch } from "./dashboardModel";
import { marksReleases } from "./dashboardModel";
import { BUTTON_SMALL, Menu, VariableSelect } from "./parts";

/** Viewing a dashboard never writes its saved definition. */
export function ViewerControls({ dashboard, scope, range, search, onSearch }: {
  dashboard: Dashboard;
  scope: ObsScope | null;
  range: RangePreset;
  search: DashboardSearch;
  onSearch: (next: Partial<DashboardSearch>) => void;
}) {
  const fields = breakdownFields(dashboard);
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="dashboard-viewer-controls">
      <span className="text-sm text-ink-muted">View only</span>
      {dashboard.filters.filter((filter) => attributeKey(filter.field) !== "service.name").map((filter) => (
        <ViewerFilter key={filter.id} filter={filter} scope={scope} range={range} search={search} onSearch={onSearch} />
      ))}
      {fields.length > 0 && <VariableSelect id="dashboard-breakdown" label="breakdown" value={search.breakdown ?? ""}
        options={[{ value: "", label: "Saved grouping" }, { value: "none", label: "None" }, ...fields.map((field) => ({ value: field, label: attributeKey(field) === "service.name" ? "Process (service.name)" : attributeLabel(attributeKey(field)) }))]}
        onChange={(value) => onSearch({ breakdown: value || undefined })} />}
      {!!dashboard.sections?.length && <Menu label="Sections" trigger="Sections" align="left" triggerClassName={BUTTON_SMALL} testId="dashboard-sections"
        entries={dashboard.sections.map((section) => ({ label: section.title, multiple: true, checked: !search.hiddenSections?.includes(section.id),
          onSelect: () => onSearch({ hiddenSections: toggledHidden(search.hiddenSections, section.id) }),
        }))} />}

      <label className="flex min-h-11 items-center gap-2 text-sm">
        <input type="checkbox" checked={marksReleases(dashboard) && search.releases !== false} disabled={!marksReleases(dashboard)}
          onChange={(event) => onSearch({ releases: event.target.checked ? undefined : false })} />
        Release markers
      </label>
      <label className="flex min-h-11 items-center gap-2 text-sm">
        <input type="checkbox" checked={search.alerts !== false}
          onChange={(event) => onSearch({ alerts: event.target.checked ? undefined : false })} />
        Alert bands
      </label>
      <Menu label="Metrics" trigger="Metrics" align="left" triggerClassName={BUTTON_SMALL} testId="dashboard-metrics"
        entries={dashboard.panels.map((panel) => ({
          label: panel.title,
          multiple: true,
          checked: !search.hiddenPanels?.includes(panel.id),
          detail: panel.section && search.hiddenSections?.includes(panel.section) ? "Section hidden" : dashboard.sections?.find((section) => section.id === panel.section)?.title,
          onSelect: () => onSearch({ hiddenPanels: toggledHidden(search.hiddenPanels, panel.id) }),
        }))} />
      <span className="text-sm text-ink-muted">Shared in the URL. Saved dashboard unchanged.</span>
    </div>
  );
}

function ViewerFilter({ filter, scope, range, search, onSearch }: {
  filter: DashboardFilter; scope: ObsScope | null; range: RangePreset; search: DashboardSearch;
  onSearch: (next: Partial<DashboardSearch>) => void;
}) {
  const key = attributeKey(filter.field);
  const value = viewerFilterValue(filter, search) ?? "";
  const lookupScope = useMemo(() => {
    if (!scope) return null;
    const filters = [...scope.filters];
    if (search.service && (key === "rpc.service" || key === "rpc.method" || key === "procedure")) filters.push({ key: "service.name", op: "eq" as const, value: search.service });
    if (key === "rpc.method" && search.rpcService) filters.push({ key: "rpc.service", op: "eq" as const, value: search.rpcService });
    return { ...scope, filters };
  }, [scope, search.service, search.rpcService, key]);
  const signal = filter.signal === "metrics" || filter.signal === "logs" ? filter.signal : "traces";
  const values = useAttributeValues(lookupScope, range, key, "", signal);
  const names = [...new Set([...(value ? [value] : []), ...(values.data?.values.map((entry) => entry.value) ?? [])])];
  return <div className="flex flex-wrap items-center gap-1">
    <VariableSelect id={`dashboard-filter-${filter.id}`} label={filter.label} value={value}
      options={[{ value: "", label: "All" }, ...names.map((name) => ({ value: name, label: name }))]}
      onChange={(next) => {
        if (key === "rpc.service") onSearch({ rpcService: next || undefined, rpcMethod: undefined });
        else if (key === "rpc.method") onSearch({ rpcMethod: next || undefined });
        else if (key === "procedure") onSearch({ rpcProcedure: next || undefined });
        else {
          const variables = { ...search.variables };
          if (next) variables[filter.id] = next; else delete variables[filter.id];
          onSearch({ variables: Object.keys(variables).length ? variables : undefined });
        }
      }} />
    {!!values.error && <span className="text-sm text-ink-muted" role="status">Values unavailable</span>}
  </div>;
}
