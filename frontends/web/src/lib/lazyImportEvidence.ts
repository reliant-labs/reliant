// Copyright (c) 2025 Reliant Labs

/** Import errors may be missing chunks, gateway failures or CSP/network errors—not proof of a deploy. */
export interface FailedImportResource { path: string; status: number }
/**
 * Keeps a path segment only when it looks like a route or static asset name; anything else
 * (percent-encoded data, long or digit-bearing token runs as in signed storage URLs) is redacted.
 */
export function safeImportPath(pathname: string): string {
  return pathname.split("/").map((segment) =>
    segment === "" || (segment.length <= 64 && /^[A-Za-z0-9._-]+$/.test(segment) && !(segment.match(/[A-Za-z0-9]{16,}/g) ?? []).some((run) => /\d/.test(run)))
      ? segment : "[redacted]").join("/");
}
export function redactImportUrls(text: string): string {
  return text.replace(/https?:\/\/[^\s)\]"'<>]+/g, (value) => {
    try { const url = new URL(value); return `${url.origin}${safeImportPath(url.pathname)}`; } catch { return "[invalid URL]"; }
  });
}
export function failedImportResources(entries: readonly PerformanceEntry[]): FailedImportResource[] {
  const failures = new Map<string, FailedImportResource>();
  for (const entry of entries) {
    const status = (entry as PerformanceResourceTiming & { responseStatus?: number }).responseStatus;
    if (!status || status < 400) continue; // Older Safari does not expose status; do not invent one.
    try {
      const url = new URL(entry.name);
      if (!/\.(?:m?js|css)$/.test(url.pathname)) continue;
      const path = safeImportPath(url.pathname);
      failures.set(path, { path, status });
    } catch { /* Not a resource URL. */ }
  }
  return [...failures.values()].slice(-20);
}
export function importFailureDetails(error: unknown, entries: readonly PerformanceEntry[] = []): string {
  const lines: string[] = [];
  const seen = new Set<unknown>();
  let current = error;
  for (let depth = 0; current instanceof Error && depth < 5 && !seen.has(current); depth++) {
    seen.add(current);
    lines.push(redactImportUrls(`${current.name}: ${current.message}`));
    current = current.cause;
  }
  for (const failed of failedImportResources(entries)) lines.push(`Resource HTTP ${failed.status}: ${failed.path}`);
  return lines.join("\n");
}
