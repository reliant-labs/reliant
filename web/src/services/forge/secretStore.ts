// Copyright (c) 2025 Reliant Labs

/**
 * The MANAGED secret store's data layer.
 *
 * Sibling of services/forge/secrets.ts and deliberately NOT a replacement for
 * it. The two answer different questions about different stores:
 *
 *   secrets.ts      `forge secret list --json`, via the DAEMON. Answers "what
 *                   does this environment DECLARE, and does forge's own store
 *                   hold a value" — for the file/external/none providers forge
 *                   ships today.
 *   secretStore.ts  control-plane's SecretStoreService, via Connect. Answers
 *                   "what does the MANAGED store hold" — names, versions,
 *                   timestamps — for the hosted store that sits in front of
 *                   OpenBao.
 *
 * A screen needs both, because neither is the whole truth: forge knows which
 * secrets a workload DECLARES (and the managed store has no idea), and the
 * managed store knows which secrets actually EXIST and how many times they have
 * been written (and forge, holding no values for it, has no idea). Joining them
 * is what makes "declared but never set" a renderable state.
 *
 * ── THE VALUE IS NOT HERE, AND CANNOT BE ─────────────────────────────────────
 *
 * Not by this module's discipline — by the storage engine's refusal. The
 * server-side token control-plane holds has `list` + `read` on
 * `secret/metadata/**` and NO capability on `secret/data/` at all. Verified by
 * attacking it with that exact credential:
 *
 *   bao token capabilities secret/data/...      -> create, update   (NO read)
 *   bao kv get <secret>                         -> "* permission denied"
 *   bao kv metadata get <secret>                -> version history, no value
 *
 * So there is no reveal here because there is no RPC that could serve one, and
 * adding a field capable of carrying a value to any RESPONSE message fails
 * control-plane's TestSecretStoreResponsesCarryNoSecretValueFields — a
 * reflection test over an allow-list. `setSecret` below takes a value because
 * it travels INBOUND and is the one such field in the whole surface; it is
 * accepted, forwarded, and never stored in this module, never returned, and
 * never logged.
 *
 * That is why `SetSecretResult` carries a version number and a timestamp rather
 * than an echo of what was written: a write-only field that echoed back would
 * be a read path wearing a write path's name.
 */

import { ConnectError, Code } from "@connectrpc/connect";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import { SecretStoreService } from "@/gen/controlplane/services/secret_store/v1/secret_store_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";
import { destinationOf } from "./topology";

// ── Domain types ────────────────────────────────────────────────────────────
//
// Plain, serialisable shapes rather than the generated protobuf messages, for
// the same reason services/controlPlane/git/cloud.ts converts: timestamps
// become ISO strings the view can format without importing protobuf wkt, and
// the view layer stays testable with object literals.

/** One secret's list-view metadata. Never a value — no field here could hold one. */
export interface ManagedSecretSummary {
  name: string;
  /**
   * The newest version number. ZERO IS MEANINGFUL AND IS NOT "no secret": it
   * means the secret has metadata but no live version, which is what remains
   * after every version has been destroyed. A tombstone, and the UI says so
   * rather than rendering it as an ordinary absent secret.
   */
  currentVersion: number;
  /** Oldest version still retained; anything below it aged out under max_versions. */
  oldestVersion: number;
  /** Retention count Bao enforces. Zero means the mount default applies. */
  maxVersions: number;
  createdAt?: string;
  updatedAt?: string;
  /** Soft-deleted — recoverable via undelete. */
  currentVersionDeleted: boolean;
  /** Permanently destroyed — not recoverable. */
  currentVersionDestroyed: boolean;
}

/** One immutable version's metadata. */
export interface ManagedSecretVersion {
  version: number;
  createdAt?: string;
  /** Set when soft-deleted; absent otherwise. Presence IS the soft-deleted flag. */
  deletedAt?: string;
  destroyed: boolean;
}

export interface ManagedSecretHistory {
  summary: ManagedSecretSummary | null;
  /** Newest first — the order a reader scans for "what changed most recently". */
  versions: ManagedSecretVersion[];
}

export interface SetSecretResult {
  /** The version this write created. Not the value it wrote. */
  version: number;
  createdAt?: string;
}

// ── Store availability ──────────────────────────────────────────────────────

