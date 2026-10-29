// Copyright (c) 2026 Reliant Labs
import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import Badge from "@/components/forge-ui/badge";
import Combobox, { type ComboboxOption } from "@/components/forge-ui/combobox";
import ToggleSwitch from "@/components/forge-ui/toggle_switch";
import { obsKeys, useObsReadApi } from "@/hooks/observability-queries";
import { obsRetry } from "@/services/controlPlane/observability/errors";
import type { RPCCatalog } from "@/services/controlPlane/observability/graphTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";
import { useObsAskContext } from "../AskReliant/askReliantContext";
import type { ObsNavigate, ObsSearch } from "../obsSearch";
import { ObsError, ObsLoadingRows } from "../ObsStates";
import { GraphPanel } from "./GraphPanel";
import { GraphQualityInfo, GraphQualityNotice } from "./GraphQuality";
import { fmtCount, shortService } from "./graphStats";
import { catalogNotes, methodRows, RPCExplorer } from "./RPCExplorer";

/** Service and method choices: every service/method seen in traffic or declared in a catalog. */
function filterOptions(catalog: RPCCatalog | undefined, service: string, rpcId: string) {
  const rows = catalog ? methodRows(catalog) : [];
  const byService = new Map<string, { methods: number; requests: bigint }>();
  for (const r of rows) {
    const s = byService.get(r.service) ?? { methods: 0, requests: 0n };
    byService.set(r.service, { methods: s.methods + 1, requests: s.requests + r.requests });
  }
  if (service && !byService.has(service)) byService.set(service, { methods: 0, requests: 0n });
  const services: ComboboxOption[] = [...byService].sort(([a], [b]) => shortService(a).localeCompare(shortService(b)) || a.localeCompare(b))
    .map(([name, s]) => ({ value: name, label: shortService(name), keywords: [name],
      description: `${name} · ${s.methods} method${s.methods === 1 ? "" : "s"}${s.requests > 0n ? ` · ${fmtCount(s.requests)} req` : ""}` }));
  const methods: ComboboxOption[] = rows.filter((r) => !service || r.service === service)
    .sort((a, b) => shortService(a.service).localeCompare(shortService(b.service)) || a.method.localeCompare(b.method))
    .map((r) => ({ value: r.id, label: r.method, keywords: [r.id], group: service ? undefined : shortService(r.service),
      description: r.requests > 0n ? `${fmtCount(r.requests)} req` : "no traffic" }));
  if (rpcId && !methods.some((m) => m.value === rpcId)) methods.unshift({ value: rpcId, label: rpcId.split("/")[1] ?? rpcId, keywords: [rpcId] });
  return { services, methods };
}

