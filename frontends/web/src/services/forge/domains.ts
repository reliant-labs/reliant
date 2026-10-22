// Copyright (c) 2025 Reliant Labs

/**
 * CUSTOM DOMAINS — the data layer over control-plane's DomainService.
 *
 * Sibling of cloudEnvs.ts and secretStore.ts, and it reaches the control
 * plane the same way they do: getControlPlaneClient over the shared
 * interceptor chain. No daemon is involved. A domain is an ORG resource, so
 * unlike everything else on the forge surface it is not scoped to a project
 * or an environment at all — its BINDING is.
 *
 * ── WHY THE RECORDS ARE NEVER COMPUTED HERE ─────────────────────────────────
 *
 * `required_records` arrives from the server, per domain, and is rendered
 * verbatim. It is tempting to derive it — an apex gets an A to the ingress
 * IP, a subdomain a CNAME to the ingress host, both a TXT at
 * `_reliant-challenge.<host>` — and that derivation is right today and would
 * be wrong the first time the platform adds an address, moves the ingress
 * name, or reserves an IPv6. The tenant would then paste a record that no
 * longer verifies, from a screen that looked authoritative. The server knows
 * its own ingress configuration; this module does not, and must not guess.
 *
 * ── PLAIN SHAPES, EXPLICIT FIELDS ───────────────────────────────────────────
 *
 * Converted out of the generated messages for the same reason secretStore.ts
 * converts: timestamps become ISO strings the view formats without importing
 * protobuf wkt, the view layer stays testable with object literals, and every
 * field is read EXPLICITLY — a field a newer control plane adds is dropped
 * here rather than spread into a component.
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  DeployCustomDomainState,
  DomainSource,
  type Domain as DomainMessage,
  type DomainBinding as DomainBindingMessage,
  type DeployDnsRecord,
} from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { DomainService } from "@/gen/controlplane/services/domain/v1/domain_pb";
import { getControlPlaneClient } from "@/services/controlPlane/client";
import { CONTROL_PLANE_API_URL } from "@/services/controlPlane/config";

import { workloadLink, type ForgeHostedWorkload } from "./topology";

// ── Domain types ────────────────────────────────────────────────────────────

/**
 * Where a domain is in acquisition, in this app's vocabulary.
 *
 * One name per control-plane state, plus `unknown` for a state this build
 * does not recognise. `unknown` never folds into `pending-dns`: telling a
 * tenant to go and publish DNS is an instruction, and issuing one on the
 * strength of an enum we could not decode would be inventing the instruction.
 */
export type DomainState =
  | "pending-dns"
  | "verifying"
  | "issuing"
  | "live"
  | "failed"
  | "conflict"
  | "unknown";

/** Where the domain came from. Only `external` domains need tenant DNS. */
export type DomainOrigin = "external" | "platform" | "unknown";

/**
 * What the last verification pass concluded about one record.
 *
 * THREE STATES, NOT TWO, and collapsing them is the mistake to avoid. The
 * server sends `resolved` and `detail`, and the PAIR is the answer:
 *
 *   ok        resolved
 *   failed    not resolved, WITH a detail saying what was seen
 *   unchecked not resolved, NO detail — the verifier has not reached it
 *
 * `unchecked` is every record on a domain added seconds ago. Rendering it
 * as a failure would put a red cross on a record that is very likely
 * correct, which is worse than no mark at all: the tenant goes and "fixes"
 * something that was never broken.
 */
export type DnsRecordCheck = "ok" | "failed" | "unchecked";

/** One DNS record the tenant publishes at their provider. */
export interface DomainDnsRecord {
  /** 'A', 'CNAME' or 'TXT'. */
  type: string;
  /** Fully qualified: 'example.com', '_reliant-challenge.example.com'. */
  name: string;
  /** An IP for A, the ingress host for CNAME, the ownership token for TXT. */
  value: string;
  /** What the last pass concluded. See DnsRecordCheck. */
  check: DnsRecordCheck;
  /**
   * Why it is not confirmed, in the server's words: "resolves to
   * 203.0.113.7, expected 34.63.203.181". Empty unless `check` is
   * `failed`. Rendered verbatim — the control plane writes these to be
   * tenant-safe and actionable, and rewriting them here would create a
   * second copy that drifts from what the checker actually found.
   */
  detail: string;
}

