// Copyright (c) 2025 Reliant Labs

/**
 * SystemService, BackgroundService and ConnectorService: the server is the
 * v1.31.0 release (as the cloud machine's daemon is, workspace.ts), no agent
 * has a background process running, and Alex has granted no connector to a
 * machine.
 */
import { BackgroundService } from "@/gen/reliant/v1/background_pb";
import { ConnectorService } from "@/gen/reliant/v1/connector_pb";
import { SystemService } from "@/gen/reliant/v1/system_pb";

import { DAY } from "../../shopStory";
import { RELIANT_RELEASE, isoAgo } from "../../workspace";
import { serve } from "../serve";

export function systemHandlers() {
  return {
    ...serve(SystemService, {
      version: () => ({ version: RELIANT_RELEASE.version, commit: RELIANT_RELEASE.commit, date: isoAgo(2 * DAY), branch: "main" }),
    }),
    ...serve(BackgroundService, { listProcesses: () => ({ processes: [] }) }),
    ...serve(ConnectorService, { listConnectors: () => ({ connectors: [] }) }),
  };
}
