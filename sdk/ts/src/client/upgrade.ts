// The server's refusal of a client older than its minimum: connect
// FailedPrecondition with `x-forge-error-reason: upgrade_required`, and a
// google.rpc.ErrorInfo detail (reason UPGRADE_REQUIRED, domain reliantlabs.io)
// whose metadata names the floor and how to upgrade.

import { ConnectError } from "@connectrpc/connect";
import { BinaryReader, WireType } from "@bufbuild/protobuf/wire";

const REASON_HEADER = "x-forge-error-reason";
const REASON = "upgrade_required";
const ERROR_INFO_TYPE = "google.rpc.ErrorInfo";
const ERROR_INFO_REASON = "UPGRADE_REQUIRED";
const ERROR_INFO_DOMAIN = "reliantlabs.io";

/**
 * The server refused the call because this client is older than the minimum it
 * supports. Upgrading is the only fix; retrying does not help. Every field but
 * `message` may be empty: a refusal of a client that sent no version names no
 * floor.
 */
export class UpgradeRequiredError extends Error {
  override readonly name = "UpgradeRequiredError";
  /** The client the server judged, e.g. "sdk-ts". */
  readonly clientKind: string;
  /** The version the server saw in Reliant-Client. */
  readonly clientVersion: string;
  /** The oldest version the server accepts. */
  readonly minVersion: string;
  /** Where to read how to upgrade. */
  readonly upgradeUrl: string;
  /** A command that upgrades the client. */
  readonly upgradeCommand: string;
  /** The ConnectError the server's refusal arrived as. */
  override readonly cause: ConnectError;

  constructor(cause: ConnectError, metadata: Readonly<Record<string, string>> = {}) {
    super(cause.rawMessage || "upgrade required");
    this.cause = cause;
    this.clientKind = metadata.client_kind ?? "";
    this.clientVersion = metadata.client_version ?? "";
    this.minVersion = metadata.min_version ?? "";
    this.upgradeUrl = metadata.upgrade_url ?? "";
    this.upgradeCommand = metadata.upgrade_command ?? "";
  }

  /**
   * The upgrade refusal `err` is, or undefined. Accepts what a call threw: a
   * ConnectError (or the runtime's ConnectClientError), or an
   * UpgradeRequiredError, returned as is.
   */
  static from(err: unknown): UpgradeRequiredError | undefined {
    if (err instanceof UpgradeRequiredError) return err;
    if (!(err instanceof ConnectError)) return undefined;
    const byHeader = err.metadata.get(REASON_HEADER) === REASON;
    let metadata: Record<string, string> | undefined;
    for (const d of err.details) {
      if ("desc" in d || d.type !== ERROR_INFO_TYPE) continue;
      const info = decodeErrorInfo(d.value);
      if (info && info.reason === ERROR_INFO_REASON && (info.domain === ERROR_INFO_DOMAIN || info.domain === "")) {
        metadata = info.metadata;
        break;
      }
    }
    if (!byHeader && !metadata) return undefined;
    return new UpgradeRequiredError(err, metadata);
  }
}

/** Whether `err` is the server's refusal of this client as too old. */
export function isUpgradeRequired(err: unknown): boolean {
  return UpgradeRequiredError.from(err) !== undefined;
}

interface ErrorInfo {
  reason: string;
  domain: string;
  metadata: Record<string, string>;
}

// google.rpc.ErrorInfo: reason (1), domain (2), metadata (3, map<string,
// string>). Decoded by hand so the package needs no google.rpc schema.
function decodeErrorInfo(bytes: Uint8Array): ErrorInfo | undefined {
  try {
    const info: ErrorInfo = { reason: "", domain: "", metadata: {} };
    const r = new BinaryReader(bytes);
    while (r.pos < r.len) {
      const [field, wire] = r.tag();
      if (wire !== WireType.LengthDelimited) {
        r.skip(wire, field);
      } else if (field === 1) {
        info.reason = r.string();
      } else if (field === 2) {
        info.domain = r.string();
      } else if (field === 3) {
        const entry = new BinaryReader(r.bytes());
        let key = "";
        let value = "";
        while (entry.pos < entry.len) {
          const [f, w] = entry.tag();
          if (w !== WireType.LengthDelimited) entry.skip(w, f);
          else if (f === 1) key = entry.string();
          else if (f === 2) value = entry.string();
          else entry.skip(w, f);
        }
        info.metadata[key] = value;
      } else {
        r.skip(wire, field);
      }
    }
    return info;
  } catch {
    return undefined;
  }
}
