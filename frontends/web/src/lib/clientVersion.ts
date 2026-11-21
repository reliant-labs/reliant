/**
 * WHICH BUILD OF WHICH CLIENT THIS IS, AND WHETHER THE SERVER STILL SERVES IT.
 *
 * Every first-party client names itself to the reliant API in one request
 * header, `Reliant-Client: <kind>/<version>`: this app is `web/<version>` in a
 * browser tab and `desktop/<version>` inside the Electron shell. The server
 * compares it with the minimum it serves for that kind, refuses an older one
 * with FailedPrecondition and the reason `upgrade_required`, and publishes the
 * minimums on SystemService.Version (`client_minimums`), so the app can show
 * its forced upgrade before a refused call ever happens.
 *
 * The comparison is semver precedence, the server's own rule: `1.9.2-dev+abc`
 * is below `1.9.2`, build metadata is ignored, and a version that does not
 * parse (a bare commit, "unknown") is never below anything. A client that
 * cannot name its version still sends the header with what it has: an
 * unparseable version is allowed, where a missing header can be refused.
 */

import { BinaryReader, WireType } from "@bufbuild/protobuf/wire";
import { ConnectError } from "@connectrpc/connect";
import type { ClientMinimum, ClientMinimums } from "../gen/reliant/v1/system_pb";
import { appBuild } from "./appBuild";

export type ClientKind = "web" | "desktop";

export interface ClientIdentity {
  kind: ClientKind;
  /** Semver without a leading "v" when the build knows it; else "unknown". */
  version: string;
}

/** What a header value may carry; anything else is replaced, never sent. */
const VERSION_TOKEN = /^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$/;

function cleanVersion(raw: string | null | undefined): string | null {
  const v = raw?.trim().replace(/^v(?=\d)/, "");
  return v && VERSION_TOKEN.test(v) && v !== "unknown" ? v : null;
}

/**
 * This client's kind and version. Inside Electron it is the desktop app's
 * version (the preload's `window.forgeDesktop.appVersion`), because the desktop
 * app is what has to update; in a browser it is the bundle's own release
 * identity, baked in at build time.
 */
export function clientIdentity(): ClientIdentity {
  const desktop =
    typeof window !== "undefined" && (!!window.electronAPI || !!window.RELIANT_CONFIG?.isElectron);
  const baked = cleanVersion(appBuild()?.version);
  if (desktop) {
    return { kind: "desktop", version: cleanVersion(window.forgeDesktop?.appVersion) ?? baked ?? "unknown" };
  }
  return { kind: "web", version: baked ?? "unknown" };
}

/** The `Reliant-Client` header value: `web/1.9.2`, `desktop/1.9.2`. */
export function clientHeaderValue(identity: ClientIdentity = clientIdentity()): string {
  return `${identity.kind}/${identity.version}`;
}

// ─── Semver precedence ───────────────────────────────────────────────

interface Semver {
  core: [number, number, number];
  pre: string[];
}

const SEMVER =
  /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z.-]+)?$/;

function parseSemver(raw: string): Semver | null {
  const m = SEMVER.exec(raw.trim());
  if (!m) return null;
  return { core: [Number(m[1]), Number(m[2]), Number(m[3])], pre: m[4] ? m[4].split(".") : [] };
}

function compareIdentifiers(a: string, b: string): number {
  const an = /^\d+$/.test(a);
  const bn = /^\d+$/.test(b);
  if (an && bn) return Number(a) - Number(b);
  if (an !== bn) return an ? -1 : 1; // numeric identifiers sort first
  return a < b ? -1 : a > b ? 1 : 0;
}

function compareSemver(a: Semver, b: Semver): number {
  for (let i = 0; i < 3; i++) {
    if (a.core[i] !== b.core[i]) return a.core[i] - b.core[i];
  }
  // A prerelease sorts before its release.
  if (!a.pre.length || !b.pre.length) return b.pre.length - a.pre.length;
  for (let i = 0; i < Math.min(a.pre.length, b.pre.length); i++) {
    const c = compareIdentifiers(a.pre[i], b.pre[i]);
    if (c !== 0) return c;
  }
  return a.pre.length - b.pre.length;
}

/**
 * True only when both versions parse and `version` is older than `minimum`.
 * An empty minimum is no floor; an unparseable version is never forced.
 */
export function isBelowMinimum(version: string, minimum: string | null | undefined): boolean {
  if (!minimum?.trim()) return false;
  const v = parseSemver(version);
  const min = parseSemver(minimum);
  return !!v && !!min && compareSemver(v, min) < 0;
}

/** The served minimum for this client's kind, or undefined when none is set. */
export function minimumFor(kind: ClientKind, minimums: ClientMinimums | undefined): ClientMinimum | undefined {
  return kind === "desktop" ? minimums?.desktop : minimums?.web;
}

// ─── The refusal's detail ────────────────────────────────────────────

/** What the server's refusal says about the floor (its google.rpc.ErrorInfo). */
export interface UpgradeRequiredInfo {
  minVersion?: string;
  upgradeUrl?: string;
  upgradeCommand?: string;
}

const ERROR_INFO_TYPE = "google.rpc.ErrorInfo";
const UPGRADE_REQUIRED_INFO_REASON = "UPGRADE_REQUIRED";

/**
 * Decodes google.rpc.ErrorInfo (reason = 1, domain = 2, map<string,string>
 * metadata = 3) by hand: the web has no generated googleapis types, and three
 * fields do not justify adding them.
 */
function decodeErrorInfo(bytes: Uint8Array): { reason: string; metadata: Record<string, string> } {
  const reader = new BinaryReader(bytes);
  let reason = "";
  const metadata: Record<string, string> = {};
  while (reader.pos < reader.len) {
    const [field, wire] = reader.tag();
    if (field === 1 && wire === WireType.LengthDelimited) {
      reason = reader.string();
    } else if (field === 3 && wire === WireType.LengthDelimited) {
      const entry = new BinaryReader(reader.bytes());
      let key = "";
      let value = "";
      while (entry.pos < entry.len) {
        const [f, w] = entry.tag();
        if (f === 1 && w === WireType.LengthDelimited) key = entry.string();
        else if (f === 2 && w === WireType.LengthDelimited) value = entry.string();
        else entry.skip(w, f);
      }
      if (key) metadata[key] = value;
    } else {
      reader.skip(wire, field);
    }
  }
  return { reason, metadata };
}

/**
 * The floor an `upgrade_required` refusal names, from its ErrorInfo detail.
 * Empty when the error carries no such detail (or one that does not decode):
 * the forced upgrade still opens, it just cannot name the minimum.
 */
export function upgradeRequiredInfo(err: unknown): UpgradeRequiredInfo {
  if (!(err instanceof ConnectError)) return {};
  for (const detail of err.details) {
    if (!("type" in detail) || detail.type !== ERROR_INFO_TYPE) continue;
    try {
      const { reason, metadata } = decodeErrorInfo(detail.value);
      if (reason !== UPGRADE_REQUIRED_INFO_REASON) continue;
      return {
        minVersion: metadata.min_version || undefined,
        upgradeUrl: metadata.upgrade_url || undefined,
        upgradeCommand: metadata.upgrade_command || undefined,
      };
    } catch {
      // A malformed detail names nothing; the refusal itself still counts.
    }
  }
  return {};
}
