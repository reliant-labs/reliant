// Where the usage is going — per deployment and environment, heaviest first.
//
// Rows that name no deployment are KEPT and labelled rather than dropped. A
// row whose deployment has since been purged still records what it cost, and
// hiding it would make this table quietly disagree with the accrued total
// above it — the kind of discrepancy that turns into a support ticket about
// our arithmetic.
//
// Names arrive as prebuilt maps rather than one lookup per row: this table can
// reference a dozen deployments, and the caller already lists them once. An id
// with no name falls back to the id itself — a row whose deployment has since
// been purged still records what it cost, and showing the raw id beats showing
// an em dash.
//
// forge-ui's DataTable, the list every table in the app is made of.

import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";

import { formatCents, formatQuantity } from "./usageModel";
import type { AttributionRow } from "./usageModel";

export interface AttributionTableProps {
  rows: AttributionRow[];
  deploymentNames?: Map<string, string>;
  environmentNames?: Map<string, string>;
}

type Row = { key: string; row: AttributionRow };

export function AttributionTable({
  rows,
  deploymentNames,
  environmentNames,
}: AttributionTableProps) {
  if (rows.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        No metered usage in this period yet. Usage appears here once a deployment
        has been running long enough to be swept — this is measured after the
        fact, not projected from what you have declared.
      </p>
    );
  }

  const columns: DataTableColumn<Row>[] = [
    {
      key: "deployment",
      header: "Deployment",
      render: (_, { row }) =>
        row.deploymentId ? (
          (deploymentNames?.get(row.deploymentId) ?? row.deploymentId)
        ) : (
          <span className="text-ink-muted">Cluster floor (no deployment)</span>
        ),
    },
    {
      key: "environment",
      header: "Environment",
      className: "text-ink-muted",
      render: (_, { row }) =>
        row.environmentId ? (environmentNames?.get(row.environmentId) ?? row.environmentId) : "—",
    },
    { key: "cpu", header: "CPU requested (vCPU-hr)", numeric: true, className: "text-ink-muted", render: (_, { row }) => formatQuantity(row.cpuDisplay) },
    { key: "memory", header: "Memory requested (GiB-hr)", numeric: true, className: "text-ink-muted", render: (_, { row }) => formatQuantity(row.memoryDisplay) },
    { key: "storage", header: "Storage (GiB-hr)", numeric: true, className: "text-ink-muted", render: (_, { row }) => formatQuantity(row.storageDisplay) },
    { key: "accrued", header: "Accrued", numeric: true, className: "font-medium", render: (_, { row }) => formatCents(row.accruedCents) },
  ];

  return (
    <div className="forge-ui flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        Accrued cost by deployment, highest first. Estimated from metered usage —
        not an invoice.
      </p>
      <DataTable<Row>
        ariaLabel="Accrued cost by deployment"
        columns={columns}
        data={rows.map((row) => ({ key: `${row.environmentId}/${row.deploymentId}`, row }))}
        getRowKey={(r) => r.key}
      />
    </div>
  );
}
