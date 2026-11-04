// Copyright (c) 2026 Reliant Labs
import { z } from "zod";
import type { RPCCatalog, RPCMethod } from "@/services/controlPlane/observability/graphTypes";

const text = z.string().max(2048);
const source = z.object({ path: text, line: z.number().int().nonnegative() });
const codeMethod = z.object({ id: text, name: text, signature: z.string().max(12000), scope: text, legacy_name: text, source });
/** Exact public forge.dev/service-catalog/v1 vocabulary. Objects strip unknown
 * fields: endpoint values/unknown extensions never become UI metadata. */
const catalogSchema = z.object({
  schema: z.literal("forge.dev/service-catalog/v1"), module: text,
  processes: z.array(z.object({ id: text, root: text, command: z.array(text).max(30), components: z.array(text).max(1000),
    serves: z.array(z.object({ service: text, route_set: text, audience: text, resolution: text, component: text.optional() })).max(500),
    clients: z.array(text).max(1000), source })).max(300),
  services: z.array(z.object({ name: text, proto_file: text,
    methods: z.array(z.object({ id: text, name: text, input: text, output: text, client_streaming: z.boolean(), server_streaming: z.boolean(),
      idempotency: text, auth_required: z.boolean() })).max(1000),
    descriptor: z.record(z.string(), z.unknown()),
  })).max(500),
  components: z.array(z.object({ id: text, name: text, kind: text, type: text, source, methods: z.array(codeMethod).max(1000) })).max(1000),
  bindings: z.array(z.object({ consumer: text, field: text, interface: text, methods: z.array(codeMethod).max(1000),
    candidates: z.array(text).max(1000), providers: z.array(text).max(1000), resolution: text })).max(2000),
  clients: z.array(z.object({ id: text, provider: text, service: text, config_field: text, constructor: text, source, resolution: text })).max(1000),
  diagnostics: z.array(z.object({ code: text, component: text.optional(), source })).max(1000),
});
export type DeclaredCatalog = z.infer<typeof catalogSchema>;
export const MAX_CATALOG_BYTES = 8 * 1024 * 1024;
export function parseDeclaredCatalog(json: string): { catalog: DeclaredCatalog | null; reason: string } {
  if (json.length > MAX_CATALOG_BYTES || new TextEncoder().encode(json).length > MAX_CATALOG_BYTES) return { catalog: null, reason: "Catalog exceeds the browser metadata limit" };
  try {
    // Go's public catalog encodes nil slices as null, distinct from malformed
    // non-array input. Normalize only catalog slice fields, never descriptors.
    const value = JSON.parse(json);
    const array = (obj: unknown, keys: string[]) => {
      if (!obj || typeof obj !== "object" || Array.isArray(obj)) return;
      for (const key of keys) if ((obj as Record<string, unknown>)[key] === null) (obj as Record<string, unknown>)[key] = [];
    };
    array(value, ["processes", "services", "components", "bindings", "clients", "diagnostics"]);
    if (value && typeof value === "object") {
      if (Array.isArray(value.processes)) for (const p of value.processes) array(p, ["command", "components", "serves", "clients"]);
      if (Array.isArray(value.services)) for (const s of value.services) array(s, ["methods"]);
      if (Array.isArray(value.components)) for (const c of value.components) array(c, ["methods"]);
      if (Array.isArray(value.bindings)) for (const b of value.bindings) array(b, ["methods", "candidates", "providers"]);
    }
    const result = catalogSchema.safeParse(value);
    return result.success ? { catalog: result.data, reason: "" } : { catalog: null, reason: "Catalog schema is unsupported or exceeds metadata limits" };
  } catch { return { catalog: null, reason: "Catalog JSON is invalid" }; }
}

export function catalogMethods(catalog: RPCCatalog, service = "", method = ""): RPCMethod[] {
  // Keep declared and observed facts distinct, even for one full RPC identity.
  const rows = new Map<string, RPCMethod>();
  for (const row of catalog.methods.slice(0, 2000)) {
    if ((service && row.service !== service) || (method && row.id !== method)) continue;
    rows.set(`${row.id}\u0000${row.evidence}`, row);
  }
  return [...rows.values()].sort((a, b) => a.id.localeCompare(b.id) || a.evidence.localeCompare(b.evidence));
}

/** No executable href is derived from metadata. Relative source paths are text. */
export function sourceLabel(ref: { path: string; line: number }): string {
  if (!ref.path || ref.path.startsWith("/") || ref.path.includes("..") || /^[a-z]+:/i.test(ref.path)) return "Source location unavailable";
  return `${ref.path}:${ref.line}`;
}

export function schemaText(descriptor: Record<string, unknown>, typeName: string): string | null {
  const schemas = descriptor.Schemas;
  if (!schemas || typeof schemas !== "object" || Array.isArray(schemas)) return null;
  const schema = Object.hasOwn(schemas, typeName) ? (schemas as Record<string, unknown>)[typeName] : undefined;
  if (schema === undefined) return null;
  const value = JSON.stringify(schema, null, 2);
  return value.length <= 48000 ? value : `${value.slice(0, 48000)}\n… schema display truncated`;
}
