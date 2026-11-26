// Copyright (c) 2026 Reliant Labs

/**
 * The ML "Setup" view (02-views §10): per connected cluster, a coverage
 * CHECKLIST, not a code dump. Each row says who covers an area (adopted:
 * found and theirs; installed by forge; partial; missing; not applicable),
 * with its component, version and namespace; a gap links the finding that
 * fixes it; the KCL that declares a row stays behind "View KCL" (`?sel=
 * <cluster>:<key>`). Beside it: what discovery found (products, drivers,
 * CRDs) and the command that re-runs it.
 */

import { ChevronDown, ChevronRight } from "lucide-react";

import type { GetMlStackResponse, MlClusterSetup, MlCoverageItem } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { CodeBlock, MlFrame, MlNoCluster, MlRead } from "../MlParts";
import { componentStateOf, EmptyNote, FactList, FindingLink, fmtAgo, fmtPct, Grid, KpiStrip, MlTable, Panel, StackedBar, StatusDot, tsMs, useMl, useMlSearch, type MlColumn } from "../kit";
import { CopyCommand } from "../Recommendations/CopyCommand";
import { isGap, kclSel, orderCoverage, setupKpis } from "./setupModel";

export function MlSetupView({ sub: _sub }: { sub: readonly string[] }) {
  const q = useMl("getMlStack", {});
  return (
    <MlFrame view="setup">
      <MlRead query={q} what="setup">
        {(data) => (data.clusters.length ? <SetupBody data={data} /> : <MlNoCluster />)}
      </MlRead>
    </MlFrame>
  );
}

function SetupBody({ data }: { data: GetMlStackResponse }) {
  const k = setupKpis(data.clusters);
  const nowMs = Date.now();
  return (
    <div className="space-y-6">
      <KpiStrip
        testId="ml-setup-kpis"
        items={[
          { label: "Clusters", value: k.clusters, sub: data.clusters.map((c) => c.cluster?.provider.toUpperCase()).filter(Boolean).join(" · ") || undefined },
          { label: "Telemetry coverage", value: fmtPct(k.coverage), sub: "covered of applicable, partial counts half", hero: true },
          { label: "Missing", value: k.missing, tone: k.missing ? "danger" : undefined, sub: "each with the finding that fixes it" },
          { label: "Partial", value: k.partial, tone: k.partial ? "warning" : undefined },
          { label: "Last discovery", value: fmtAgo(k.lastDiscoveryMs, nowMs), sub: k.oldestDiscoveryMs !== k.lastDiscoveryMs ? `oldest ${fmtAgo(k.oldestDiscoveryMs, nowMs)}` : undefined },
        ]}
      />
      {data.clusters.map((c) => (
        <ClusterSetup key={c.cluster?.id} setup={c} nowMs={nowMs} />
      ))}
    </div>
  );
}

function ClusterSetup({ setup, nowMs }: { setup: MlClusterSetup; nowMs: number }) {
  const [search, setSearch] = useMlSearch();
  const c = setup.cluster!;
  const items = orderCoverage(setup.coverage);
  const open = items.find((i) => search.sel === kclSel(c.id, i.key));
  const columns: MlColumn<MlCoverageItem>[] = [
    { key: "title", header: "Area", render: (i) => <span className="text-ink">{i.title}</span> },
    {
      key: "state",
      header: "State",
      render: (i) => {
        const s = componentStateOf(i.state);
        return <StatusDot tone={s.tone} label={s.label} />;
      },
    },
    { key: "component", header: "Component", render: (i) => <span className="block max-w-64 truncate" title={i.component}>{i.component || "—"}</span> },
    { key: "version", header: "Version", render: (i) => <span className="font-mono text-code text-ink-muted">{i.version || "—"}</span> },
    { key: "ns", header: "Namespace", render: (i) => <span className="font-mono text-code text-ink-muted">{i.namespace || "—"}</span> },
    { key: "detail", header: "Detail", render: (i) => <span className="block max-w-md truncate text-ink-muted" title={i.detail}>{i.detail}</span> },
    {
      key: "kcl",
      header: "",
      render: (i) =>
        i.kcl ? (
          <button
            type="button"
            className="inline-flex items-center gap-0.5 rounded-md px-1.5 py-0.5 text-meta text-ink-muted hover:bg-ink/5 hover:text-ink"
            aria-expanded={open?.key === i.key}
            onClick={() => setSearch({ sel: open?.key === i.key ? undefined : kclSel(c.id, i.key) })}
            data-testid={`ml-setup-kcl-${i.key}`}
          >
            {open?.key === i.key ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}
            View KCL
          </button>
        ) : null,
    },
    { key: "fix", header: "", render: (i) => (i.findingId && isGap(i) ? <FindingLink id={i.findingId}>Fix</FindingLink> : null) },
  ];
  return (
    <section className="space-y-3" data-testid={`ml-setup-cluster-${c.id}`}>
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 className="text-section text-ink">{c.name}</h2>
        <span className="text-meta text-ink-muted">
          {[c.provider.toUpperCase(), c.location, c.kubernetesVersion && `Kubernetes ${c.kubernetesVersion}`, `${c.nodeCount} GPU node${c.nodeCount === 1 ? "" : "s"}`, c.gpuCount ? `${c.gpuCount} GPUs` : "no GPUs found"]
            .filter(Boolean)
            .join(" · ")}
        </span>
        <span className="text-meta text-ink">{fmtPct(setup.coverageRatio)} covered</span>
      </div>
      <Panel title="Coverage" note={`${items.filter(isGap).length} gaps · discovered ${fmtAgo(tsMs(setup.discoveredAt), nowMs)}`} flush>
        <MlTable rows={items} columns={columns} rowKey={(i) => i.key} selected={open?.key} testId={`ml-setup-coverage-${c.id}`} />
        {open && (
          <div className="border-t border-border-subtle px-4 py-3">
            <CodeBlock code={open.kcl} label={`${open.title}: the declaration in deploy/kcl (${componentStateOf(open.state).label.toLowerCase()})`} testId="ml-setup-kcl" />
          </div>
        )}
      </Panel>
      <Grid cols={2}>
        <Panel title="Inventory" note="what discovery found">
          <div className="space-y-3">
            <StackedBar segments={setup.inventory?.products.map((p) => ({ label: p.name.replace("NVIDIA ", ""), value: p.value })) ?? []} format={(v) => `${v} GPUs`} />
            <FactList
              columns={1}
              facts={[
                { label: "GPU nodes", value: setup.inventory?.nodeCount ?? 0 },
                { label: "Drivers", value: setup.inventory?.driverVersions.join(", ") || "not reported" },
              ]}
            />
          </div>
        </Panel>
        <Panel title="Custom resources" note="CRDs found, with who owns them" flush>
          <MlTable
            rows={setup.inventory?.crds ?? []}
            empty={<EmptyNote>No custom resources reported: discovery has not listed this cluster&apos;s CRDs yet.</EmptyNote>}
            rowKey={(r) => r.name}
            columns={[
              { key: "n", header: "CRD", render: (r) => <span className="font-mono text-code">{r.name}</span> },
              { key: "v", header: "Version", render: (r) => <span className="text-ink-muted">{r.version}</span> },
              { key: "o", header: "Owner", render: (r) => <span className="text-ink-muted">{r.owner}</span> },
            ]}
          />
        </Panel>
      </Grid>
      <CopyCommand command={setup.discoverCommand} label="Re-run discovery (read-only: lists what the cluster runs, changes nothing)" testId={`ml-setup-discover-${c.id}`} />
    </section>
  );
}
