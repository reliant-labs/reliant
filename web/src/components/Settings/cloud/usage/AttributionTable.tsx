// Where the usage is going — per deployment and environment, heaviest first.
//
// Rows that name no deployment are KEPT and labelled rather than dropped. A
// vCluster floor is real money with nothing to attribute it to, and hiding it
// would make this table quietly disagree with the accrued total above it —
// the kind of discrepancy that turns into a support ticket about our
// arithmetic.
//
// Names arrive as prebuilt maps rather than one lookup per row: this table can
// reference a dozen deployments, and the caller already lists them once. An id
// with no name falls back to the id itself — a row whose deployment has since
// been purged still records what it cost, and showing the raw id beats showing
// an em dash.
//
// Uses the shared Table primitives from ../ui rather than a bare <table>, so
// the border/spacing rhythm matches every other table in cloud settings.

import { Table, Tbody, Td, Th, Thead, Tr } from "../ui";
import { formatCents, formatQuantity } from "./usageModel";

import type { AttributionRow } from "./usageModel";

export interface AttributionTableProps {
  rows: AttributionRow[];
  deploymentNames?: Map<string, string>;
  environmentNames?: Map<string, string>;
}

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

  return (
    <div className="flex flex-col gap-3">
      <p className="text-xs text-muted-foreground">
        Accrued cost by deployment, highest first. Estimated from metered usage —
        not an invoice.
      </p>
      <Table>
        <Thead>
          <Tr>
            <Th scope="col">Deployment</Th>
            <Th scope="col">Environment</Th>
            <Th scope="col" className="text-right">
              CPU (vCPU-hr)
            </Th>
            <Th scope="col" className="text-right">
              Memory (GiB-hr)
            </Th>
            <Th scope="col" className="text-right">
              Storage (GiB-hr)
            </Th>
            <Th scope="col" className="text-right">
              Accrued
            </Th>
          </Tr>
        </Thead>
        <Tbody>
          {rows.map((row) => (
            <Tr key={`${row.environmentId}/${row.deploymentId}`}>
              <Td className="text-foreground">
                {row.deploymentId ? (
                  (deploymentNames?.get(row.deploymentId) ?? row.deploymentId)
                ) : (
                  <span className="text-muted-foreground">
                    Cluster floor (no deployment)
                  </span>
                )}
              </Td>
              <Td className="text-muted-foreground">
                {row.environmentId ? (
                  (environmentNames?.get(row.environmentId) ?? row.environmentId)
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </Td>
              <Td className="text-right tabular-nums text-muted-foreground">
                {formatQuantity(row.cpuDisplay)}
              </Td>
              <Td className="text-right tabular-nums text-muted-foreground">
                {formatQuantity(row.memoryDisplay)}
              </Td>
              <Td className="text-right tabular-nums text-muted-foreground">
                {formatQuantity(row.storageDisplay)}
              </Td>
              <Td className="text-right font-medium tabular-nums text-foreground">
                {formatCents(row.accruedCents)}
              </Td>
            </Tr>
          ))}
        </Tbody>
      </Table>
    </div>
  );
}
