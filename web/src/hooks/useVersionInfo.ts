import { useEffect, useState } from "react";

import { systemGrpc } from "../api/system-grpc";
import { appBuild } from "../lib/appBuild";
import { formatVersion } from "../lib/releaseIdentity";

/**
 * Everything the About screens say about which build the user is running.
 *
 * Three facts, from three places, because they are three different things:
 *  - `app`: THIS bundle, baked in at build time. Always available in a shipped
 *    build (the build refuses to ship without it), so the screen never has to
 *    say "version unknown" about the code it is drawn by.
 *  - `server`: the reliant API this app talks to, from its Version RPC.
 *  - `release`: the forge release that shipped the bundle (hosted web only).
 */
export interface VersionInfo {
  app: string | null;
  server: ServerVersion;
  release: string | null;
}

export type ServerVersion =
  | { state: "loading" }
  | { state: "unreachable" }
  | { state: "known"; version: string }
  // The server answered and could not name itself (an unstamped binary).
  | { state: "unknown" };

export function useVersionInfo(): VersionInfo {
  const build = appBuild();
  const baked = formatVersion(build?.version, build?.commit);
  const [desktopVersion, setDesktopVersion] = useState<string | null>(null);
  const [server, setServer] = useState<ServerVersion>({ state: "loading" });

  useEffect(() => {
    let cancelled = false;
    systemGrpc
      .version()
      .then((data) => {
        if (cancelled) return;
        const version = formatVersion(data.version, data.commit);
        setServer(version ? { state: "known", version } : { state: "unknown" });
      })
      .catch((error) => {
        console.error("Failed to fetch server version:", error);
        if (!cancelled) setServer({ state: "unreachable" });
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // The packaged desktop app also knows its own version from Electron. The
  // baked identity is the same build's, so it wins; this only covers a bundle
  // built without one.
  useEffect(() => {
    if (baked || !window.electronAPI?.getVersion) return;
    let cancelled = false;
    window.electronAPI
      .getVersion()
      .then((v) => {
        if (!cancelled) setDesktopVersion(formatVersion(v));
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [baked]);

  return {
    app: baked ?? desktopVersion,
    server,
    release: build?.release || null,
  };
}

/** One-line text for the server's version, for the About screens. */
export function serverVersionLabel(server: ServerVersion): string {
  switch (server.state) {
    case "loading":
      return "…";
    case "unreachable":
      return "unreachable";
    case "unknown":
      return "unknown";
    case "known":
      return server.version;
  }
}
