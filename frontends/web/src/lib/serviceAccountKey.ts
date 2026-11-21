// Copyright (c) 2025 Reliant Labs

/**
 * A client-side sanity check of a pasted Google Cloud service-account key, the
 * credential a Vertex AI account connects with.
 *
 * The server is the authority: it parses the key, refuses anything that is not
 * a `service_account` key, and makes a Vertex call with it before it stores
 * anything. This check only catches the paste that cannot work (half a file, a
 * gcloud login, a workload-identity config) before a round trip, with a message
 * that says what to paste instead.
 *
 * The messages never quote the key: a service-account key holds a private key,
 * so nothing here echoes any part of what was pasted.
 */

/**
 * The server's own sentence for a refused Vertex AI key. The server names the
 * fault (wrong credential type, no project, a missing role), so the message is
 * shown as it is, without the Connect code prefix ("[invalid_argument] ").
 */
export function vertexErrorMessage(errorText: string): string {
  const message = errorText.replace(/^\[[a-z_]+\]\s*/, "").trim();
  return (
    message ||
    'Vertex AI did not accept that key. Check that the service account has the "Vertex AI User" role.'
  );
}

/** The fields a service-account key must carry for the server to use it. */
const REQUIRED_FIELDS = ["project_id", "client_email", "private_key"] as const;

/** What other Google credential files are, in words, so the error can say which one was pasted. */
const OTHER_CREDENTIAL_TYPES: Record<string, string> = {
  authorized_user: "a gcloud user login",
  external_account: "a workload identity federation config",
  impersonated_service_account: "an impersonation config",
};

/**
 * Why `text` cannot be a service-account key, or null when it looks like one.
 * Checks that it parses as a JSON object of `"type": "service_account"` with a
 * non-empty `project_id`, `client_email` and `private_key`.
 */
export function serviceAccountKeyError(text: string): string | null {
  const trimmed = text.trim();
  if (!trimmed) return "Paste the contents of the service-account key file.";

  let parsed: unknown;
  try {
    parsed = JSON.parse(trimmed);
  } catch {
    return "That is not valid JSON. Paste the whole key file, from the first { to the last }.";
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    return "That is not a key file. Paste the whole JSON object the key file holds.";
  }

  const key = parsed as Record<string, unknown>;
  if (key.type !== "service_account") {
    const other = typeof key.type === "string" ? OTHER_CREDENTIAL_TYPES[key.type] : undefined;
    const what = other ? `That is ${other}, not a service-account key.` : "That is not a service-account key.";
    return `${what} Vertex AI needs a key file whose "type" is "service_account".`;
  }

  const missing = REQUIRED_FIELDS.filter((f) => typeof key[f] !== "string" || (key[f] as string).trim() === "");
  if (missing.length > 0) {
    return `The key file has no ${missing.join(", ")}. Download a new JSON key and paste the whole file.`;
  }
  return null;
}
