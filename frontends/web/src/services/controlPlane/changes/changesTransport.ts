// Copyright (c) 2025 Reliant Labs

/**
 * The one unary call to controlplane.v1.ChangesService, in the Connect
 * protocol's JSON form, as the signed-in person: the stand-in for a
 * generated client until changes.proto is vendored (changesWire.ts says
 * why). Its demo twin (changesTransport.demo.ts) answers every call
 * "not available": the public demo reaches no backend.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import { getAuthTokenProvider } from "@/api/authProvider";
import { controlPlaneBaseUrl } from "@/lib/connect";

export const CHANGES_SERVICE = "controlplane.v1.ChangesService";

/** One unary call: the protojson request in, the protojson response out. Tests swap it (changesApi.ts). */
export type ChangesCall = (method: string, body: Record<string, unknown>, signal?: AbortSignal) => Promise<unknown>;

const CODES: Record<string, Code> = {
  canceled: Code.Canceled,
  unknown: Code.Unknown,
  invalid_argument: Code.InvalidArgument,
  deadline_exceeded: Code.DeadlineExceeded,
  not_found: Code.NotFound,
  already_exists: Code.AlreadyExists,
  permission_denied: Code.PermissionDenied,
  resource_exhausted: Code.ResourceExhausted,
  failed_precondition: Code.FailedPrecondition,
  aborted: Code.Aborted,
  out_of_range: Code.OutOfRange,
  unimplemented: Code.Unimplemented,
  internal: Code.Internal,
  unavailable: Code.Unavailable,
  data_loss: Code.DataLoss,
  unauthenticated: Code.Unauthenticated,
};

/** A Connect error response as a ConnectError. A 404 with no Connect body is a server without the service. */
export function connectErrorOf(status: number, headers: Headers, body: unknown): ConnectError {
  const json = (body && typeof body === "object" ? body : {}) as { code?: unknown; message?: unknown };
  const named = typeof json.code === "string" ? CODES[json.code] : undefined;
  const code = named ?? (status === 404 ? Code.Unimplemented : status === 401 ? Code.Unauthenticated : status === 403 ? Code.PermissionDenied : Code.Unavailable);
  const message = typeof json.message === "string" ? json.message : `HTTP ${status}`;
  return new ConnectError(message, code, headers);
}

/** The live call: Connect unary JSON to platform-api, as the signed-in person. */
export const callChanges: ChangesCall = async (method, body, signal) => {
  const base = controlPlaneBaseUrl();
  if (!base) throw new ConnectError("Control plane API URL not configured (CONTROL_PLANE_API_URL)", Code.Unavailable);
  const token = await getAuthTokenProvider().getToken();
  const headers: Record<string, string> = { "Content-Type": "application/json", "Connect-Protocol-Version": "1" };
  if (token) headers.Authorization = `Bearer ${token}`;
  let response: Response;
  try {
    response = await fetch(`${base.replace(/\/$/, "")}/${CHANGES_SERVICE}/${method}`, { method: "POST", headers, body: JSON.stringify(body), signal });
  } catch (err) {
    if (err instanceof DOMException && err.name === "AbortError") throw new ConnectError("request aborted", Code.Canceled);
    throw new ConnectError(err instanceof Error ? err.message : "network error", Code.Unavailable);
  }
  const text = await response.text();
  let json: unknown = null;
  try {
    json = text ? JSON.parse(text) : {};
  } catch {
    json = null;
  }
  if (!response.ok) throw connectErrorOf(response.status, response.headers, json);
  return json ?? {};
};
