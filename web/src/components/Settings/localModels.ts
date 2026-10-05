import { DaemonStatus } from "../../gen/reliant/v1/daemon_registry_pb";
import type { DaemonInfo } from "../../gen/reliant/v1/daemon_registry_pb";
import type {
  LocalModelEndpoint,
  LocalModelInventory,
} from "../../gen/reliant/v1/tools_daemon_pb";

export const ENDPOINT_KIND_LABELS: Record<string, string> = {
  ollama: "Ollama",
  lmstudio: "LM Studio",
  llamacpp: "llama.cpp",
  vllm: "vLLM",
  openai_compatible: "OpenAI-compatible",
};

export const COMMON_ENDPOINT_URLS: Array<{ label: string; url: string }> = [
  { label: "Ollama", url: "http://localhost:11434/v1" },
  { label: "LM Studio", url: "http://localhost:1234/v1" },
  { label: "llama.cpp", url: "http://localhost:8080/v1" },
  { label: "vLLM", url: "http://localhost:8000/v1" },
];

/** The ModelSelector.providers entry meaning "this local model on that machine". */
export function localProviderRef(daemonId: string): string {
  return `local:${daemonId}`;
}

export function localGroupLabel(machineName: string): string {
  return `Local · ${machineName}`;
}

/** The ModelSelector.providers entry meaning "this model on that configured endpoint". */
export function endpointProviderRef(endpointId: string): string {
  return `endpoint:${endpointId}`;
}

/** The catalog's `local` source of a model; configured endpoints carry no daemon id. */
export interface LocalSource {
  daemonId: string;
  machineName: string;
  endpointId: string;
}

/** A model served by a user-configured endpoint, not one a daemon detected. */
export function isCustomEndpointModel(local: Pick<LocalSource, "daemonId">): boolean {
  return !local.daemonId;
}

/** The one provider string that pins a catalog model to where it is served. */
export function pinRefFor(local: LocalSource): string {
  return isCustomEndpointModel(local) ? endpointProviderRef(local.endpointId) : localProviderRef(local.daemonId);
}

/** Picker group label: a machine's local models, or a configured endpoint by its own name. */
export function localSourceGroupLabel(local: LocalSource): string {
  return isCustomEndpointModel(local) ? local.machineName : localGroupLabel(local.machineName);
}

/** The provider ref of a saved pin ("local:<id>" or "endpoint:<id>"), if any. */
export function pinnedRefOf(providers: string[] | undefined): string | undefined {
  return providers?.find((p) => p.startsWith("local:") || p.startsWith("endpoint:"));
}

export function endpointKindLabel(kind: string): string {
  return ENDPOINT_KIND_LABELS[kind] ?? (kind || "OpenAI-compatible");
}

/** A daemon can run local models only while its stream is attached. */
export function isDaemonOnline(daemon: Pick<DaemonInfo, "status">): boolean {
  return daemon.status === DaemonStatus.ACTIVE || daemon.status === DaemonStatus.IDLE;
}

export function isManagedDaemon(daemon: Pick<DaemonInfo, "daemonType">): boolean {
  return daemon.daemonType === "managed";
}

export function machineLabel(daemon: Pick<DaemonInfo, "hostname" | "daemonId">): string {
  return daemon.hostname || daemon.daemonId;
}

function hostOf(baseUrl: string): string {
  try {
    return new URL(baseUrl).host;
  } catch {
    return baseUrl;
  }
}

/** Turn the daemon's raw probe failure into a sentence a user can act on. */
export function describeEndpointError(endpoint: Pick<LocalModelEndpoint, "error" | "kind" | "baseUrl">): string {
  const raw = endpoint.error.trim();
  if (!raw) return "";
  const host = hostOf(endpoint.baseUrl);
  const kind = endpointKindLabel(endpoint.kind);
  const lower = raw.toLowerCase();
  if (lower.includes("refused") || lower.includes("no such host") || lower.includes("unreachable")) {
    return endpoint.kind && endpoint.kind !== "openai_compatible"
      ? `Nothing is listening on ${host} — is ${kind} running?`
      : `Nothing is listening on ${host} — is the model server running?`;
  }
  if (lower.includes("timeout") || lower.includes("deadline")) {
    return `${host} didn't answer in time. The server may be busy or blocked by a firewall.`;
  }
  const status = raw.match(/\b(401|403)\b/);
  if (status) return `${host} rejected the request (HTTP ${status[1]}). It may require an API key.`;
  if (/\b404\b/.test(raw)) return `${host} responded, but it doesn't look like an OpenAI-compatible server. Check the URL ends in /v1.`;
  return raw;
}

export type UrlValidation = { ok: true; url: string } | { ok: false; message: string };

export function validateEndpointUrl(input: string, existing: string[]): UrlValidation {
  const trimmed = input.trim().replace(/\/+$/, "");
  if (!trimmed) return { ok: false, message: "Enter the server's base URL." };
  let parsed: URL;
  try {
    parsed = new URL(trimmed);
  } catch {
    return { ok: false, message: "That doesn't look like a URL. Try http://localhost:11434/v1." };
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return { ok: false, message: "The URL must start with http:// or https://." };
  }
  if (existing.some((u) => u.replace(/\/+$/, "") === trimmed)) {
    return { ok: false, message: "That endpoint is already configured." };
  }
  return { ok: true, url: trimmed };
}

/** The user-configured URLs — the only list SetLocalModelEndpoints replaces. */
export function configuredUrls(inventory: LocalModelInventory | undefined): string[] {
  return (inventory?.endpoints ?? []).filter((e) => e.source === "configured").map((e) => e.baseUrl);
}

/** Prefer whichever inventory was probed most recently (RPC response vs polled list). */
export function newerInventory(
  a: LocalModelInventory | undefined,
  b: LocalModelInventory | undefined,
): LocalModelInventory | undefined {
  if (!a) return b;
  if (!b) return a;
  return Date.parse(b.probedAt || "") > Date.parse(a.probedAt || "") ? b : a;
}

/** Ollama documents ~64000 as the minimum for agents and coding tools. */
export const AGENT_CONTEXT_MIN = 64000;
export const CONTEXT_TOO_SMALL = 16000;

export type ContextSeverity = "none" | "soft" | "warning";

/** 0 means the context is unknown, which is not a reason to warn. */
export function contextSeverity(tokens: number | bigint): ContextSeverity {
  const n = Number(tokens);
  if (!n || n >= AGENT_CONTEXT_MIN) return "none";
  return n < CONTEXT_TOO_SMALL ? "warning" : "soft";
}

export function contextWarningText(tokens: number | bigint): string {
  const sev = contextSeverity(tokens);
  if (sev === "none") return "";
  const size = formatContextWindow(BigInt(Math.trunc(Number(tokens)))).replace(" ctx", "");
  return sev === "warning"
    ? `${size} context — too small for agent work`
    : `${size} context — smaller than the ~64K agents work best with`;
}

export function formatContextWindow(tokens: bigint): string {
  const n = Number(tokens);
  if (!n) return "";
  if (n >= 1_000_000) return `${+(n / 1_000_000).toFixed(1)}M ctx`;
  if (n >= 1000) return `${Math.round(n / 1024)}K ctx`;
  return `${n} ctx`;
}

export function formatProbedAgo(probedAt: string, now: number = Date.now()): string {
  const t = Date.parse(probedAt);
  if (!t) return "never";
  const seconds = Math.max(0, Math.round((now - t) / 1000));
  if (seconds < 10) return "just now";
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.round(hours / 24)}d ago`;
}
