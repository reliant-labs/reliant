// Copyright (c) 2025 Reliant Labs

import { useState } from "react";
import Button from "@/components/forge-ui/button";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import { useDashboard, useDashboardIndex, useDashboardMutation } from "@/hooks/dashboard-queries";
import type { Dashboard } from "@/services/controlPlane/observability/types";
import { useForgeConsole } from "../Console/consoleContext";
import { cn } from "@/lib/utils";
import { FIELD } from "../controlStyles";
import { useObsReadOnly } from "../Observability/obsReadOnly";
import { browseViewOf } from "../Observability/Dashboards/dataPanelModel";
import type { DataSearch } from "./dataUrl-model";

/** Adds a structured panel through the SAME optimistic-version SaveDashboard path as the editor. */
export function SaveBrowseView({ search, environmentId }: { search: DataSearch; environmentId: string }) {
  const { forgeProject, href } = useForgeConsole();
  const { orgId } = useObservabilityOrg();
  const index = useDashboardIndex(orgId, forgeProject, environmentId);
  const readOnly = useObsReadOnly().reason;
  const [open, setOpen] = useState(false);
  const [key, setKey] = useState("");
  const [title, setTitle] = useState("");
  const [error, setError] = useState<string | null>(null);
  const choices = index.data?.dashboards.filter((item) => item.source === "workspace" && !item.readOnly && item.ref.projectId === forgeProject) ?? [];
  const selected = choices.find((item) => item.ref.key === key);
  const doc = useDashboard(orgId, selected?.ref ?? null);
  const save = useDashboardMutation(orgId, (api, org, input: { spec: Dashboard; base: number; slug: string }) => api.saveDashboard({
    orgId: org, projectId: forgeProject, slug: input.slug, spec: input.spec, baseVersion: input.base, message: `Added Data view: ${search.table}`,
  }));
  const submit = () => {
    if (!doc.data || !selected || !doc.data.canWrite) return;
    try {
      const browse = browseViewOf(search);
      const id = `browse-${crypto.randomUUID()}`;
      const spec = { ...doc.data.spec, panels: [...doc.data.spec.panels, { id, kind: "browse", title: title.trim() || search.table!, signal: "", series: [], browse }] };
      save.mutate({ spec, base: doc.data.version, slug: selected.ref.key }, { onSuccess: () => { setOpen(false); setError(null); }, onError: (err) => setError(err instanceof Error ? err.message : "Could not save view") });
    } catch (err) { setError(err instanceof Error ? err.message : "Invalid view"); }
  };
  if (!search.db || !search.table) return null;
  return <div className="space-y-2">
    <Button size="sm" variant="outline" disabled={!index.data?.canWrite || !!readOnly} title={readOnly ?? undefined} onClick={() => setOpen(!open)}>Save as view</Button>
    {open && <section className="space-y-2 rounded-lg border border-border bg-surface-sunken p-3" aria-label="Save Data view">
      <p className="text-xs text-ink-muted">Adds this table, filters, columns and sort to a workspace dashboard. Pagination and row selection are not saved.</p>
      <label className="block text-xs text-ink">Dashboard
        <select className={cn(FIELD, "mt-1 block w-full")} value={key} onChange={(event) => setKey(event.target.value)}>
          <option value="">Choose a workspace dashboard</option>
          {choices.map((item) => <option key={item.ref.key} value={item.ref.key}>{item.title}</option>)}
        </select>
      </label>
      {!choices.length && <p className="text-xs text-ink-muted">Create a workspace dashboard first, or save a release dashboard as a copy.</p>}
      <label className="block text-xs text-ink">Panel title<input className={cn(FIELD, "mt-1 block w-full")} value={title} placeholder={search.table} onChange={(event) => setTitle(event.target.value)} /></label>
      {(!!error || !!doc.error) && <p role="alert" className="text-xs text-danger-ink">{error ?? "Could not load dashboard"}</p>}
      <div className="flex items-center gap-2">
        <Button size="sm" variant="primary" loading={save.isPending} disabled={!doc.data?.canWrite || save.isPending} onClick={submit}>Save view</Button>
        <Button size="sm" variant="ghost" onClick={() => setOpen(false)}>Cancel</Button>
      </div>
    </section>}
    {save.isSuccess && selected && <a className="text-xs text-primary-ink" href={href({ kind: "dashboard", dashKind: "workspace", key: selected.ref.key })}>View saved dashboard</a>}
  </div>;
}
