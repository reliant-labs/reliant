import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { requestKey } from "@reliantlabs/forge-web-runtime/service-hooks";

import { SystemService, VersionRequestSchema } from "@/gen/reliant/v1/system_pb";
import { systemServiceKeys } from "@/hooks/system-service-hooks_gen";
import { connectClient } from "@/lib/connect";
import { appBuild } from "../lib/appBuild";
import { formatVersion } from "../lib/releaseIdentity";

const systemClient = connectClient(SystemService);

/**
 * The server's Version RPC, as a query.
 *
 * forge generates no query hook for it: it classifies a read by its rpc's
 * NAME (Get/List/Search…), and `Version` is not a read verb, so
 * system-service-hooks_gen.ts offers only `useVersion` as a MUTATION. Until
 * forge also reads a NO_SIDE_EFFECTS idempotency level, this key is built
 * exactly the way the generated factory would build it —
 * `[service, method, protojson(req)]` under `systemServiceKeys.all` — so an
 * invalidation of the service reaches it, and the day forge emits
 * `systemServiceKeys.version` this becomes `useVersion({})` with the same
 * cache entry.
 */
const versionQueryKey = [
  ...systemServiceKeys.all,
  "version",
  requestKey(VersionRequestSchema, {}),
] as const;

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
  const versionQuery = useQuery({
    queryKey: versionQueryKey,
    queryFn: () => systemClient.version({}),
  });
  const server = serverVersionFrom(versionQuery);

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

function serverVersionFrom(query: {
  data?: { version: string; commit: string };
  isError: boolean;
}): ServerVersion {
  if (query.data) {
    const version = formatVersion(query.data.version, query.data.commit);
    return version ? { state: "known", version } : { state: "unknown" };
  }
  return query.isError ? { state: "unreachable" } : { state: "loading" };
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
