// Copyright (c) 2025 Reliant Labs

/** DaemonRegistryService: Alex's cloud machine (up) and laptop (asleep). */
import { DaemonRegistryService } from "@/gen/reliant/v1/daemon_registry_pb";

import { daemons } from "../../workspace";
import { serve } from "../serve";

export function daemonHandlers() {
  return serve(DaemonRegistryService, {
    listDaemons: () => ({ daemons: daemons() }),
  });
}
