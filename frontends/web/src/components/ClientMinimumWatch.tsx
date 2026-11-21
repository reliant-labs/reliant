import { useEffect } from "react";
import { create } from "@bufbuild/protobuf";
import { SystemService, VersionRequestSchema } from "../gen/reliant/v1/system_pb";
import { noteUpgradeRequired } from "../api/versionSkew";
import { connectClient } from "../lib/connect";
import { clientIdentity } from "../lib/clientVersion";
import { watchClientMinimum } from "../lib/clientMinimum";

async function fetchServedMinimums() {
  const response = await connectClient(SystemService).version(create(VersionRequestSchema));
  return response.clientMinimums;
}

/**
 * Opens the forced upgrade prompt when this build is below the minimum the
 * reliant API publishes for it (lib/clientMinimum.ts), in a browser tab and in
 * the desktop app alike. Renders nothing. Mounted once, in RootShell.
 */
export function ClientMinimumWatch({
  fetchMinimums = fetchServedMinimums,
}: {
  fetchMinimums?: typeof fetchServedMinimums;
}) {
  useEffect(
    () =>
      watchClientMinimum({
        identity: clientIdentity(),
        fetchMinimums,
        onBelowMinimum: (minimum) =>
          noteUpgradeRequired("served-minimum", {
            minVersion: minimum.minVersion,
            upgradeUrl: minimum.upgradeUrl || undefined,
          }),
      }),
    [fetchMinimums],
  );
  return null;
}
