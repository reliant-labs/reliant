// Copyright (c) 2026 Reliant Labs
import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { obsKeys, useObsReadApi } from "@/hooks/observability-queries";
import { obsRetry } from "@/services/controlPlane/observability/errors";
import type { ObsScope } from "@/services/controlPlane/observability/types";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";
import type { ObsNavigate, ObsSearch } from "../obsSearch";
import { ObsError, ObsLoadingRows } from "../ObsStates";
import { GraphPanel } from "./GraphPanel";
import { RPCExplorer } from "./RPCExplorer";

const INPUT = "h-8 max-w-full rounded-md border border-border bg-surface-sunken px-2 text-sm text-ink";
export function GraphView({ scope, range, search, navigate, subject }: {
  scope: ObsScope; range: RangePreset; search: ObsSearch; navigate: ObsNavigate; subject: string;
}) {
  const api = useObsReadApi();
  // A window stays frozen while selecting nodes/methods. The panel remounts
  // this boundary on explicit Refresh, advancing relative time to now.
  const [window] = useState(() => resolveWindow(range));
  const explorer = search.view === "rpc";
  const rpcId = search.rpcId ?? "";
  const service = search.rpcService ?? "";
  const processService = search.service ?? "";
  const processServices = processService ? [processService] : [];
  // Runtime service.name does not prove a declared process identity. Do not
  // query/display a wider catalog when that restriction cannot be honored.
  const catalog = useQuery({ queryKey: [...obsKeys.all(scope), "graph-catalog", window, service, processService],
    queryFn: () => api.getServiceGraphCatalog({ ...scope, window, rpcService: service }),
    enabled: !processService, staleTime: Infinity, retry: obsRetry });
  const exactCatalog = useQuery({ queryKey: [...obsKeys.all(scope), "graph-catalog-exact", window, service, rpcId, processService],
    queryFn: () => api.getServiceGraphCatalog({ ...scope, window, rpcService: service, rpcIds: [rpcId] }),
    enabled: !!rpcId && !processService, staleTime: Infinity, retry: obsRetry });
  // React Query intentionally retains data after a failed refetch. Catalog
  // metadata is permissioned: never present retained data as currently verified.
  const catalogError = catalog.error || exactCatalog.error;
  const catalogData = !processService && !catalogError ? catalog.data : undefined;
  const selectedData = !processService && !catalogError ? exactCatalog.data : undefined;
  const displayedCatalog = catalogData ? { ...catalogData,
    methods: [...catalogData.methods.filter((m) => m.id !== rpcId), ...(selectedData?.methods ?? [])],
    declaredCatalogs: selectedData?.declaredCatalogs ?? catalogData.declaredCatalogs,
  } : selectedData;
  const graph = useQuery({ queryKey: [...obsKeys.all(scope), "graph", window, rpcId, service, processService, !!search.internal, !!search.declared],
    queryFn: () => api.getServiceGraph({ ...scope, window, rpcIds: rpcId ? [rpcId] : [], rpcService: service, services: processServices, includeInternal: !!search.internal, includeDeclared: !!search.declared && !processService }),
    enabled: !explorer, staleTime: Infinity, retry: obsRetry });
  const services = [...new Set(catalogData?.methods.map((m) => m.service) ?? [])].sort();
  const methods = [...new Set(catalogData?.methods.filter((m) => !service || m.service === service).map((m) => m.id) ?? [])].sort();
  const [draft, setDraft] = useState(rpcId);
  useEffect(() => setDraft(rpcId), [rpcId]);
  const onRPC = (id: string) => { setDraft(id); navigate({ rpcId: id || undefined, rpcService: id.split("/")[0] || undefined }); };
  return <div className="space-y-3" data-testid="graph-view">
    <div className="flex flex-wrap items-end gap-3">
      <label className="space-y-1 text-xs text-ink-muted"><span className="block">Full gRPC / Connect service</span><select aria-label="RPC service" className={INPUT} value={service} onChange={(e) => { setDraft(""); navigate({ rpcService: e.target.value || undefined, rpcId: undefined }); }}><option value="">All services</option>{service && !services.includes(service) && <option>{service}</option>}{services.map((s) => <option key={s}>{s}</option>)}</select></label>
      <form className="flex items-end gap-2" onSubmit={(e) => { e.preventDefault(); onRPC(draft.trim()); }}><label className="space-y-1 text-xs text-ink-muted"><span className="block">Exact full service/method</span><input aria-label="Full RPC identity" list="graph-rpc-methods" className={`${INPUT} w-80`} value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="acme.v1.OrdersService/GetOrder" maxLength={1024} /><datalist id="graph-rpc-methods">{methods.map((m) => <option key={m} value={m} />)}</datalist></label><button className={`${INPUT} hover:bg-ink/5`} type="submit">Apply</button></form>
      {!explorer && <><label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={!!search.errors} onChange={(e) => navigate({ errors: e.target.checked || undefined })} />Server faults only</label><label className="flex items-center gap-2 text-sm"><input type="checkbox" disabled={!!processService} checked={!!search.declared && !processService} onChange={(e) => navigate({ declared: e.target.checked || undefined })} />Declared overlay</label><label className="flex items-center gap-2 text-sm"><input type="checkbox" checked={!!search.internal} onChange={(e) => navigate({ internal: e.target.checked || undefined })} />RPC / method detail</label></>}
      <button className="text-sm text-primary underline" onClick={() => navigate({ view: explorer ? "graph" : "rpc" })}>{explorer ? "Service graph" : "Browse RPC catalog"}</button>
    </div>
    {processService && <p className="text-sm">Process service filter: {processService} <button className="text-primary underline" onClick={() => navigate({ service: undefined })}>Clear</button></p>}
    <p className="text-xs text-ink-muted">Bounded minute aggregates · maximum one hour. Investigation window stays frozen while drilling; Refresh advances this relative interval to now. Missing minutes are not zero traffic.</p>
    {scope.filters.length > 0 && <p role="status" className="text-sm">Attribute filters remain in scope. Unsupported aggregate dimensions are rejected by the API, never silently ignored. <button className="text-primary underline" onClick={() => navigate({ f: undefined })}>Clear unsupported attribute filters</button></p>}
    {processService && <p role="status" className="text-sm">RPC catalog and declared process metadata are unsupported with a process service filter: no verified runtime-to-declared process mapping is available. Remove the process filter explicitly to browse the wider catalog.</p>}
    {!!catalogError && <GraphReadError error={catalogError} subject={`${subject} RPC catalog`} retry={() => { void catalog.refetch(); if (rpcId) void exactCatalog.refetch(); }} />}
    {explorer ? processService || catalogError ? null : catalog.isPending || (!!rpcId && exactCatalog.isPending) ? <ObsLoadingRows /> : displayedCatalog && <RPCExplorer catalog={displayedCatalog} service={service} rpcId={rpcId} onSelect={onRPC} onGraph={(id) => navigate({ view: "graph", rpcId: id })} /> : graph.isPending ? <ObsLoadingRows /> : graph.error ? <GraphReadError error={graph.error} subject={subject} retry={() => void graph.refetch()} /> : graph.data && <GraphPanel graph={graph.data} catalog={displayedCatalog} scope={scope} filters={{ rpcId, errorsOnly: search.errors, declared: !!search.declared && !processService, internal: search.internal }} navigate={navigate} onRPC={onRPC} />}
  </div>;
}
function GraphReadError({ error, subject, retry }: { error: unknown; subject: string; retry: () => void }) {
  if (ConnectError.from(error).code === Code.Unimplemented) return <p role="status" className="rounded-lg border border-border bg-surface p-4 text-sm">This server does not support the service graph / RPC catalog API. Update the control plane; retrying cannot enable an unsupported API.</p>;
  return <ObsError error={error} subject={subject} onRetry={retry} />;
}
