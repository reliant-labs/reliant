// Copyright (c) 2025 Reliant Labs

import type { DbCell, DbColumn, DbTable } from "@/services/controlPlane/observability/appDatabaseApi";
import { cellCut } from "./dataCells-model";
import { encodeFilter, type DataSearch } from "./dataUrl-model";

// Bounds shared by URLs and returned subject cells. Never shorten a key to fit.
export function validIdentityValue(value: unknown, max = 200): value is string {
  return typeof value === "string" && value.length > 0 && value.length <= max && value.trim() === value && ![...value].some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127);
}
export function validIdentityEmail(value: unknown): value is string {
  return validIdentityValue(value) && /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value);
}
export type IdentityTarget = { user: string } | { q: string };
export function identityTarget(column: Pick<DbColumn, "identitySubject"> | undefined, value: DbCell, truncated: boolean): IdentityTarget | null {
  if (cellCut(value, truncated).cut) return null;
  if (column?.identitySubject === "id" && validIdentityValue(value)) return { user: value };
  if (column?.identitySubject === "email" && validIdentityEmail(value)) return { q: value };
  return null;
}
export interface AppDataSubject { environmentId: string; userId: string; email: string }
export function parseAppDataSubject(raw: unknown, environmentId: string): AppDataSubject | null {
  if (typeof raw !== "string" || raw.length > 1600) return null;
  try {
    const s = JSON.parse(raw) as AppDataSubject;
    return s && s.environmentId === environmentId && validIdentityValue(s.userId) && (s.email === "" || validIdentityEmail(s.email)) ? s : null;
  } catch { return null; }
}
// Direct columns only. One already-authorized description, no graph, scans or counts.
export function appDataTargets(tables: readonly DbTable[], subject: AppDataSubject): { label: string; search: DataSearch }[] {
  const out: { label: string; search: DataSearch }[] = [];
  for (const table of tables.slice(0, 200)) {
    for (const col of table.columns.slice(0, 200)) {
      const value = col.identitySubject === "id" ? subject.userId : col.identitySubject === "email" ? subject.email : null;
      if (!value || !identityTarget(col, value, false)) continue;
      out.push({ label: `${table.schema}.${table.name} · ${col.name}`, search: { table: `${table.schema}.${table.name}`, f: [encodeFilter({ column: col.name, op: "eq", values: [value] })] } });
      if (out.length === 25) return out;
    }
  }
  return out;
}
export function uniqueExactEmail<T extends { email: string }>(rows: readonly T[], email: string, complete: boolean): T | null {
  if (!complete || !validIdentityEmail(email)) return null;
  const exact = rows.filter((r) => r.email.trim().toLowerCase() === email.toLowerCase());
  return exact.length === 1 ? exact[0]! : null;
}
