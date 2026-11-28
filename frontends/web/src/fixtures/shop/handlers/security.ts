// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's VulnerabilityService handlers: the four reads the Security
 * pages make (Vulnerabilities, SBOMs, an advisory, a release's findings, the
 * Overview's card), answered on the WIRE as control-plane would answer them.
 * The pack composes them with the other services' handlers (../index.ts
 * shopScenario).
 *
 * The data is shop's story (model/security.ts), and the server's rules
 * (filters, order, counts, paging, gate verdicts) are Security/vulnDataset.ts's.
 * Security/vulnService.ts decodes each request into the console's plain
 * types, asks the engine, and encodes the answer into the generated response
 * message. So the demo runs the pages' REAL reads (the generated hooks and
 * services/controlPlane/vulnerabilities/wire.ts) over the fixture transport.
 */

import { VulnerabilityService } from "@/gen/controlplane/services/vulnerability/v1/vulnerability_pb";
import { VULNERABILITY_READ_RPCS, vulnerabilityHandlers, type VulnerabilityReads } from "@/components/Forge/Security/vulnService";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { shopVulnEngine } from "../model/security";
import { demoNow } from "../shopStory";
import { withMessageRequests } from "./defaults";
import { DEMO_SCOPE, scopedToDemo, type PackScope } from "./scope";

/** The RPCs served here: every read the Security pages make. */
export const SECURITY_RPCS = VULNERABILITY_READ_RPCS;

/**
 * VulnerabilityService's reads over shop's dataset as of `now` (the pack
 * clock), with requests normalized to messages and reads outside shop
 * answered empty (scope.ts).
 */
export function securityHandlers(now: () => number = demoNow, scope: PackScope = DEMO_SCOPE): MockHandlers {
  // One engine per transport: the dataset is fixed by the pack clock, which never moves.
  let engine: ReturnType<typeof shopVulnEngine> | null = null;
  const answers = () => (engine ??= shopVulnEngine(now()));
  const reads: VulnerabilityReads = {
    getVulnerabilitySummary: (req) => answers().getVulnerabilitySummary(req),
    listVulnerabilities: (req) => answers().listVulnerabilities(req),
    getVulnerability: (req) => answers().getVulnerability(req),
    getReleaseVulnerabilities: (req) => answers().getReleaseVulnerabilities(req),
  };
  return withMessageRequests(scopedToDemo(vulnerabilityHandlers(reads), [VulnerabilityService], scope), [VulnerabilityService]);
}
