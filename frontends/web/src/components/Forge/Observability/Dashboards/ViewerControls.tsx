// Copyright (c) 2025 Reliant Labs

/**
 * What a reader changes about a dashboard while viewing it. Every control
 * writes the URL, never the saved definition:
 *
 *   ViewerFilters   the dashboard's own filters, inline in the toolbar, each
 *                   under the console's words for its attribute
 *                   ("Environment", never `deployment.environment.name`).
 *                   An environment filter is left out while the console's
 *                   scope names one environment: it could only pick that one.
 *   ViewOptions     one "View options" popover for the rest: the breakdown,
 *                   release markers and alert bands (with what each draws),
 *                   and which sections and panels show. Its trigger counts
 *                   what differs from the saved dashboard.
 */

import { useMemo, type ReactNode } from "react";
import { SlidersHorizontal } from "lucide-react";

import Popover from "@/components/forge-ui/popover";
import { useAttributeValues } from "@/hooks/observability-queries";
import { cn } from "@/lib/utils";
import type { Dashboard, DashboardFilter, ObsScope } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { attributeLabel, ENV_KEY } from "../attributes";
import { attributeKey, breakdownFields, toggledHidden, viewerFilterValue } from "./viewerModel";
import type { DashboardSearch } from "./dashboardModel";
import { marksReleases } from "./dashboardModel";
import { BUTTON_SMALL, VariableSelect } from "./parts";

/** The menu triggers stand beside the selects: the same 32px field height. */
const TRIGGER = cn(BUTTON_SMALL, "h-8 text-sm");

/**
 * A filter's name as the reader sees it. forge labels a generated filter with
 * its attribute key; the console has words for the keys it knows.
 */
export function filterLabel(filter: Pick<DashboardFilter, "label" | "field">): string {
  const key = attributeKey(filter.field);
  const label = filter.label?.trim();
  if (!label || label === key || label === filter.field) return attributeLabel(key);
  return label;
}

/** The dashboard's filters (but the process, which the toolbar's Service picker is). */
export function ViewerFilters({ dashboard, scope, range, search, onSearch, oneEnv }: {
  dashboard: Dashboard;
  scope: ObsScope | null;
  range: RangePreset;
  search: DashboardSearch;
  onSearch: (next: Partial<DashboardSearch>) => void;
  /** The console's scope names one environment: an environment filter could only pick it. */
  oneEnv: boolean;
}) {
  const filters = dashboard.filters.filter((filter) => {
    const key = attributeKey(filter.field);
    if (key === "service.name") return false;
    return !(oneEnv && key === ENV_KEY && !viewerFilterValue(filter, search));
  });
  if (filters.length === 0) return null;
  return (
    <>
      {filters.map((filter) => (
        <ViewerFilter key={filter.id} filter={filter} scope={scope} range={range} search={search} onSearch={onSearch} />
      ))}
    </>
  );
}

/** How many view settings differ from the saved dashboard: the trigger's count. */
export function viewChanges(dashboard: Dashboard, search: DashboardSearch): number {
  return (
    (search.breakdown ? 1 : 0) +
    (marksReleases(dashboard) && search.releases === false ? 1 : 0) +
    (search.alerts === false ? 1 : 0) +
    (search.hiddenSections?.filter((id) => dashboard.sections?.some((section) => section.id === id)).length ?? 0) +
    (search.hiddenPanels?.filter((id) => dashboard.panels.some((panel) => panel.id === id)).length ?? 0)
  );
}