export function GraphView({ scope, range, search, navigate, subject }: {
  scope: ObsScope; range: RangePreset; search: ObsSearch; navigate: ObsNavigate; subject: string;
}) {
  const api = useObsReadApi();
  const { environments } = useObsAskContext();
  const envNames = useMemo(() => new Map(environments.map((e) => [e.id, e.name])), [environments]);
  // A window stays frozen while selecting nodes/methods. The panel remounts
  // this boundary on explicit Refresh, advancing relative time to now.
  const [window] = useState(() => resolveWindow(range));
  const [showUnresolved, setShowUnresolved] = useState(false);
  const explorer = search.view === "rpc";
  const rpcId = search.rpcId ?? "";
  const service = search.rpcService ?? "";
  const processService = search.service ?? "";
  // Runtime service.name does not prove a declared process identity. Do not
  // query/display a wider catalog when that restriction cannot be honored.
  const catalogKey = [...obsKeys.all(scope), "graph-catalog", window] as const;
  const everything = useQuery({ queryKey: [...catalogKey, ""],
    queryFn: () => api.getServiceGraphCatalog({ ...scope, window, rpcService: "" }),
    enabled: !processService, staleTime: Infinity, retry: obsRetry });
  const scoped = useQuery({ queryKey: [...catalogKey, service],
    queryFn: () => api.getServiceGraphCatalog({ ...scope, window, rpcService: service }),
    enabled: !processService && !!service, staleTime: Infinity, retry: obsRetry });
  const catalog = service ? scoped : everything;
  const exactCatalog = useQuery({ queryKey: [...catalogKey, "exact", service, rpcId],
    queryFn: () => api.getServiceGraphCatalog({ ...scope, window, rpcService: service, rpcIds: [rpcId] }),
    enabled: !!rpcId && !processService, staleTime: Infinity, retry: obsRetry });
  // React Query intentionally retains data after a failed refetch. Catalog
  // metadata is permissioned: never present retained data as currently verified.
  const catalogError = everything.error || catalog.error || exactCatalog.error;
  const catalogData = !processService && !catalogError ? catalog.data : undefined;
  const optionsData = !processService && !catalogError ? everything.data : undefined;
  const selectedData = !processService && !catalogError ? exactCatalog.data : undefined;
  const displayedCatalog = catalogData ? { ...catalogData,
    methods: [...catalogData.methods.filter((m) => m.id !== rpcId), ...(selectedData?.methods ?? [])],
    declaredCatalogs: selectedData?.declaredCatalogs ?? catalogData.declaredCatalogs,
  } : selectedData;
  const graph = useQuery({ queryKey: [...obsKeys.all(scope), "graph", window, rpcId, service, processService, !!search.internal, !!search.declared],
    queryFn: () => api.getServiceGraph({ ...scope, window, rpcIds: rpcId ? [rpcId] : [], rpcService: service, services: processService ? [processService] : [], includeInternal: !!search.internal, includeDeclared: !!search.declared && !processService }),
    enabled: !explorer, staleTime: Infinity, retry: obsRetry });
  const { services, methods } = useMemo(() => filterOptions(optionsData, service, rpcId), [optionsData, service, rpcId]);
  const notes = useMemo(() => catalogNotes(displayedCatalog, envNames), [displayedCatalog, envNames]);
  const filters = useMemo(() => ({ rpcId, errorsOnly: search.errors, declared: !!search.declared && !processService, internal: search.internal, showUnresolved }),
    [rpcId, search.errors, search.declared, search.internal, processService, showUnresolved]);
  const onRPC = (id: string) => navigate({ rpcId: id || undefined, rpcService: id ? id.split("/")[0] : service || undefined });
  const onTraces = (id: string) => navigate({ view: "traces", q: id, rpcId: undefined, rpcService: undefined });
  const quality = explorer ? displayedCatalog?.quality ?? null : graph.data?.quality ?? null;
  const optionsLoading = !processService && everything.isPending;

  return <div className="space-y-3" data-testid="graph-view">
    <div className="flex flex-wrap items-center gap-2">
      <Combobox aria-label="Service" size="sm" className="w-56" options={services} value={service || null} clearable
        placeholder="All services" searchPlaceholder="Search services" loading={optionsLoading} disabled={!!processService}
        emptyMessage="No services with traffic in this window" panelClassName="w-96"
        onChange={(v) => navigate({ rpcService: v || undefined, rpcId: undefined })} />
      <Combobox aria-label="Method" size="sm" className="w-64" options={methods} value={rpcId || null} clearable
        placeholder="All methods" searchPlaceholder="Search methods" loading={optionsLoading} disabled={!!processService}
        emptyMessage="No methods with traffic in this window" panelClassName="w-96"
        onChange={(v) => onRPC(v ?? "")} />
      {processService && <Badge label={`Process: ${processService}`} removable onRemove={() => navigate({ service: undefined })} />}
      {!explorer && <div className="flex flex-wrap items-center gap-4 pl-2">
        <ToggleSwitch size="sm" label="Errors only" checked={!!search.errors} onChange={(on) => navigate({ errors: on || undefined })} />
        <ToggleSwitch size="sm" label="Unresolved peers" checked={showUnresolved} onChange={setShowUnresolved} />
        <ToggleSwitch size="sm" label="Internal calls" checked={!!search.internal} onChange={(on) => navigate({ internal: on || undefined })} />
        <ToggleSwitch size="sm" label="Declared" disabled={!!processService} checked={!!search.declared && !processService} onChange={(on) => navigate({ declared: on || undefined })} />
      </div>}
      <div className="ml-auto"><GraphQualityInfo quality={quality} window={explorer ? undefined : graph.data?.window} notes={notes} /></div>
    </div>
    <GraphQualityNotice quality={quality} />
    {scope.filters.length > 0 && <p role="status" className="text-xs text-ink-muted">Attribute filters don't apply to this view. <button className="text-primary hover:underline" onClick={() => navigate({ f: undefined })}>Clear filters</button></p>}
    {explorer && processService && <p role="status" className="text-sm text-ink-muted">Methods can't be narrowed to one process. <button className="text-primary hover:underline" onClick={() => navigate({ service: undefined })}>Clear the process filter</button> to browse them.</p>}
    {explorer && !!catalogError && <GraphReadError error={catalogError} subject={`${subject} RPC methods`} retry={() => { void everything.refetch(); if (service) void scoped.refetch(); if (rpcId) void exactCatalog.refetch(); }} />}
    {explorer
      ? processService || catalogError ? null
        : catalog.isPending || (!!rpcId && exactCatalog.isPending) ? <ObsLoadingRows />
          : displayedCatalog && <RPCExplorer catalog={displayedCatalog} service={service} rpcId={rpcId} envNames={envNames} onSelect={onRPC} onTraces={onTraces}
            onGraph={(id) => navigate({ view: "graph", rpcId: id, rpcService: id.split("/")[0] })} />
      : graph.isPending ? <ObsLoadingRows />
        : graph.error ? <GraphReadError error={graph.error} subject={subject} retry={() => void graph.refetch()} />
          : graph.data && <GraphPanel graph={graph.data} scope={scope} filters={filters} navigate={navigate} onRPC={onRPC} envNames={envNames}
            onShowUnresolved={() => setShowUnresolved(true)} />}
  </div>;
}
function GraphReadError({ error, subject, retry }: { error: unknown; subject: string; retry: () => void }) {
  if (ConnectError.from(error).code === Code.Unimplemented) return <p role="status" className="rounded-lg border border-border bg-surface p-4 text-sm">This server does not support the service graph yet. Update the control plane to enable it.</p>;
  return <ObsError error={error} subject={subject} onRetry={retry} />;
}
