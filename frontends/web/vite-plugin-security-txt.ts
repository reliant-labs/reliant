import type { Plugin } from "vite";

/**
 * Emits `/.well-known/security.txt` (RFC 9116) into the build, so
 * app.reliantlabs.io says where to report a vulnerability and where to report
 * abuse. A real file in the bundle, like version.json, so the SPA fallback
 * cannot answer the request with index.html.
 *
 * The contacts are the ones reliantlabs.io (its AUP and security.txt) and the
 * preview domain (reliantpreview.dev) publish: security@ for vulnerabilities
 * in Reliant, abuse@ for what a hosted app or site does. It is generated, not
 * a file in public/, so Expires is counted from the build instead of being a
 * date someone has to remember to edit.
 */

/** Path of the emitted asset, relative to the bundle root. */
export const SECURITY_TXT_FILE = ".well-known/security.txt";

export const SECURITY_EMAIL = "security@reliantlabs.io";
export const ABUSE_EMAIL = "abuse@reliantlabs.io";

/** RFC 9116 asks for an Expires under a year away; every release rebuilds. */
const LIFETIME_DAYS = 180;

export function renderSecurityTxt(now: Date): string {
  const day = Date.UTC(now.getUTCFullYear(), now.getUTCMonth(), now.getUTCDate());
  const expires = new Date(day + LIFETIME_DAYS * 86_400_000).toISOString().replace(".000Z", "Z");
  return [
    `# Security vulnerabilities in Reliant: ${SECURITY_EMAIL}`,
    `Contact: mailto:${SECURITY_EMAIL}`,
    `# Abuse by an app or site Reliant hosts (phishing, malware, spam): ${ABUSE_EMAIL}`,
    `Contact: mailto:${ABUSE_EMAIL}`,
    `Expires: ${expires}`,
    "Preferred-Languages: en",
    `Canonical: https://app.reliantlabs.io/${SECURITY_TXT_FILE}`,
    "Policy: https://reliantlabs.io/acceptable-use#reporting-abuse",
    "",
  ].join("\n");
}

export function securityTxt(now: () => Date = () => new Date()): Plugin {
  return {
    name: "reliant:security-txt",
    apply: "build",
    generateBundle() {
      this.emitFile({ type: "asset", fileName: SECURITY_TXT_FILE, source: renderSecurityTxt(now()) });
    },
  };
}
