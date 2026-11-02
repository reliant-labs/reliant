/**
 * Dev URLs and the cloud daemon's preview URLs.
 *
 * A dev server started by an agent listens on the DAEMON's loopback. With a
 * local daemon that is this machine's `localhost`; with a cloud daemon it is
 * the workspace pod's, which this machine reaches only through the
 * workspace-proxy → in-pod preview forwarder at the daemon's preview URL. The
 * control plane hands that URL out as a template with a `{port}` placeholder
 * (`GetDaemon.workspaceBaseDomain`): `https://{port}-<daemonId>.<baseDomain>`
 * in the cloud, `http://localhost:8080/proxy/<daemonId>/{port}/` in path-mode
 * dev. Everything here is pure; the template comes from
 * useActiveDaemonPreviewTemplate.
 */

/** Whether hostname (as `URL.hostname` spells it) is this machine's loopback. */
export function isLoopbackHostname(hostname: string): boolean {
  const host = hostname.toLowerCase();
  return (
    host === "localhost" ||
    host.endsWith(".localhost") ||
    host === "0.0.0.0" ||
    host === "[::1]" ||
    /^127(\.\d{1,3}){3}$/.test(host)
  );
}

function parseHttpUrl(url: string): URL | null {
  try {
    const parsed = new URL(url);
    return parsed.protocol === "http:" || parsed.protocol === "https:" ? parsed : null;
  } catch {
    return null;
  }
}

/** The preview URL of port on the daemon the template belongs to. */
export function previewUrlForPort(template: string, port: number): string {
  return template.replace("{port}", String(port));
}

/**
 * Where the browser reaches a dev server listening on the daemon's port: the
 * daemon's preview URL with a cloud daemon's template, plain localhost without.
 */
export function devServerUrl(port: number, template: string | undefined): string {
  return template && template.includes("{port}") ? previewUrlForPort(template, port) : `http://localhost:${port}`;
}

/**
 * Whether a browser tab at tabUrl is already showing the dev server at
 * serverUrl (any page of it): tabUrl is serverUrl or below it. The webview
 * reports `http://localhost:5173/` for `http://localhost:5173`, and the user
 * may have navigated within the app since, so this is not string equality.
 */
export function isSameDevPage(tabUrl: string, serverUrl: string): boolean {
  const tab = parseHttpUrl(tabUrl);
  const server = parseHttpUrl(serverUrl);
  if (!tab || !server) return false;
  const base = server.href.replace(/\/+$/, "");
  return tab.href === base || tab.href.startsWith(`${base}/`) || tab.href.startsWith(`${base}?`) || tab.href.startsWith(`${base}#`);
}

/**
 * Whether url is a preview URL of the template's daemon. Matched as a prefix
 * that ends at a path/query/fragment boundary, so a look-alike host
 * (`https://3000-<id>.<baseDomain>.evil.test`) is not one.
 */
export function isPreviewUrl(url: string, template: string | undefined): boolean {
  if (!template || !template.includes("{port}")) return false;
  const parsed = parseHttpUrl(url);
  if (!parsed) return false;
  const [before, after] = template.split("{port}", 2).map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"));
  const boundary = template.endsWith("/") ? "" : "(?=$|[/?#])";
  return new RegExp(`^${before}\\d+${after}${boundary}`, "i").test(parsed.href);
}

/**
 * A dev URL is one the app under development serves: a loopback URL, or a
 * preview URL of the active daemon. Dev URLs open in the embedded browser;
 * everything else belongs in the user's default browser, with their logins.
 */
export function isDevUrl(url: string, template: string | undefined): boolean {
  const parsed = parseHttpUrl(url);
  if (!parsed) return false;
  return isLoopbackHostname(parsed.hostname) || isPreviewUrl(url, template);
}

/**
 * Maps a loopback URL onto the daemon's preview URL for the same port, keeping
 * path, query and fragment. Returns null when there is nothing to map: no
 * template (a local daemon, whose loopback IS this machine's), a URL that is
 * already a preview URL (path-mode dev previews live on localhost too), a
 * non-loopback URL, or no explicit port.
 */
export function toPreviewUrl(url: string, template: string | undefined): string | null {
  if (!template || !template.includes("{port}")) return null;
  if (isPreviewUrl(url, template)) return null;
  const parsed = parseHttpUrl(url);
  if (!parsed || !isLoopbackHostname(parsed.hostname) || !parsed.port) return null;
  const base = previewUrlForPort(template, Number(parsed.port)).replace(/\/+$/, "");
  return `${base}${parsed.pathname}${parsed.search}${parsed.hash}`;
}
