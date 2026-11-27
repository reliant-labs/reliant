/**
 * The import-free half of device pairing (pairing.ts): a pairing code's shape
 * and the error a lookup or an answer throws. Its own module so the public
 * demo's twin of pairing.ts (pairing.demo.ts), which has no session to pair
 * with, formats codes and reports errors exactly as the app does.
 */

export class PairingError extends Error {
  /** 404: nothing is waiting with that code. */
  readonly notFound: boolean;

  constructor(message: string, notFound = false) {
    super(message);
    this.name = "PairingError";
    this.notFound = notFound;
  }
}

/** The 8 letters of a code, whatever the person typed around them. */
export function normalizePairingCode(typed: string): string {
  return typed.toUpperCase().replace(/[^A-Z]/g, "");
}

/** "bdfghjkl" → "BDFG-HJKL"; shorter input is returned normalized. */
export function formatPairingCode(typed: string): string {
  const code = normalizePairingCode(typed);
  return code.length === 8 ? `${code.slice(0, 4)}-${code.slice(4)}` : code;
}