/**
 * Classify the wire's (resolved, detail) pair.
 *
 * A `detail` with `resolved` true would be a server contract violation;
 * it is read as `ok` and the detail dropped, because the boolean is the
 * conclusion and the string is only its explanation.
 *
 * BOTH ARGUMENTS ARE OPTIONAL ON THE WIRE, and that is not defensive
 * padding. proto3 omits a false bool and an empty string, and a control
 * plane predating these fields sends neither — so `undefined` is the
 * ordinary shape for every record read from an older server, not a
 * malformed one. It means exactly `unchecked`, which is already the safe
 * reading, so the fallbacks converge on the right answer rather than
 * masking a problem.
 */
export function dnsRecordCheckOf(
  resolved: boolean | undefined,
  detail: string | undefined
): DnsRecordCheck {
  if (resolved) return "ok";
  return (detail ?? "").trim() ? "failed" : "unchecked";
}

/** What a domain serves: one environment and target, or a redirect. */
export interface DomainBinding {
  id: string;
  domainId: string;
  environmentId: string;
  /** The workload or static-site NAME. Empty for a redirect binding. */
  target: string;
  /** The hostname a 308 points at. Empty for a normal binding. */
  redirectTo: string;
}

export interface ForgeDomain {
  id: string;
  hostname: string;
  state: DomainState;
  origin: DomainOrigin;
  /** The records to publish. Stays populated once live — removing them breaks it. */
  requiredRecords: DomainDnsRecord[];
  /** Why the last attempt failed, in the server's words. Empty on success. */
  lastError: string;
  /** When ownership was last PROVEN. Distinct from liveSince. */
  verifiedAt?: string;
  /** When it FIRST served. Never cleared by a later failure. */
  liveSince?: string;
  createdAt?: string;
  /** At most one — a hostname resolves to exactly one place. */
  binding: DomainBinding | null;
}

// ── State vocabulary ────────────────────────────────────────────────────────

export function domainStateOf(state: DeployCustomDomainState): DomainState {
  switch (state) {
    case DeployCustomDomainState.PENDING_DNS:
      return "pending-dns";
    case DeployCustomDomainState.VERIFYING:
      return "verifying";
    case DeployCustomDomainState.ISSUING:
      return "issuing";
    case DeployCustomDomainState.LIVE:
      return "live";
    case DeployCustomDomainState.FAILED:
      return "failed";
    case DeployCustomDomainState.CONFLICT:
      return "conflict";
    default:
      return "unknown";
  }
}

export function domainOriginOf(source: DomainSource): DomainOrigin {
  switch (source) {
    case DomainSource.EXTERNAL:
      return "external";
    case DomainSource.PLATFORM:
      return "platform";
    default:
      return "unknown";
  }
}

export const DOMAIN_STATE_LABELS: Record<DomainState, string> = {
  "pending-dns": "Waiting for DNS",
  verifying: "Checking ownership",
  issuing: "Issuing certificate",
  live: "Live",
  failed: "Failed",
  conflict: "Claimed by another organization",
  unknown: "Unknown",
};

/**
 * What is happening, in a sentence — the machine's half of the story.
 *
 * Present tense and specific about WHO is acting, because the difference
 * between "you have not done the thing yet" and "we are doing the thing" is
 * the whole question a tenant is asking when they open this screen.
 */
export const DOMAIN_STATE_EXPLANATIONS: Record<DomainState, string> = {
  "pending-dns":
    "This domain's DNS does not point at Reliant yet. Nothing converges until the records below resolve.",
  verifying:
    "The DNS resolves to Reliant, and we are confirming you own the domain by reading the TXT record.",
  issuing:
    "Ownership is confirmed. A TLS certificate is being obtained from Let's Encrypt.",
  live: "Serving over HTTPS with a valid certificate.",
  failed: "The last attempt did not succeed. This is retryable.",
  conflict:
    "Another organization proved ownership of this hostname first, so it is theirs. A hostname is claimed by whoever verifies it, not by whoever types it.",
  unknown:
    "This build does not recognise the state the control plane reported. Nothing is assumed about it.",
};

/**
 * What the TENANT should do — the human's half. Empty where there is nothing
 * to do, which is a real answer and better than inventing busywork.
 */
export const DOMAIN_STATE_NEXT_STEPS: Record<DomainState, string> = {
  "pending-dns":
    "Add these records at your DNS provider. We check automatically, every few minutes — you do not need to stay on this page.",
  verifying:
    "Nothing to do. Keep the records published; they are re-checked on every pass.",
  issuing: "Nothing to do. This usually finishes within a minute or two.",
  live: "Keep the records published. Removing them fails the next check and stops the domain serving.",
  failed: "Fix what the error describes, then check again.",
  conflict:
    "If the hostname is yours, the organization holding it must remove it — that frees the name immediately and you can check again. Contact support if you cannot reach them.",
  unknown: "",
};

