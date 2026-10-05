import { create } from "@bufbuild/protobuf";
import {
  ModelEndpointInputSchema,
  ModelEndpointModelSchema,
  ModelEndpointRoute,
} from "../../gen/reliant/v1/model_endpoint_pb";
import type { ModelEndpoint, ModelEndpointInput, ModelEndpointModel } from "../../gen/reliant/v1/model_endpoint_pb";

/** The message shown while the sealed credential store is not wired in. */
export const CREDENTIAL_STORE_NOTE =
  "API keys and custom headers need Reliant's sealed credential store. Coming with the next update; endpoints without a key work today.";

/** Keys the server controls; extra JSON may not set them. */
export const RESERVED_BODY_KEYS = ["model", "messages", "tools", "stream"] as const;

export interface ModelDraft {
  name: string;
  hidden: boolean;
  /** "" means "use what the server reported". */
  contextWindow: string;
  maxOutputTokens: string;
  /** "auto" defers to the probe; "on"/"off" is an explicit override. */
  tools: Tri;
  vision: Tri;
  thinking: Tri;
  temperature: string;
  topP: string;
  extraBodyJson: string;
}

export type Tri = "auto" | "on" | "off";

export interface EndpointDraft {
  name: string;
  baseUrl: string;
  route: "direct" | "via_daemon";
  daemonId: string;
  apiKey: string;
  models: ModelDraft[];
}

export function emptyDraft(): EndpointDraft {
  return { name: "", baseUrl: "", route: "direct", daemonId: "", apiKey: "", models: [] };
}

export function emptyModelDraft(name: string): ModelDraft {
  return {
    name, hidden: false, contextWindow: "", maxOutputTokens: "",
    tools: "auto", vision: "auto", thinking: "auto", temperature: "", topP: "", extraBodyJson: "",
  };
}

const tri = (v: boolean | undefined): Tri => (v === undefined ? "auto" : v ? "on" : "off");
const fromTri = (v: Tri): boolean | undefined => (v === "auto" ? undefined : v === "on");

export function modelToDraft(m: ModelEndpointModel): ModelDraft {
  return {
    name: m.name,
    hidden: m.hidden,
    contextWindow: m.contextWindow ? String(m.contextWindow) : "",
    maxOutputTokens: m.maxOutputTokens ? String(m.maxOutputTokens) : "",
    tools: tri(m.supportsTools),
    vision: tri(m.supportsVision),
    thinking: tri(m.supportsThinking),
    temperature: m.temperature === undefined ? "" : String(m.temperature),
    topP: m.topP === undefined ? "" : String(m.topP),
    extraBodyJson: m.extraBodyJson,
  };
}

export function endpointToDraft(e: ModelEndpoint): EndpointDraft {
  return {
    name: e.name,
    baseUrl: e.baseUrl,
    route: e.route === ModelEndpointRoute.VIA_DAEMON ? "via_daemon" : "direct",
    daemonId: e.daemonId,
    apiKey: "",
    models: e.models.map(modelToDraft),
  };
}

/** null when valid, otherwise the sentence to show next to the field. */
export function validateExtraBodyJson(raw: string): string | null {
  const text = raw.trim();
  if (!text) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch {
    return "That isn't valid JSON.";
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return 'Must be a JSON object, e.g. {"min_p": 0.05}.';
  }
  for (const key of RESERVED_BODY_KEYS) {
    if (key in (parsed as Record<string, unknown>)) {
      return `"${key}" is controlled by Reliant and can't be set here.`;
    }
  }
  return null;
}

function numberError(label: string, raw: string, ok: (n: number) => boolean, hint: string): string | null {
  if (!raw.trim()) return null;
  const n = Number(raw);
  if (!Number.isFinite(n) || !ok(n)) return `${label} ${hint}`;
  return null;
}

export interface ModelErrors {
  contextWindow?: string;
  maxOutputTokens?: string;
  temperature?: string;
  topP?: string;
  extraBodyJson?: string;
}

