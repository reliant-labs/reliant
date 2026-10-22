/**
 * PKCE primitives shared by the provider OAuth flows (Claude, Codex).
 *
 * The S256 code challenge is a SHA-256 of the verifier, and SHA-256 in a
 * browser lives on `crypto.subtle` — which exists only in a SECURE CONTEXT.
 * An https:// page has one; so does http:// on localhost or 127.0.0.1. Plain
 * http:// on a LAN IP or a tunnel host does NOT, and there `crypto.subtle` is
 * `undefined`.
 *
 * Reading through it unguarded produced "Cannot read properties of undefined
 * (reading 'digest')" — a TypeError that names neither the cause nor the fix,
 * thrown out of the flow instead of returned as a result the UI can render.
 *
 * We deliberately do NOT ship a pure-JS SHA-256 fallback. It would make the
 * PKCE handshake itself work, but the flow would still be running on an origin
 * where every token it is about to obtain travels in clear text, and where the
 * browser withholds the rest of the secure-context APIs. Papering over that
 * would convert a loud, fixable misconfiguration into a silent one that hands
 * out credentials over plaintext. The remedy is to serve the app over HTTPS or
 * reach it on localhost, so that is what the error says.
 */

/** Raised when PKCE cannot run because the page is not a secure context. */
export class InsecureContextError extends Error {
  constructor() {
    super(
      "This page is not running in a secure context, so the browser does not " +
        "provide the SHA-256 needed to sign in. Open the app over HTTPS, or on " +
        "localhost, and try again.",
    );
    this.name = "InsecureContextError";
  }
}

export const base64UrlEncode = (bytes: Uint8Array): string => {
  let binary = "";
  for (const byte of bytes) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/g, "");
};

export const randomBytes = (length: number): Uint8Array => {
  const values = new Uint8Array(length);
  crypto.getRandomValues(values);
  return values;
};

export const generateCodeVerifier = (): string => base64UrlEncode(randomBytes(64));

/**
 * SHA-256 the verifier into an S256 code challenge.
 *
 * @throws {InsecureContextError} when `crypto.subtle` is unavailable.
 */
export const generateCodeChallenge = async (codeVerifier: string): Promise<string> => {
  if (typeof crypto === "undefined" || !crypto.subtle) {
    throw new InsecureContextError();
  }

  const encoded = new TextEncoder().encode(codeVerifier);
  const digest = await crypto.subtle.digest("SHA-256", encoded);
  return base64UrlEncode(new Uint8Array(digest));
};