/**
 * Whether the managed store is usable, and if not, why.
 *
 * This is a four-state answer rather than a boolean because the three failures
 * call for completely different sentences, and collapsing them produces the
 * "something went wrong" screen that tells a user nothing:
 *
 *   available      the store answered. Render the full surface.
 *   not-configured control-plane is reachable but has no OpenBao bound, so the
 *                  handler answers Unavailable on every RPC. Nothing is broken;
 *                  this deployment simply has no managed store.
 *   no-control-plane  reliant is running without a control plane at all (no
 *                  VITE_CONTROL_PLANE_API_URL). Local/OSS deployments live
 *                  here, and a managed store is not a thing they have.
 *   unreachable    the RPC failed for a transport reason. Genuinely an error.
 */
export type ManagedStoreAvailability =
  | "available"
  | "not-configured"
  | "no-control-plane"
  | "unreachable"
  /**
   * The environment does not deploy to a control plane at all (a cluster,
   * compose, host… env). A managed store is keyed by a control-plane
   * environment id, and this env has none — so there is nothing to look up,
   * and NO call is made. Not an error; the env simply has no managed store.
   */
  | "not-hosted"
  /**
   * Hosted, but forge reported no environment id: the control plane has
   * never been asked to ensure this environment (`forge env deploy` creates
   * it). There is no id to key a lookup on, and inventing one is the thing
   * this state exists to refuse.
   */
  | "not-ensured"
  /**
   * Hosted on a control plane OTHER than the one this console talks to. The
   * id belongs to that control plane's tenant space; sending it here would
   * answer NotFound at best, so no call is made.
   */
  | "other-control-plane";

export function hasControlPlane(): boolean {
  return !!CONTROL_PLANE_API_URL;
}

// ── Which store, keyed how ──────────────────────────────────────────────────

/**
 * What the managed store lookup for ONE environment is keyed on — or why
 * there is no lookup at all.
 *
 * SecretStoreService is keyed by a control-plane `environment_id` (the org is
 * resolved server-side from that row). The only honest source of that id is
 * forge's own `env topology --json` / `env status --json`, which reports it
 * for hosted envs and ONLY once the env has been ensured. So the id is read
 * off the report, never looked up by name here and never fabricated: a
 * browser-side name→id lookup would be a second, divergent answer to "which
 * environment" (forge's hostedEnvResolver is the first).
 */
export type ManagedStoreTarget =
  | { kind: "lookup"; environmentId: string; endpoint: string }
  | { kind: "none"; availability: Exclude<ManagedStoreAvailability, "available" | "unreachable"> };

/** The env facts the target is derived from. Structural, so topology and status both fit. */
export interface ManagedStoreEnvFacts {
  destination?: string;
  endpoint?: string;
  environment_id?: string;
}

/**
 * normalizeEndpoint reduces a control-plane URL to a comparable origin.
 *
 * `localhost` and `127.0.0.1` are the same host for this purpose — forge's
 * KCL and Vite's env disagree on the spelling in dev, and treating them as
 * different control planes would hide a store that is right there.
 */
export function normalizeEndpoint(url: string | undefined): string {
  const raw = (url ?? "").trim();
  if (raw === "") return "";
  try {
    const parsed = new URL(raw);
    const host = parsed.hostname === "localhost" ? "127.0.0.1" : parsed.hostname;
    const port = parsed.port ? `:${parsed.port}` : "";
    return `${parsed.protocol}//${host}${port}`.toLowerCase();
  } catch {
    return raw.replace(/\/+$/, "").toLowerCase();
  }
}

/**
 * managedStoreTarget decides whether — and against what id — the managed
 * store may be asked about an environment.
 *
 * Pure, and total over every input, including a report too old to carry
 * `destination` at all: an absent destination is NOT hosted, so it makes no
 * call. That direction matters: guessing "hosted" would fire a lookup keyed
 * on nothing.
 *
 * `consoleEndpoint` is the control plane this console talks to. An empty
 * value means this build has none, which outranks everything else.
 */
export function managedStoreTarget(
  env: ManagedStoreEnvFacts | null | undefined,
  consoleEndpoint: string = CONTROL_PLANE_API_URL
): ManagedStoreTarget {
  if (!consoleEndpoint) return { kind: "none", availability: "no-control-plane" };
  if (!env || destinationOf(env) !== "hosted") return { kind: "none", availability: "not-hosted" };

  const environmentId = (env.environment_id ?? "").trim();
  if (environmentId === "") return { kind: "none", availability: "not-ensured" };

  const endpoint = normalizeEndpoint(env.endpoint);
  // An env that names no endpoint cannot be proven to live elsewhere; the id
  // came from the control plane forge talked to, which is the declared one.
  if (endpoint !== "" && endpoint !== normalizeEndpoint(consoleEndpoint)) {
    return { kind: "none", availability: "other-control-plane" };
  }
  return { kind: "lookup", environmentId, endpoint };
}