export function validateModelDraft(m: ModelDraft): ModelErrors {
  const errors: ModelErrors = {};
  const int = (n: number) => Number.isInteger(n) && n >= 0;
  const ctx = numberError("Context window", m.contextWindow, int, "must be a whole number of tokens.");
  if (ctx) errors.contextWindow = ctx;
  const out = numberError("Max output", m.maxOutputTokens, int, "must be a whole number of tokens.");
  if (out) errors.maxOutputTokens = out;
  if (!errors.contextWindow && !errors.maxOutputTokens && m.contextWindow.trim() && m.maxOutputTokens.trim()) {
    if (Number(m.maxOutputTokens) > Number(m.contextWindow)) errors.maxOutputTokens = "Max output can't exceed the context window.";
  }
  const temp = numberError("Temperature", m.temperature, (n) => n >= 0 && n <= 2, "must be between 0 and 2.");
  if (temp) errors.temperature = temp;
  const topP = numberError("Top-p", m.topP, (n) => n > 0 && n <= 1, "must be above 0 and at most 1.");
  if (topP) errors.topP = topP;
  const extra = validateExtraBodyJson(m.extraBodyJson);
  if (extra) errors.extraBodyJson = extra;
  return errors;
}

export function hasModelErrors(e: ModelErrors): boolean {
  return Object.keys(e).length > 0;
}

export type DraftValidation = { ok: true } | { ok: false; field: "name" | "baseUrl" | "daemonId" | "models"; message: string };

export function validateDraft(d: EndpointDraft): DraftValidation {
  if (!d.name.trim()) return { ok: false, field: "name", message: "Give the endpoint a name." };
  const url = d.baseUrl.trim();
  if (!url) return { ok: false, field: "baseUrl", message: "Enter the server's base URL." };
  let parsed: URL;
  try {
    parsed = new URL(url);
  } catch {
    return { ok: false, field: "baseUrl", message: "That doesn't look like a URL. Try https://llm.example.com/v1." };
  }
  if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
    return { ok: false, field: "baseUrl", message: "The URL must start with http:// or https://." };
  }
  if (parsed.username || parsed.password) {
    return { ok: false, field: "baseUrl", message: "Don't put a username or password in the URL." };
  }
  if (d.route === "via_daemon" && !d.daemonId) {
    return { ok: false, field: "daemonId", message: "Choose which of your machines should reach this server." };
  }
  for (const m of d.models) {
    if (hasModelErrors(validateModelDraft(m))) {
      return { ok: false, field: "models", message: `Fix the settings for ${m.name}.` };
    }
  }
  return { ok: true };
}

const toBig = (raw: string): bigint => (raw.trim() ? BigInt(Math.trunc(Number(raw))) : 0n);
const toNum = (raw: string): number | undefined => (raw.trim() ? Number(raw) : undefined);

/**
 * Only models the user changed are sent: a model left at its defaults is
 * recomputed from the probe on every refresh, so persisting it would freeze
 * today's discovered capabilities.
 */
function isCustomised(m: ModelDraft): boolean {
  return (
    m.hidden || !!m.contextWindow.trim() || !!m.maxOutputTokens.trim() || m.tools !== "auto" || m.vision !== "auto" ||
    m.thinking !== "auto" || !!m.temperature.trim() || !!m.topP.trim() || !!m.extraBodyJson.trim()
  );
}

export function draftToInput(d: EndpointDraft, opts: { includeKey: boolean }): ModelEndpointInput {
  return create(ModelEndpointInputSchema, {
    name: d.name.trim(),
    baseUrl: d.baseUrl.trim(),
    route: d.route === "via_daemon" ? ModelEndpointRoute.VIA_DAEMON : ModelEndpointRoute.DIRECT,
    daemonId: d.route === "via_daemon" ? d.daemonId : "",
    // Unset on an update keeps the stored key; the UI never sends one while
    // the sealed store is unavailable.
    apiKey: opts.includeKey && d.apiKey ? d.apiKey : undefined,
    models: d.models.filter(isCustomised).map((m) =>
      create(ModelEndpointModelSchema, {
        name: m.name,
        hidden: m.hidden,
        contextWindow: toBig(m.contextWindow),
        maxOutputTokens: toBig(m.maxOutputTokens),
        supportsTools: fromTri(m.tools),
        supportsVision: fromTri(m.vision),
        supportsThinking: fromTri(m.thinking),
        temperature: toNum(m.temperature),
        topP: toNum(m.topP),
        extraBodyJson: m.extraBodyJson.trim(),
      }),
    ),
  });
}

/** Merge the models a probe found into a draft without losing the user's edits. */
export function mergeProbeIntoDraft(models: ModelDraft[], discovered: string[]): ModelDraft[] {
  const have = new Set(models.map((m) => m.name));
  const added = discovered.filter((n) => !have.has(n)).map(emptyModelDraft);
  return [...models, ...added].sort((a, b) => a.name.localeCompare(b.name));
}

export function formatLatency(ms: bigint | number): string {
  const n = Number(ms);
  if (n < 1000) return `${n} ms`;
  return `${(n / 1000).toFixed(1)} s`;
}
