// Copyright (c) 2026 Reliant Labs
import { useMemo } from "react";
import Button from "@/components/forge-ui/button";
import type { RPCCatalog, RPCMethod } from "@/services/controlPlane/observability/graphTypes";
import { catalogMethods, parseDeclaredCatalog, schemaText, sourceLabel } from "./catalogModel";
import { GraphQuality } from "./GraphQuality";

export function RPCExplorer({ catalog, service, rpcId, onSelect, onGraph }: {
  catalog: RPCCatalog; service: string; rpcId: string; onSelect: (rpc: string) => void; onGraph: (rpc: string) => void;
}) {
  const rows = useMemo(() => catalogMethods(catalog, service), [catalog, service]);
  const selected = rows.filter((m) => m.id === rpcId);
  const artifacts = useMemo(() => catalog.declaredCatalogs.map((a) => ({ ...a, ...parseDeclaredCatalog(a.json) })), [catalog.declaredCatalogs]);
  return <div className="space-y-3" data-testid="rpc-explorer">
    <div className="rounded-lg border border-border bg-surface-sunken p-3 text-sm"><strong>Browse only.</strong> No requests can be prepared or executed here. Registered methods are capabilities, not proof of use.</div>
    <GraphQuality quality={catalog.quality} />
    {(catalog.declaredStatus !== "available" || artifacts.length === 0) && <div role="status" className="rounded-lg border border-border bg-surface p-4"><h3 className="font-medium">{catalog.declaredStatus === "partial" ? "Declared metadata partially available" : "Declared metadata unavailable"}</h3><p className="mt-1 text-sm text-ink-muted">{catalog.unavailableReason || "No verified artifact catalog supplied. Observed methods below may lack schemas, validation and process capability metadata."}</p></div>}
    {artifacts.filter((a) => a.reason).map((a) => <p role="status" key={`${a.envId}/${a.bundleDigest}`} className="text-sm">{a.reason} · environment {a.envId}</p>)}
    {!rows.length ? <div className="rounded-lg border border-border bg-surface p-8 text-center"><h3 className="font-medium">No RPC methods match</h3><p className="mt-2 text-sm text-ink-muted">An empty observed catalog does not establish that no methods are registered.</p></div> : <div className="overflow-auto rounded-lg border border-border bg-surface"><table className="w-full text-left text-sm"><thead className="border-b border-border text-xs text-ink-muted"><tr><th className="p-3">Full service / method</th><th className="p-3">Evidence</th><th className="p-3">Observations</th><th className="p-3">Streaming</th></tr></thead><tbody>{rows.map((m) => <tr key={`${m.id}/${m.evidence}`} className="border-b border-border/60 last:border-0"><td className="p-3"><button className="break-all text-left text-primary underline" onClick={() => onSelect(m.id)}>{m.id}</button></td><td className="p-3">{m.evidence}</td><td className="p-3">{m.evidence === "observed" ? m.observedCount.toString() : "Not evidence of use"}</td><td className="p-3">{m.streaming || "Unknown"}</td></tr>)}</tbody></table></div>}
    {rpcId && selected.length === 0 && <p role="status" className="text-sm">Selected RPC is not present in this scoped catalog.</p>}
    {selected.length > 0 && <section aria-label="RPC method detail" className="space-y-4 rounded-lg border border-border bg-surface p-4">
      <h3 className="break-all font-medium">{rpcId}</h3>
      {selected.map((m) => <MethodFacts key={m.evidence} method={m} />)}
      <Button variant="outline" size="sm" onClick={() => onGraph(rpcId)}>Show graph for this full RPC</Button>
      {artifacts.map((artifact) => {
        const c = artifact.catalog;
        const svc = c?.services.find((s) => s.name === selected[0].service);
        const method = svc?.methods.find((m) => m.id === rpcId);
        if (!c || !svc || !method) return null;
        const inputs = schemaText(svc.descriptor, method.input);
        const outputs = schemaText(svc.descriptor, method.output);
        const serves = c.processes.filter((p) => p.serves.some((s) => s.service === svc.name));
        const callers = c.processes.filter((p) => c.clients.some((client) => p.clients.includes(client.id) && client.service === svc.name));
        const componentIds = new Set(serves.flatMap((p) => p.serves.filter((s) => s.service === svc.name && s.component).map((s) => s.component!)));
        const processComponentIds = new Set(serves.flatMap((p) => p.components));
        return <div key={`${artifact.envId}/${artifact.bundleDigest}`} className="space-y-3 border-t border-border pt-3">
          <h4 className="text-sm font-medium">Verified declared artifact · {artifact.envId}</h4>
          <p className="break-all text-xs text-ink-muted">Bundle {artifact.bundleDigest} · repository {artifact.repository || "unavailable"} · commit {artifact.commit || "unavailable"}</p>
          <p className="text-sm">Declared serves: {serves.map((p) => p.root).join(", ") || "Unavailable"}</p>
          <p className="text-sm">Declared can-call: {callers.map((p) => p.root).join(", ") || "Unavailable"}. Not a per-method execution claim.</p>
          <p className="text-sm">Proto: {svc.proto_file} · idempotency {method.idempotency} · auth {method.auth_required ? "required" : "not required"}</p>
          <div className="grid gap-3 lg:grid-cols-2">{[["Input schema / validation", method.input, inputs], ["Output schema / validation", method.output, outputs]].map(([label, type, body]) => <div key={label} className="rounded-md border border-border/60 bg-surface-sunken p-3"><h5 className="text-sm font-medium">{label}</h5><p className="break-all text-xs text-ink-muted">{type}</p><pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-all text-xs">{body ?? "Schema unavailable in this artifact"}</pre></div>)}</div>
          <p className="text-xs text-ink-muted">Validation is descriptor metadata (including validate / validate_options), not a guarantee of client-side validation. Streaming lifetime is not unary request latency.</p>
          <details><summary className="cursor-pointer text-sm font-medium">Related message / enum descriptor metadata</summary><pre className="mt-2 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded-md border border-border/60 bg-surface-sunken p-3 text-xs">{JSON.stringify(svc.descriptor, null, 2).slice(0, 48000)}{JSON.stringify(svc.descriptor).length > 48000 ? "\n… descriptor display truncated" : ""}</pre></details>
          <details><summary className="cursor-pointer text-sm font-medium">Process contract / code metadata</summary><p className="mt-2 text-xs text-ink-muted">Composition metadata for serving processes, not an observed per-RPC call path. Locations are supplied text, not invented source links.</p>
            {c.components.filter((component) => processComponentIds.has(component.id) || processComponentIds.has(component.name)).slice(0, 100).map((component) => <div key={component.id} className="mt-3 text-xs"><p className="font-medium">{component.name} · {component.kind} · {component.type} · {componentIds.has(component.id) || componentIds.has(component.name) ? "Declared handler for this service" : "Serving-process component; RPC mapping unavailable"}</p><p>{sourceLabel(component.source)}</p>{component.methods.slice(0, 100).map((m) => <p className="break-all font-mono" key={m.id}>{m.id} · {m.signature} · {sourceLabel(m.source)}</p>)}</div>)}
            {c.bindings.filter((binding) => processComponentIds.has(binding.consumer)).slice(0, 100).map((binding, i) => <p key={i} className="mt-2 break-all text-xs">{binding.consumer}.{binding.field}: {binding.interface} · {binding.resolution} · providers {binding.providers.join(", ") || "unresolved"} · implementation candidates (not selected): {binding.candidates.join(", ") || "unavailable"}</p>)}
            {!c.components.length && !c.bindings.length && <p className="text-sm text-ink-muted">No contract/code metadata supplied.</p>}
          </details>
        </div>;
      })}
    </section>}
  </div>;
}
function MethodFacts({ method: m }: { method: RPCMethod }) {
  return <div className="space-y-1 text-sm"><p>{m.evidence} · {m.streaming || "Streaming unknown"} · idempotency {m.idempotency || "unknown"} · auth {m.authRequired === undefined ? "unknown" : m.authRequired ? "required" : "not required"}</p>
    <p>Input {m.inputType || "unavailable"} → output {m.outputType || "unavailable"}</p>
    <p>Observed process IDs: {m.processIds.join(", ") || "unavailable"}</p>
    <p>Declared serves: {m.servesProcessIds.join(", ") || "unavailable"} · declared can-call: {m.canCallProcessIds.join(", ") || "unavailable"}</p>
    <p className="text-xs text-ink-muted">{m.sourceUnavailableReason || "Exact source mapping unavailable; no source URL inferred."}</p></div>;
}