export function ViewOptions({ dashboard, search, onSearch }: {
  dashboard: Dashboard;
  search: DashboardSearch;
  onSearch: (next: Partial<DashboardSearch>) => void;
}) {
  const fields = breakdownFields(dashboard);
  const changed = viewChanges(dashboard, search);
  const releases = marksReleases(dashboard);
  return (
    <Popover
      align="right"
      label="View options"
      panelClassName="w-80 max-w-[calc(100vw-1rem)] p-3"
      trigger={(props) => (
        <button {...props} type="button" aria-label={changed ? `View options, ${changed} changed` : "View options"} className={TRIGGER} data-testid="dashboard-view-options">
          <SlidersHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
          <span className="hidden sm:inline">View options</span>
          {changed > 0 && (
            <span className="rounded-full bg-ink/10 px-1.5 text-xs tabular-nums" aria-hidden="true">
              {changed}
            </span>
          )}
        </button>
      )}
    >
      <div className="space-y-3 text-sm" data-testid="dashboard-viewer-controls">
        {fields.length > 0 && (
          <VariableSelect
            id="dashboard-breakdown"
            label="Breakdown"
            value={search.breakdown ?? ""}
            options={[
              { value: "", label: "As saved" },
              { value: "none", label: "None" },
              ...fields.map((field) => ({ value: field, label: attributeKey(field) === "service.name" ? "Service" : attributeLabel(attributeKey(field)) })),
            ]}
            onChange={(value) => onSearch({ breakdown: value || undefined })}
          />
        )}
        <OptionGroup title="On the charts">
          <Check
            label="Release markers"
            detail={releases ? "dashed lines where a release reached the environment" : "this dashboard declares no release markers"}
            checked={releases && search.releases !== false}
            disabled={!releases}
            onChange={(on) => onSearch({ releases: on ? undefined : false })}
          />
          <Check label="Alert bands" detail="red bands while an alert was firing" checked={search.alerts !== false} onChange={(on) => onSearch({ alerts: on ? undefined : false })} />
        </OptionGroup>
        {!!dashboard.sections?.length && (
          <OptionGroup title="Sections" testId="dashboard-sections">
            {dashboard.sections.map((section) => (
              <Check
                key={section.id}
                label={section.title}
                checked={!search.hiddenSections?.includes(section.id)}
                onChange={() => onSearch({ hiddenSections: toggledHidden(search.hiddenSections, section.id) })}
              />
            ))}
          </OptionGroup>
        )}
        <OptionGroup title="Panels" testId="dashboard-metrics" scroll>
          {dashboard.panels.map((panel) => (
            <Check
              key={panel.id}
              label={panel.title}
              detail={panel.section && search.hiddenSections?.includes(panel.section) ? "section hidden" : undefined}
              checked={!search.hiddenPanels?.includes(panel.id)}
              onChange={() => onSearch({ hiddenPanels: toggledHidden(search.hiddenPanels, panel.id) })}
            />
          ))}
        </OptionGroup>
        <p className="border-t border-border pt-2 text-xs text-ink-muted">Only your view changes, and the link carries it. The saved dashboard stays as it is.</p>
      </div>
    </Popover>
  );
}

function OptionGroup({ title, testId, scroll = false, children }: { title: string; testId?: string; scroll?: boolean; children: ReactNode }) {
  return (
    <fieldset className="space-y-0.5" data-testid={testId}>
      <legend className="pb-1 text-xs font-medium text-ink-muted">{title}</legend>
      <div className={cn(scroll && "max-h-48 overflow-y-auto pr-1")}>{children}</div>
    </fieldset>
  );
}

function Check({ label, detail, checked, disabled, onChange }: { label: string; detail?: string; checked: boolean; disabled?: boolean; onChange: (on: boolean) => void }) {
  return (
    <label className={cn("flex min-h-8 items-start gap-2 rounded-md px-1 py-1 hover:bg-ink/5", disabled && "cursor-not-allowed opacity-60")}>
      <input type="checkbox" className="mt-0.5" checked={checked} disabled={disabled} onChange={(event) => onChange(event.target.checked)} />
      <span className="min-w-0">
        <span className="block text-ink">{label}</span>
        {detail && <span className="block text-xs text-ink-muted">{detail}</span>}
      </span>
    </label>
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
  // A failed value lookup leaves "All" and says why on hover, not as a word in the toolbar.
  return <span className="inline-flex" title={values.error ? "Its values could not be read just now" : undefined} data-unavailable={values.error ? "true" : undefined}>
    <VariableSelect id={`dashboard-filter-${filter.id}`} label={filterLabel(filter)} value={value}
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
  </span>;
}