/**
 * Whether the state can still change on its own.
 *
 * Drives the live refresh. `live` is treated as NON-terminal deliberately: a
 * domain that goes live can later fail its re-check (a deleted TXT record),
 * and a screen that stopped polling at `live` would keep saying so long after
 * it stopped being true. `conflict` is the genuinely settled one — it changes
 * only when another org acts.
 */
export function domainIsConverging(state: DomainState): boolean {
  return state === "pending-dns" || state === "verifying" || state === "issuing";
}

/** One line: where this domain points. */
export function bindingSummary(binding: DomainBinding | null): string {
  if (!binding) return "Not serving anything";
  if (binding.redirectTo) return `Redirects to ${binding.redirectTo}`;
  return binding.target;
}

// ── Bind targets ────────────────────────────────────────────────────────────

/** What a domain can be pointed at inside an environment. */
export type DomainTargetKind = "service" | "static-site";

export interface DomainTarget {
  /** The workload or static-site NAME — what a binding is written against. */
  name: string;
  kind: DomainTargetKind;
}

/**
 * domainTargetsOf narrows an environment's workloads to the ones a custom
 * domain can actually serve, so the picker never offers a dead end.
 *
 * A STATIC SITE always qualifies: the edge serves its bucket directly and
 * needs nothing from a deployment.
 *
 * A WORKLOAD qualifies only when it is exposed, and the evidence is its
 * platform URL. The control plane routes a custom domain by reusing the
 * coordinates of the workload's PLATFORM hostname (control-plane
 * internal/domaintargets/locator.go), and the operator allocates one only for
 * a workload with an exposed port. A worker or job has no URL and a database
 * does not speak HTTP — binding a domain to either would sit in "target not
 * ready" forever.
 *
 * The tier is read in both spellings: the control plane says `backend`, forge
 * says `workload`, and they name the same tier. An unreported tier with a URL
 * is still something that answers HTTP, so it is offered as a service.
 */
