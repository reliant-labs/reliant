// Copyright (c) 2025 Reliant Labs

/**
 * REGISTER: give an environment that exists only in the user's KCL a
 * control-plane row, so Live can show it and its secrets can be set — with
 * the daemon offline from then on.
 *
 * ── THE CHICKEN-AND-EGG THIS BREAKS ─────────────────────────────────────────
 *
 * Live reads the control plane and never the daemon (design §8.0, O-14). An
 * environment nobody has built has no row, so there is nothing for Live to
 * read: it cannot show the env, and the managed store has no environment to
 * hold a value against. That is the trap the owner hit — you cannot set a
 * secret before the first deploy, and you cannot deploy without the secret.
 *
 * Register is the bootstrap (briefing §6, design §10 state 2). PREVIEW, which
 * is daemon-dependent because only the daemon can read the user's files, asks
 * forge for the env's own projection of its render (`forge env shape <env>
 * --json`). The BROWSER then calls EnsureEnvironment itself, with the user's
 * session — not the daemon's token, which is the indirection that produced the
 * 403. Live then shows "Declared, not built yet", and from that moment the
 * environment needs no daemon again except for Preview and further builds.
 *
 * ── WHY THE SHAPE COMES FROM FORGE AND NOTHING ELSE ─────────────────────────
 *
 * `forge env shape` is the SAME projection `forge env build` records. Nothing
 * here derives a shape, fills a default, or asks the user: the env's kind is
 * IMMUTABLE once the row exists, so a Register that guessed would produce an
 * environment that can only be abandoned, and a Register that disagreed with
 * the next build would be decided by whichever ran first. So this module
 * validates the document and refuses, rather than repairing it.
 *
 * `declared_by` is the render's own provenance — a CLAIM, stored as such
 * (O-11). Identity (`created_by`) is always server-set from the token.
 */

import type { JsonObject } from "@bufbuild/protobuf";

import { DeployEnvironmentKind } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";

import type { LiveEnvKind } from "./live";

/**
 * `forge env shape <env> --json` — the document Preview reads and Register
 * sends. Typed as ADDITIVE like every other forge report contract: everything
 * forge marks omitempty is optional, and a document from a newer forge must
 * parse rather than throw.
 */
export interface ForgeEnvShapeReport {
  project?: string;
  env?: string;
  /** forge's predicate over the render: persistent | preview | self_managed | local. */
  kind?: string;
  /** The canonical JSON of release.Shape — snake_case keys, exactly as recorded. */
  shape?: JsonObject;
  /** Where the render came from. A claim. */
  provenance?: {
    repo?: string;
    commit?: string;
    branch?: string;
    tag?: string;
    dirty?: boolean;
    tree?: string;
    forge_version?: string;
    worktree?: { key?: string; label?: string; host_id?: string };
  };
}

/**
 * What a Register needs, extracted from forge's document and checked.
 *
 * A separate step from the call so Preview can decide whether to OFFER the
 * button at all: an env whose shape forge could not project is not registered
 * by a button that fails on click.
 */
export interface RegisterCandidate {
  project: string;
  name: string;
  kind: LiveEnvKind;
  shape: JsonObject;
  report: ForgeEnvShapeReport;
}

/**
 * The kinds that may be registered, mapped onto the wire enum.
 *
 * `undefined` for anything else, INCLUDING an empty string and an
 * unrecognised value from a newer forge. Never UNSPECIFIED and never a
 * default: the kind is immutable, so an unrecognised one must stop the
 * Register rather than be coerced into the nearest familiar kind.
 */
function kindForWire(kind: string): DeployEnvironmentKind | undefined {
  switch (kind.trim().toLowerCase()) {
    case "persistent":
      return DeployEnvironmentKind.PERSISTENT;
    case "preview":
      return DeployEnvironmentKind.PREVIEW;
    case "self_managed":
      return DeployEnvironmentKind.SELF_MANAGED;
    case "local":
      return DeployEnvironmentKind.LOCAL;
    default:
      return undefined;
  }
}

function liveKindOf(kind: string): LiveEnvKind {
  switch (kind.trim().toLowerCase()) {
    case "persistent":
    case "preview":
    case "self_managed":
    case "local":
      return kind.trim().toLowerCase() as LiveEnvKind;
    default:
      return "unknown";
  }
}

/**
 * Read a Register candidate out of forge's shape document, or explain why
 * there is none.
 *
 * `project` falls back to the project name the caller already holds (the
 * forge.yaml name Live joins on), because an older forge may not echo it —
 * but a MISMATCH is refused rather than reconciled: the two disagreeing means
 * the document describes a different project's environment, and registering
 * it under this one would file an env where nothing will look for it.
 */
