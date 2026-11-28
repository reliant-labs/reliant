// Copyright (c) 2025 Reliant Labs

/**
 * DaemonRegistryService: Alex's cloud machine (up, on the v1.31.0 build) and
 * laptop (asleep). The control plane's DaemonService adds the cloud machine's
 * spec (size M, 50 GiB persistent disk, as billing.ts bills it); the laptop
 * is self-hosted, so the control plane has no spec for it.
 */
import { Code, ConnectError } from "@connectrpc/connect";

import { DaemonDiskMode, DaemonLifecyclePhase, DaemonSize, DaemonStatus, DaemonType, OwnerType, PortAccessMode } from "@/gen/controlplane/controlplane/v1/shared_pb";
import { DaemonService } from "@/gen/controlplane/services/daemon/v1/daemon_pb";
import { DaemonRegistryService } from "@/gen/reliant/v1/daemon_registry_pb";

import { DAY, DEMO_ORG, HOUR } from "../../shopStory";
import { APP_USER, DAEMON_IDS, DAEMON_NAMES, daemons, tsAgo } from "../../workspace";
import { serve } from "../serve";

/** The cloud machine as the control plane provisioned it. */
const cloudSpec = () => ({
  id: DAEMON_IDS.cloud, name: DAEMON_NAMES.cloud, ownerId: APP_USER.id, ownerType: OwnerType.USER, daemonType: DaemonType.MANAGED,
  status: DaemonStatus.ACTIVE, storageSize: "50Gi", hostname: DAEMON_NAMES.cloud, platform: "linux/amd64",
  connectedAt: tsAgo(3 * HOUR), createdAt: tsAgo(12 * DAY), updatedAt: tsAgo(3 * HOUR), size: DaemonSize.DAEMON_SIZE_M, idleTimeout: "30m",
  lifecyclePhase: DaemonLifecyclePhase.READY, defaultPortAccess: PortAccessMode.AUTHENTICATED, diskMode: DaemonDiskMode.PERSISTENT, orgId: DEMO_ORG,
});

export function daemonHandlers() {
  return {
    ...serve(DaemonRegistryService, {
      listDaemons: () => ({ daemons: daemons() }),
    }),
    ...serve(DaemonService, {
      getDaemon: (req) => {
        if (req.daemonId !== DAEMON_IDS.cloud) throw new ConnectError("daemon not found", Code.NotFound);
        return { daemon: cloudSpec(), workspaceBaseDomain: "" };
      },
    }),
  };
}