export function domainTargetsOf(workloads: readonly ForgeHostedWorkload[]): DomainTarget[] {
  const byName = new Map<string, DomainTarget>();
  for (const workload of workloads) {
    const name = (workload.name ?? "").trim();
    if (name === "" || byName.has(name)) continue;
    if ((workload.observed_state ?? "").trim().toLowerCase() === "deleted") continue;
    const tier = (workload.tier ?? "").trim().toLowerCase();
    if (tier === "static") {
      byName.set(name, { name, kind: "static-site" });
    } else if (
      (tier === "backend" || tier === "workload" || tier === "") &&
      workloadLink(workload) !== ""
    ) {
      byName.set(name, { name, kind: "service" });
    }
  }
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/** How a target's kind reads in a picker: "web — static site". */
export const DOMAIN_TARGET_KIND_LABELS: Record<DomainTargetKind, string> = {
  service: "service",
  "static-site": "static site",
};

// ── Availability ────────────────────────────────────────────────────────────

/**
 * Whether the control plane can answer about domains at all.
 *
 * Mirrors cloudEnvs.ts's CloudAvailability, and for the same reason: four of
 * these five are NOT errors, and painting a red banner in front of a user
 * running reliant without a control plane — or without the org role that may
 * write a public identity — would be describing a normal configuration as a
 * fault.
 *
 *   available         it answered.
 *   no-control-plane  this build has none (no VITE_CONTROL_PLANE_API_URL).
 *   no-access         PermissionDenied: domain reads need org membership, and
 *                     the write side needs org admin.
 *   not-configured    Unimplemented/Unavailable: this control plane does not
 *                     serve the domain registry (one without its database, or
 *                     one predating the service).
 *   unreachable       anything else. The only genuinely bad state.
 */
export type DomainAvailability =
  | "available"
  | "no-control-plane"
  | "no-access"
  | "not-configured"
  | "unreachable";

export function hasControlPlane(): boolean {
  return !!CONTROL_PLANE_API_URL;
}

export function domainAvailabilityFromError(err: unknown): DomainAvailability {
  if (!hasControlPlane()) return "no-control-plane";
  if (err instanceof ConnectError) {
    if (err.code === Code.PermissionDenied || err.code === Code.Unauthenticated) {
      return "no-access";
    }
    if (err.code === Code.Unimplemented || err.code === Code.Unavailable) {
      return "not-configured";
    }
  }
  return "unreachable";
}

export const DOMAIN_AVAILABILITY_EXPLANATIONS: Record<DomainAvailability, string> = {
  available: "",
  "no-control-plane":
    "This build of Reliant is not connected to a control plane, so it has no domain registry to read.",
  "no-access":
    "Your role in this organization may not read custom domains. Adding and removing them needs an organization admin.",
  "not-configured":
    "This control plane does not serve custom domains. It may predate the feature, or be running without its database.",
  unreachable: "The control plane could not be reached.",
};

/** A Connect error's own message, for the detail line under an `unreachable`. */
export function domainErrorDetail(err: unknown): string {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  return err instanceof Error ? err.message : "";
}

// ── Conversion ──────────────────────────────────────────────────────────────

function isoOf(ts: Timestamp | undefined): string | undefined {
  return ts ? timestampDate(ts).toISOString() : undefined;
}

function toRecord(msg: DeployDnsRecord): DomainDnsRecord {
  const check = dnsRecordCheckOf(msg.resolved, msg.detail);
  return {
    type: msg.type,
    name: msg.name,
    value: msg.value,
    check,
    // Only carried when it explains a failure, so a component cannot
    // accidentally render a stray detail beside a confirmed record. The
    // `?? ""` keeps this field a string for every caller — `failed` is
    // only reachable with a non-empty detail, so it never actually falls
    // back, but the type should not depend on that argument.
    detail: check === "failed" ? msg.detail ?? "" : "",
  };
}

function toBinding(msg: DomainBindingMessage | undefined): DomainBinding | null {
  if (!msg) return null;
  return {
    id: msg.id,
    domainId: msg.domainId,
    environmentId: msg.environmentId,
    target: msg.target,
    redirectTo: msg.redirectTo,
  };
}

export function toForgeDomain(msg: DomainMessage): ForgeDomain {
  return {
    id: msg.id,
    hostname: msg.hostname,
    state: domainStateOf(msg.state),
    origin: domainOriginOf(msg.source),
    requiredRecords: (msg.requiredRecords ?? []).map(toRecord),
    lastError: msg.lastError,
    verifiedAt: isoOf(msg.verifiedAt),
    liveSince: isoOf(msg.liveSince),
    createdAt: isoOf(msg.createdAt),
    binding: toBinding(msg.binding),
  };
}

// ── Calls ───────────────────────────────────────────────────────────────────

function client() {
  return getControlPlaneClient(DomainService);
}

/** Every domain the org holds, by hostname. The list is deliberately unpaginated server-side. */
export async function listDomains(): Promise<ForgeDomain[]> {
  const res = await client().listDomains({});
  return (res.domains ?? []).map(toForgeDomain);
}

export async function getDomain(domainId: string): Promise<ForgeDomain | null> {
  const res = await client().getDomain({ domainId });
  return res.domain ? toForgeDomain(res.domain) : null;
}

/**
 * Claim a hostname. Returns it in PENDING_DNS with its records populated —
 * those records are the whole of what the tenant does next.
 *
 * Claiming does NOT lock the name; uniqueness binds at verification.
 */
export async function createDomain(hostname: string): Promise<ForgeDomain> {
  const res = await client().createDomain({ hostname: hostname.trim().toLowerCase() });
  if (!res.domain) throw new Error(`The control plane returned no domain for ${hostname}.`);
  return toForgeDomain(res.domain);
}

export async function deleteDomain(domainId: string): Promise<void> {
  await client().deleteDomain({ domainId });
}

/**
 * Check DNS now rather than waiting for the poller. A NUDGE: the reconciler
 * polls regardless, so this changes only latency.
 */
export async function verifyDomain(domainId: string): Promise<ForgeDomain | null> {
  const res = await client().verifyDomain({ domainId });
  return res.domain ? toForgeDomain(res.domain) : null;
}

/**
 * Point a domain at an environment and a target, replacing any binding it
 * already has. Rebinding is this same call — there is no separate update.
 *
 * `target` and `redirectTo` are mutually exclusive; the server refuses both.
 */
export async function bindDomain(args: {
  domainId: string;
  environmentId: string;
  target?: string;
  redirectTo?: string;
}): Promise<DomainBinding | null> {
  const res = await client().createDomainBinding({
    domainId: args.domainId,
    environmentId: args.environmentId,
    target: args.target ?? "",
    redirectTo: args.redirectTo ?? "",
  });
  return toBinding(res.binding);
}

/**
 * Stop serving, and KEEP the domain. Its verification survives, so re-binding
 * later costs no DNS work and no new certificate.
 */
export async function unbindDomain(domainId: string): Promise<void> {
  await client().deleteDomainBinding({ bindingId: "", domainId });
}
