// Copyright (c) 2025 Reliant Labs

/**
 * Holds the landing site's `rid` / `utm_*` handoff before the router or a sign-in
 * redirect can drop it from the address bar; telemetry hands it to the runtime when
 * it starts. A bare import, for the same reason as browser-log-boot: imports
 * evaluate in order. See lib/landingAttribution.ts for what and why.
 */
import { holdLandingAttribution } from "./landingAttribution";

holdLandingAttribution();
