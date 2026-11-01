/**
 * The decisions behind the Add machine sheet, as plain functions so they are
 * tested without rendering it: which sizes are offered and on what terms, and
 * what a new machine is called.
 */
import { DaemonSize } from "@/gen/controlplane/controlplane/v1/shared_pb";
import type { ComputePlanOption } from "@/components/Billing/PlanTiles";
import { formatSizeLabel, type DaemonSizeName } from "@/components/Settings/cloud/billingUtils";
import { formatMachineSpec } from "@/components/Billing/machineSpecs";
import { repoNameFromUrl } from "@/lib/cloudProjectPath";
import { uniqueMachineName } from "@/lib/uniqueMachineName";

/** The wire enum for each catalog size name. */
export const DAEMON_SIZE_BY_NAME: Record<DaemonSizeName, DaemonSize> = {
  s1: DaemonSize.DAEMON_SIZE_S1,
  s2: DaemonSize.DAEMON_SIZE_S2,
  m: DaemonSize.DAEMON_SIZE_M,
  l: DaemonSize.DAEMON_SIZE_L,
  xl: DaemonSize.DAEMON_SIZE_XL,
  "2xl": DaemonSize.DAEMON_SIZE_2XL,
  "3xl": DaemonSize.DAEMON_SIZE_3XL,
};

/**
 * On what terms a size can be started now:
 *
 * - `included` — the account's plan (or redeemed coupon) already runs it.
 * - `upgrade`  — it has a plan, and this size needs a bigger one.
 * - `purchase` — no compute plan yet; this size's plan is the first purchase.
 */
export type MachineRowTerms = "included" | "upgrade" | "purchase";

export interface CloudMachineRow {
  size: DaemonSizeName;
  label: string;
  detail: string;
  terms: MachineRowTerms;
  /** The cheapest plan that runs this size, when the catalog priced one. */
  plan?: ComputePlanOption;
}

/**
 * One row per size, every size the catalog sells — sizes above the plan are
 * offered as upgrades rather than hidden. A size the account may run but the
 * catalog did not price (a coupon grant while the catalog is down) still gets
 * a row: it needs no purchase, so it needs no price.
 */
export function cloudMachineRows(
  options: ComputePlanOption[],
  allowedSizes: readonly string[],
  hasSubscription: boolean,
): CloudMachineRow[] {
  const allowed = new Set(allowedSizes.map((s) => s.toLowerCase()));
  const rows: CloudMachineRow[] = [];
  const seen = new Set<string>();
  for (const option of options) {
    if (!option.size) continue;
    seen.add(option.size);
    rows.push({
      size: option.size,
      label: option.label,
      detail: option.detail ?? formatMachineSpec(option.size) ?? "",
      terms: allowed.has(option.size) ? "included" : hasSubscription ? "upgrade" : "purchase",
      plan: option,
    });
  }
  for (const size of Object.keys(DAEMON_SIZE_BY_NAME) as DaemonSizeName[]) {
    if (allowed.has(size) && !seen.has(size)) {
      rows.push({ size, label: formatSizeLabel(size), detail: formatMachineSpec(size) ?? "", terms: "included" });
    }
  }
  const order = Object.keys(DAEMON_SIZE_BY_NAME);
  return rows.sort((a, b) => order.indexOf(a.size) - order.indexOf(b.size));
}

/** The row to start on: the caller's suggestion, else Small, else the first. */
export function defaultRow(rows: CloudMachineRow[], suggested?: DaemonSizeName): CloudMachineRow | undefined {
  return rows.find((r) => r.size === suggested) ?? rows.find((r) => r.size === "m") ?? rows[0];
}

/**
 * The name a new machine gets unless the person types one: derived from the
 * repository when there is one ("api-server"), else from the size
 * ("cloud-small"), and suffixed until it is free on the account.
 */
export function defaultMachineName(
  size: DaemonSizeName,
  taken: Iterable<string | undefined>,
  repoCloneUrl?: string,
): string {
  const fromRepo = repoCloneUrl ? repoNameFromUrl(repoCloneUrl) : "";
  return uniqueMachineName(fromRepo || `cloud-${size}`, taken);
}

/** The control plane's machine-name rule, for validating a typed name. */
export const MACHINE_NAME_RULE = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/;
