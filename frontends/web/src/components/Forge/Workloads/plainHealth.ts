// Copyright (c) 2025 Reliant Labs

/**
 * How a workload's trouble reads to someone who does not know they are on
 * Kubernetes. The server already speaks plain language (`healthReason`,
 * `plainReason`); this module decides WHICH words go where, in one place, so the
 * list, the detail page and the attention strip cannot word it differently.
 *
 * Three rules:
 *   1. The plain sentence is the headline. The raw Kubernetes word ("OOMKilled",
 *      "BackOff", "Unhealthy") lives behind a disclosure, never in a table cell.
 *   2. When the cause is the platform's, SAY SO. A user who is told "waiting for
 *      capacity" with no sign that it is ours goes looking for a fault of their own.
 *   3. A reason with no plain wording (a new Kubernetes event the server does not
 *      know yet) is shown as it is rather than hidden.
 */

import type { Workload, WorkloadCause, WorkloadEvent } from "@/services/controlPlane/observability/types";

/** What a verdict's trouble is called in the attention list: the subject comes first, this is what is wrong. */
export const PLATFORM_NOTE = "On us, not your code";

/** The headline for an event row: the plain wording when the server has one, else the raw reason. */
export function eventHeadline(event: Pick<WorkloadEvent, "reason" | "plainReason">): string {
  return event.plainReason || event.reason;
}

/**
 * The raw reason, only when it adds something the headline does not already say.
 * Null when the headline IS the raw reason (no plain wording), so a row never
 * shows the same word twice.
 */
export function eventRawReason(event: Pick<WorkloadEvent, "reason" | "plainReason">): string | null {
  return event.plainReason && event.plainReason !== event.reason ? event.reason : null;
}

/**
 * How a container's last exit reads. These are the container runtime's words, so
 * this is the console's half of the same table the server keeps (internal/
 * workloadhealth ExitPhrase); an exit it does not know reads as "stopped
 * unexpectedly" rather than as the raw word.
 */
export function exitPhrase(reason: string): string {
  switch (reason) {
    case "":
      return "";
    case "OOMKilled":
      return "Ran out of memory";
    case "Error":
      return "Exited with an error";
    case "Completed":
      return "Exited";
    case "ContainerCannotRun":
    case "StartError":
    case "CreateContainerError":
    case "CreateContainerConfigError":
    case "RunContainerError":
      return "Could not start";
    case "DeadlineExceeded":
      return "Ran past its time limit";
    case "Evicted":
      return "Moved to another machine";
    case "CrashLoopBackOff":
    case "BackOff":
      return "Keeps stopping";
    default:
      return "Stopped unexpectedly";
  }
}

/**
 * Why a Job run failed, in words. These are the Job controller's reasons; the
 * server keeps the same table for events (workloadhealth PlainReason).
 */
export function jobFailurePhrase(reason: string): string {
  switch (reason) {
    case "BackoffLimitExceeded":
      return "Failed after exhausting its retries";
    case "DeadlineExceeded":
      return "Ran past its time limit";
    case "PodFailurePolicy":
      return "Stopped by its failure policy";
    case "":
      return "Failed";
    default:
      return "Failed";
  }
}

const IPV4 = /\b(?:\d{1,3}\.){3}\d{1,3}\b/g;
const IPV6 = /\b(?:[0-9a-fA-F]{1,4}:){3,7}[0-9a-fA-F]{1,4}\b/g;
const NODE_NAME = /\b(?:gke|aks|eks)-[a-z0-9]+(?:-[a-z0-9]+){2,}\b|\bip-\d+-\d+-\d+-\d+(?:\.[a-z0-9.-]+)?\b/g;
const SERVICE_DNS = /\b[a-z0-9-]+\.[a-z0-9-]+\.svc(?:\.cluster\.local)?\b/g;
const CONTAINER_ID = /\b(?:containerd|docker|cri-o):\/\/[0-9a-f]{12,64}\b|\b[0-9a-f]{64}\b/g;
const TENANT_NAMESPACE = /\b(?:env|ws)-[0-9a-f]{8}(?:-[0-9a-f]{4}){0,3}(?:-[0-9a-f]{12})?\b/g;
const QUOTED_NODE = /\bnodes? "[^"]+"/g;

/**
 * Takes the Kubernetes internals out of free text before a user reads it: pod and
 * node addresses, node names, in-cluster DNS names, container ids and the
 * platform's namespace names. The server does this for everything it sends
 * (workloadhealth.Scrub); this is for text that reaches the console by another
 * road, such as a hosted workload's `last_error`, which the workload operator
 * writes. An address that was a URL host becomes `localhost`, because the probe
 * reached the container's own port.
 */
export function scrubInternals(text: string): string {
  if (!text) return text;
  return text
    .replace(QUOTED_NODE, "a node")
    .replace(CONTAINER_ID, "")
    .replace(NODE_NAME, "a node")
    .replace(SERVICE_DNS, "an internal service")
    .replace(TENANT_NAMESPACE, "this environment")
    .replace(IPV6, "localhost")
    .replace(IPV4, "localhost")
    .split(/\s+/)
    .filter(Boolean)
    .join(" ");
}

/**
 * A runtime error line in words: the container runtime's reason names are
 * replaced where they stand ("OOMKilled" reads "ran out of memory") and the
 * internals are scrubbed. Text with no known word passes through scrubbed.
 */
export function plainErrorText(text: string): string {
  return scrubInternals(text)
    .replace(/\bOOMKilled\b/g, "ran out of memory")
    .replace(/\bOOMKilling\b/g, "ran out of memory")
    .replace(/\bCrashLoopBackOff\b/g, "keeps restarting")
    .replace(/\bImagePullBackOff\b/g, "can't pull its image")
    .replace(/\bErrImagePull\b/g, "can't pull its image")
    .replace(/\bFailedScheduling\b/g, "waiting for capacity")
    .replace(/\bBackoffLimitExceeded\b/g, "failed after exhausting its retries")
    .replace(/\bContainerCannotRun\b/g, "could not start");
}

/** True when the fault is the platform's: the console must not imply the user caused it. */
export function isPlatformCause(cause: WorkloadCause): boolean {
  return cause === "platform";
}

/** The note shown beside a verdict, or null when there is nothing to add. */
export function causeNote(w: Pick<Workload, "health" | "healthCause">): string | null {
  if (w.health === "healthy" || w.health === "suspended") return null;
  return isPlatformCause(w.healthCause) ? PLATFORM_NOTE : null;
}

/**
 * The line a verdict shows: the plain reason, which already says "this is on us"
 * when it is, so the note is not repeated inside the sentence.
 */
export function healthLine(w: Pick<Workload, "healthReason">): string {
  return w.healthReason.trim();
}

/** The details disclosure's text, or null when there is none to disclose. */
export function healthDetailText(w: Pick<Workload, "healthDetail">): string | null {
  const d = w.healthDetail.trim();
  return d === "" ? null : d;
}

/**
 * Words a user must never be shown in a headline. Used by the tests as the
 * contract, and cheap enough to assert at runtime in dev.
 */
export const KUBERNETES_WORDS: readonly string[] = [
  "OOMKilled",
  "OOMKilling",
  "BackOff",
  "CrashLoopBackOff",
  "ImagePullBackOff",
  "ErrImagePull",
  "FailedScheduling",
  "Evicted",
  "Unhealthy",
  "kubelet",
  "namespace",
];

/** The Kubernetes words a piece of headline text still carries; empty when it is clean. */
export function leakedWords(text: string): string[] {
  return KUBERNETES_WORDS.filter((w) => text.includes(w));
}