/**
 * One sentence for each reason there is no lookup. Each answers "then where
 * DO these secrets live, and how do I set one" rather than just refusing.
 */
export function availabilityExplanation(availability: ManagedStoreAvailability): string | null {
  switch (availability) {
    case "not-hosted":
      return "This environment is not hosted, so it has no managed store. Its values come from the secret provider its forge config declares.";
    case "not-ensured":
      return "This hosted environment has not been deployed yet, so its managed store does not exist here yet. The first deploy creates it — you can set values now with `forge secret set`.";
    case "other-control-plane":
      return "This environment is hosted on a different control plane from the one you are signed in to, so its store cannot be read from here. Set values with `forge secret set`.";
    case "unreachable":
      return "The managed store could not be reached, so what it holds is not known right now. This is a connection problem, not a statement about your secrets.";
    default:
      return null;
  }
}

/**
 * Map a thrown Connect error onto the availability vocabulary.
 *
 * `Unavailable` is control-plane's documented answer when no OpenBao is bound
 * (internal/app/providers.go leaves SecretStoreService nil and the handler
 * answers Unavailable on every RPC), and `Unimplemented` is what a control
 * plane too old to have mounted the service returns — a 404 at the Connect
 * layer. Both mean "no managed store here", which is a state to describe, not
 * an error to report.
 */
export function availabilityFromError(err: unknown): ManagedStoreAvailability {
  if (!hasControlPlane()) return "no-control-plane";
  if (err instanceof ConnectError) {
    if (err.code === Code.Unavailable || err.code === Code.Unimplemented) {
      return "not-configured";
    }
  }
  return "unreachable";
}

// ── Conversion ──────────────────────────────────────────────────────────────

function toISO(ts: Timestamp | undefined): string | undefined {
  if (!ts) return undefined;
  try {
    const date = timestampDate(ts);
    // A zero/unset protobuf timestamp converts to the epoch rather than
    // throwing, and rendering "1 Jan 1970" as a creation date is worse than
    // rendering nothing — it looks like real data.
    if (!Number.isFinite(date.getTime()) || date.getTime() <= 0) return undefined;
    return date.toISOString();
  } catch {
    return undefined;
  }
}

/**
 * Convert one summary message.
 *
 * Every field is read EXPLICITLY. That is the same discipline services/forge/
 * secrets.ts applies for the same reason: a field that appeared in a newer
 * control-plane and could carry something unexpected is dropped here rather
 * than spread into the view by an object spread.
 */
function toSummary(msg: {
  name: string;
  currentVersion: number;
  oldestVersion: number;
  maxVersions: number;
  createdTime?: Timestamp;
  updatedTime?: Timestamp;
  currentVersionDeleted: boolean;
  currentVersionDestroyed: boolean;
}): ManagedSecretSummary {
  return {
    name: msg.name,
    currentVersion: msg.currentVersion,
    oldestVersion: msg.oldestVersion,
    maxVersions: msg.maxVersions,
    createdAt: toISO(msg.createdTime),
    updatedAt: toISO(msg.updatedTime),
    currentVersionDeleted: msg.currentVersionDeleted === true,
    currentVersionDestroyed: msg.currentVersionDestroyed === true,
  };
}

function toVersion(msg: {
  version: number;
  createdTime?: Timestamp;
  deletionTime?: Timestamp;
  destroyed: boolean;
}): ManagedSecretVersion {
  return {
    version: msg.version,
    createdAt: toISO(msg.createdTime),
    deletedAt: toISO(msg.deletionTime),
    destroyed: msg.destroyed === true,
  };
}

// ── RPCs ────────────────────────────────────────────────────────────────────

function client() {
  return getControlPlaneClient(SecretStoreService);
}

