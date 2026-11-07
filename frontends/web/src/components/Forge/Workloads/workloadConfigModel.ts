// Copyright (c) 2025 Reliant Labs

import type { JsonObject, JsonValue } from "@bufbuild/protobuf";

export interface ConfigRow {
  path: string;
  value: string;
  withheld: boolean;
}
export interface ConfigDiffRow {
  path: string;
  before: string;
  after: string;
  change: "added" | "removed" | "changed" | "secret value not compared";
}

// Server's fixed marker (forge/pkg/release.RedactedWithheld). Treat ALL old
// marker variants as withheld too: never expose or compare a secret digest.
const REDACTED_PREFIX = "forge.dev/redacted:";
const escapePath = (key: string) => key.replaceAll("~", "~0").replaceAll("/", "~1");
const isObject = (value: JsonValue | undefined): value is JsonObject => value !== null && typeof value === "object" && !Array.isArray(value);

function objectKey(object: JsonObject): string {
  const metadata = isObject(object.metadata) ? object.metadata : {};
  return [object.apiVersion, object.kind, metadata.namespace ?? "", metadata.name].map((value) => String(value ?? "")).join("/");
}

/** A deterministic field tree. Names align K8s objects and named array entries across ordering changes. */
export function configRows(objects: JsonObject[]): ConfigRow[] {
  const rows: ConfigRow[] = [];
  const visit = (value: JsonValue, path: string) => {
    if (Array.isArray(value) && value.length) {
      const names = value.map((item) => isObject(item) && typeof item.name === "string" ? item.name : null);
      const named = names.every((name) => name !== null) && new Set(names).size === names.length;
      value.forEach((item, index) => visit(item, `${path}/${escapePath(named ? `name=${names[index]}` : String(index))}`));
    } else if (isObject(value) && Object.keys(value).length) {
      Object.keys(value).sort().forEach((key) => visit(value[key]!, `${path}/${escapePath(key)}`));
    } else {
      const withheld = typeof value === "string" && value.includes(REDACTED_PREFIX);
      rows.push({ path, value: withheld ? "withheld" : JSON.stringify(value), withheld });
    }
  };
  const seen = new Map<string, number>();
  objects.forEach((object) => {
    const key = objectKey(object);
    const occurrence = seen.get(key) ?? 0;
    seen.set(key, occurrence + 1);
    visit(object, `/${escapePath(key)}${occurrence ? `#${occurrence}` : ""}`);
  });
  return rows.sort((a, b) => a.path.localeCompare(b.path));
}

/** Unknown secrets are never "unchanged", even when both fixed markers match. */
export function diffConfig(before: JsonObject[], after: JsonObject[]): ConfigDiffRow[] {
  const left = new Map(configRows(before).map((row) => [row.path, row]));
  const right = new Map(configRows(after).map((row) => [row.path, row]));
  const withheldPaths = new Set([...left.values(), ...right.values()].filter((row) => row.withheld).map((row) => row.path));
  const ancestors = (path: string): string[] => {
    const parts = path.split("/");
    return parts.slice(1).map((_, index) => parts.slice(0, index + 2).join("/"));
  };
  // Index subtree membership once rather than scanning all fields for each
  // withheld node (large manifests can contain thousands of secret keys).
  const leftPaths = new Set([...left.keys()].flatMap(ancestors));
  const rightPaths = new Set([...right.keys()].flatMap(ancestors));
  return [...new Set([...left.keys(), ...right.keys()])].sort().flatMap<ConfigDiffRow>((path) => {
    // A withheld parent is unknown, not a removed tree plus added children.
    if (ancestors(path).slice(0, -1).some((parent) => withheldPaths.has(parent))) return [];
    const a = left.get(path);
    const b = right.get(path);
    if (a?.withheld || b?.withheld) {
      return [{ path, before: leftPaths.has(path) ? "not compared" : "missing", after: rightPaths.has(path) ? "not compared" : "missing", change: "secret value not compared" as const }];
    }
    if (a && b && a.value === b.value) return [];
    return [{ path, before: a?.value ?? "missing", after: b?.value ?? "missing", change: !a ? "added" as const : !b ? "removed" as const : "changed" as const }];
  });
}