export function registerCandidate(
  report: ForgeEnvShapeReport | null | undefined,
  expected: { project: string | null; env: string }
): { ok: true; candidate: RegisterCandidate } | { ok: false; reason: string } {
  if (!report) {
    return { ok: false, reason: "forge has not rendered this environment yet." };
  }

  const env = (report.env ?? "").trim() || expected.env.trim();
  if (env === "" || (report.env ?? "").trim() !== "" && report.env!.trim() !== expected.env.trim()) {
    return {
      ok: false,
      reason: `forge rendered ${report.env ?? "a different environment"}, not ${expected.env}.`,
    };
  }

  const reported = (report.project ?? "").trim();
  const expectedProject = (expected.project ?? "").trim();
  if (reported !== "" && expectedProject !== "" && reported !== expectedProject) {
    return {
      ok: false,
      reason: `forge says this environment belongs to ${reported}, not ${expectedProject}.`,
    };
  }
  const project = reported || expectedProject;
  if (project === "") {
    return {
      ok: false,
      reason: "Reliant does not know which forge project this environment belongs to.",
    };
  }

  const kind = liveKindOf(report.kind ?? "");
  if (kind === "unknown") {
    return {
      ok: false,
      reason:
        "forge did not say how this environment runs, and an environment's kind cannot be changed once it is recorded.",
    };
  }

  const shape = report.shape;
  if (!shape || typeof shape !== "object" || Array.isArray(shape) || Object.keys(shape).length === 0) {
    return { ok: false, reason: "forge's render carried no shape for this environment." };
  }

  // The shape's own kind must agree with the document's. The server checks
  // this too and refuses a mismatch; checking here means the user is told
  // before the write rather than by an InvalidArgument afterwards.
  const shapeKind = typeof shape.kind === "string" ? liveKindOf(shape.kind) : "unknown";
  if (shapeKind !== kind) {
    return {
      ok: false,
      reason: "forge's render disagrees with itself about how this environment runs.",
    };
  }

  // F-13, defence in depth. The shape carries secret NAMES and PROVIDERS
  // only, and the server refuses a shape whose JSON holds a value-like key
  // anywhere. Refusing here as well means a browser never TRANSMITS one — the
  // cheapest place to stop it is before the request exists.
  if (carriesValueLikeKey(shape)) {
    return {
      ok: false,
      reason: "forge's render carries secret values, which Reliant won't send anywhere.",
    };
  }

  return { ok: true, candidate: { project, name: env, kind, shape, report } };
}

const VALUE_LIKE_KEYS = new Set(["value", "data", "stringData"]);

/** Any `value` / `data` / `stringData` key, at any depth. */
function carriesValueLikeKey(node: unknown): boolean {
  if (Array.isArray(node)) return node.some(carriesValueLikeKey);
  if (!node || typeof node !== "object") return false;
  for (const [key, child] of Object.entries(node)) {
    if (VALUE_LIKE_KEYS.has(key)) return true;
    if (carriesValueLikeKey(child)) return true;
  }
  return false;
}

/**
 * Create the environment's row from the Preview render, and return its id.
 *
 * Idempotent server-side: EnsureEnvironment returns the existing row unchanged
 * when one is already there, and refuses — rather than rewriting — a
 * declaration whose immutable fields disagree with it. So a second Register is
 * harmless, and a Register against an env someone else built is refused by the
 * server rather than silently re-declaring it.
 */
export async function registerEnvironment(candidate: RegisterCandidate): Promise<string> {
  const kind = kindForWire(candidate.kind);
  if (kind === undefined) {
    // Unreachable through registerCandidate, which refuses an unknown kind.
    // It throws rather than defaulting because every available default is a
    // claim forge never made, written into an immutable field.
    throw new Error(
      "forge did not say how this environment runs, and an environment's kind cannot be changed once it is recorded."
    );
  }

  const res = await getControlPlaneClient(DeployService).ensureEnvironment({
    spec: {
      project: candidate.project,
      name: candidate.name,
      kind,
      // The Struct's content is the canonical JSON of release.Shape, passed
      // through exactly as forge produced it. The server decodes it strictly
      // (DisallowUnknownFields), validates it, and stores the RE-ENCODED
      // canonical bytes — so nothing is gained by reshaping it here, and a
      // reshape could only make it disagree with what a build records.
      shape: candidate.shape,
      declaredBy: provenanceForWire(candidate.report.provenance),
    },
  });

  const id = (res.environment?.id ?? "").trim();
  if (id === "") {
    throw new Error(`${candidate.name} was added, but Reliant did not get an id back for it.`);
  }
  return id;
}

/** forge's snake_case provenance → the wire message. Absent stays absent. */
function provenanceForWire(provenance: ForgeEnvShapeReport["provenance"]) {
  if (!provenance) return undefined;
  const worktree = provenance.worktree;
  return {
    repo: provenance.repo ?? "",
    commit: provenance.commit ?? "",
    branch: provenance.branch ?? "",
    tag: provenance.tag ?? "",
    dirty: provenance.dirty === true,
    tree: provenance.tree ?? "",
    forgeVersion: provenance.forge_version ?? "",
    worktree: worktree
      ? {
          key: worktree.key ?? "",
          label: worktree.label ?? "",
          hostId: worktree.host_id ?? "",
        }
      : undefined,
  };
}