/** Metadata for every secret in one hosted environment. Sorted by name. */
export async function listSecrets(environmentId: string): Promise<ManagedSecretSummary[]> {
  const res = await client().listSecrets({ environmentId });
  return (res.secrets ?? [])
    .filter((s) => typeof s?.name === "string" && s.name !== "")
    .map(toSummary)
    .sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * One secret's full version history, newest first.
 *
 * Sorted descending here rather than in the view because "newest first" is a
 * property of how version history is READ, not of one component's layout, and
 * every consumer wants the same order.
 */
export async function getSecretVersions(
  environmentId: string,
  name: string
): Promise<ManagedSecretHistory> {
  const res = await client().getSecretVersions({ environmentId, name });
  return {
    summary: res.summary ? toSummary(res.summary) : null,
    versions: (res.versions ?? []).map(toVersion).sort((a, b) => b.version - a.version),
  };
}

/**
 * Write a new version.
 *
 * `cas` is KV-v2 check-and-set and is how CREATE is distinguished from UPDATE:
 * passing 0 means "must not exist yet", so a create that races another create
 * fails loudly instead of silently clobbering. Passing the current version
 * means "update, and only if nobody else has written since I read this".
 * Omitting it writes unconditionally.
 *
 * The value is forwarded and then forgotten. It is not returned, not cached,
 * and deliberately not included in any error this function can raise.
 */
export async function setSecret(args: {
  environmentId: string;
  name: string;
  value: string;
  cas?: number;
}): Promise<SetSecretResult> {
  const res = await client().setSecret({
    environmentId: args.environmentId,
    name: args.name,
    secretValue: args.value,
    ...(args.cas === undefined ? {} : { cas: args.cas }),
  });
  return { version: res.version, createdAt: toISO(res.createdTime) };
}

/**
 * Soft-delete versions. Reversible via {@link undeleteSecret}.
 *
 * An empty `versions` means the current version only, which is KV-v2's own
 * default and the one this UI uses for the row-level "Delete" action.
 */
export async function deleteSecret(args: {
  environmentId: string;
  name: string;
  versions?: number[];
}): Promise<void> {
  await client().deleteSecret({
    environmentId: args.environmentId,
    name: args.name,
    versions: args.versions ?? [],
  });
}

/** Restore soft-deleted versions. Cannot restore a destroyed one — that data is gone. */
export async function undeleteSecret(args: {
  environmentId: string;
  name: string;
  versions: number[];
}): Promise<void> {
  await client().undeleteSecret({
    environmentId: args.environmentId,
    name: args.name,
    versions: args.versions,
  });
}

/**
 * Permanently destroy versions. IRREVERSIBLE.
 *
 * A separate function against a separate RPC against a separate Bao path,
 * mirroring how KV-v2 models it — not a flag on delete. The shape of the API
 * is the first place the difference between "recoverable" and "gone" should be
 * visible, before any confirmation dialog gets a chance to be dismissed.
 */
export async function destroySecret(args: {
  environmentId: string;
  name: string;
  versions: number[];
}): Promise<void> {
  await client().destroySecret({
    environmentId: args.environmentId,
    name: args.name,
    versions: args.versions,
  });
}

// ── Projections ─────────────────────────────────────────────────────────────

/**
 * What a managed secret's current state IS, for rendering.
 *
 * `tombstoned` exists because currentVersion === 0 is not the same as "this
 * secret does not exist". The metadata row survives destruction — that is what
 * keeps history honest — so a secret whose every version was destroyed is a
 * real row that holds nothing, and it needs its own sentence rather than being
 * rendered as if someone simply had not set it yet.
 */
export type ManagedSecretState = "set" | "deleted" | "destroyed" | "tombstoned";

export function managedSecretState(summary: ManagedSecretSummary): ManagedSecretState {
  if (summary.currentVersionDestroyed) return "destroyed";
  if (summary.currentVersionDeleted) return "deleted";
  if (summary.currentVersion === 0) return "tombstoned";
  return "set";
}

export function managedSecretStateLabel(state: ManagedSecretState): string {
  switch (state) {
    case "set":
      return "Set";
    case "deleted":
      return "Deleted";
    case "destroyed":
      return "Destroyed";
    default:
      return "No versions";
  }
}

export function managedSecretStateExplanation(state: ManagedSecretState): string {
  switch (state) {
    case "set":
      return "The store holds a value for this secret. Reliant is told a version exists; it is never told what the version holds.";
    case "deleted":
      return "The current version is soft-deleted. Nothing can read it, but the bytes are still there and Undelete restores it.";
    case "destroyed":
      return "The current version was permanently destroyed. Its metadata row remains so the history stays honest, but the value is gone and cannot be recovered.";
    default:
      return "This secret has a metadata record but no live version — every version it had has been destroyed. Setting it creates a new version.";
  }
}
