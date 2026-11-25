// Copyright (c) 2026 Reliant Labs

/**
 * ML › Machines (MlEarlyAccessService.GetMlMachines): the accelerator nodes
 * of the org's connected clusters, one section per cluster and pool — the
 * product and count, allocated against total and idle, utilisation against
 * allocation over 24h, and each node with its health and the evidence
 * behind it ("Xid 79 on GPU-5").
 */

import { useMemo } from "react";

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useMlMachines } from "@/hooks/ml-queries";
import type { MlCluster, MlMachines, MlNode, MlNodePool } from "@/services/controlPlane/ml/types";

import { UPlotChart } from "../Observability/charts/UPlotChart";
import { formatAgo } from "../Observability/format";
import { flagWords, NODE_HEALTH, percent } from "./mlModel";
import { MlFrame, MlRead, Section, useMlGate } from "./MlParts";

export function MlMachinesPage({ now }: { now?: number }) {
  const { orgId } = useMlGate();
  const query = useMlMachines(orgId);
  return (
    <MlFrame view="machines">
      <MlRead query={query} what="machines">
        {(machines) => <MachinesView machines={machines} now={now ?? Date.now()} />}
      </MlRead>
    </MlFrame>
  );
}

function MachinesView({ machines, now }: { machines: MlMachines; now: number }) {
  const clusters = useMemo(() => {
    const byId = new Map<string, MlCluster>(machines.clusters.map((c) => [c.id, c]));
    // A pool whose cluster is not listed still shows, under its id.
    for (const pool of machines.pools) if (!byId.has(pool.clusterId)) byId.set(pool.clusterId, { id: pool.clusterId, name: pool.clusterId, provider: "", location: "", lastSeenAtMs: null });
    return [...byId.values()];
  }, [machines]);
  if (machines.pools.length === 0) {
    return <p className="text-ui text-ink-muted" data-testid="ml-machines-none">The connected clusters report no accelerator nodes.</p>;
  }
  return (
    <div className="space-y-6" data-testid="ml-machines">
      {machines.observedAtMs !== null && <p className="text-meta text-ink-muted">Observed {formatAgo(machines.observedAtMs, now)}</p>}
      {clusters.map((cluster) => {
        const pools = machines.pools.filter((p) => p.clusterId === cluster.id);
        if (pools.length === 0) return null;
        return (
          <section key={cluster.id} className="space-y-3" data-testid={`ml-cluster-${cluster.id}`}>
            <h2 className="text-section text-ink">
              {cluster.name}
              <span className="ml-2 text-meta font-normal text-ink-muted">{[cluster.provider.toUpperCase(), cluster.location].filter(Boolean).join(" · ")}</span>
            </h2>
            {pools.map((pool) => (
              <PoolSection key={`${pool.clusterId}/${pool.name}`} pool={pool} />
            ))}
          </section>
        );
      })}
    </div>
  );
}

type NodeRow = { node: MlNode };

function PoolSection({ pool }: { pool: MlNodePool }) {
  const chart = useMemo(
    () => ({
      timestamps: pool.utilization.map((p) => p.atMs / 1000),
      series: [
        { label: "Allocated", values: pool.utilization.map((p) => p.allocatedPercent / 100), color: "--chart-2" },
        { label: "Utilised", values: pool.utilization.map((p) => p.utilizationPercent / 100), color: "--chart-1" },
      ],
    }),
    [pool.utilization],
  );
  const columns: DataTableColumn<NodeRow>[] = [
    {
      key: "name",
      header: "Node",
      render: (_, { node }) => (
        <span className="flex min-w-0 items-center gap-2">
          <Status tone={NODE_HEALTH[node.health].tone} label={NODE_HEALTH[node.health].label} hideLabel />
          <span className="truncate font-mono text-code text-ink" title={node.name}>
            {node.name}
          </span>
          {node.cordoned && <Tag title="Marked unschedulable: it gets no new pods">cordoned</Tag>}
        </span>
      ),
    },
    {
      key: "allocated",
      header: "Allocated",
      numeric: true,
      render: (_, { node }) => (
        <span className="text-ink" title={`${node.acceleratorsAllocatable} of ${node.acceleratorCount} allocatable`}>
          {node.acceleratorsAllocated} / {node.acceleratorCount}
        </span>
      ),
    },
    { key: "util", header: "Utilisation", numeric: true, render: (_, { node }) => <span className="text-ink">{percent(node.utilizationPercent)}</span> },
    { key: "mem", header: "Memory", numeric: true, render: (_, { node }) => <span className="text-ink-muted">{percent(node.memoryUsedPercent)}</span> },
    {
      key: "health",
      header: "Health",
      className: "w-full max-w-0",
      render: (_, { node }) =>
        node.flags.length === 0 ? (
          <span className="text-ink-muted">{NODE_HEALTH[node.health].label}</span>
        ) : (
          <ul className="space-y-0.5">
            {node.flags.map((flag, index) => (
              <li key={index} className="truncate text-ink" title={flag.message || undefined}>
                {flagWords(flag)}
              </li>
            ))}
          </ul>
        ),
    },
    { key: "driver", header: "Driver", render: (_, { node }) => <span className="font-mono text-code text-ink-muted">{node.driverVersion || "—"}</span> },
    { key: "sharing", header: "Sharing", render: (_, { node }) => <span className="text-ink-muted">{node.sharing && node.sharing !== "none" ? node.sharing : "—"}</span> },
  ];
  const faulty = pool.nodes.filter((node) => node.health === "bad").length;
  return (
    <Section
      title={
        <>
          {pool.name} <span className="text-ui font-normal text-ink-muted">· {pool.acceleratorProduct || pool.acceleratorVendor}</span>
        </>
      }
      note={
        <>
          {pool.acceleratorsAllocated} of {pool.acceleratorsTotal} allocated · {pool.acceleratorsIdle} idle
          {faulty > 0 && ` · ${faulty} faulty ${faulty === 1 ? "node" : "nodes"}`}
        </>
      }
      testId={`ml-pool-${pool.clusterId}-${pool.name}`}
      flush
    >
      {chart.timestamps.length > 1 && (
        <div className="px-4 pb-3">
          <UPlotChart height={160} timestamps={chart.timestamps} series={chart.series} unit="percent" label={`Utilisation against allocation of ${pool.name} over 24 hours`} />
        </div>
      )}
      <DataTable<NodeRow>
        flush
        stacked
        ariaLabel={`Nodes of ${pool.name}`}
        columns={columns}
        data={pool.nodes.map((node) => ({ node }))}
        getRowKey={({ node }) => node.name}
        getRowProps={({ node }) => ({ className: "align-top", "data-testid": `ml-node-${node.name}`, "data-health": node.health })}
      />
    </Section>
  );
}
