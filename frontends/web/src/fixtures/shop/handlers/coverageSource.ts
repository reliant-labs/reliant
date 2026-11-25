// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's CoverageSourceService handler: the coverage file view's
 * source (services/coverageSource/coverageSource.ts asks reliant's
 * GetCoverageSource, which reads the blob from the project's GitHub
 * repository). In the pack, charge.go's blob at v1.8.4 is answered from the
 * bundle (model/tests.ts SHOP_CHARGE_SOURCE), so the file view draws real code
 * under its hits; any other blob gets the honest answer, NOT_YET_AVAILABLE:
 * this deployment reads no repository. Nothing is fetched, from GitHub or
 * anywhere else, and the page's real fetcher runs unchanged over the fixture
 * transport (no CoverageSourceProvider).
 */

import { create } from "@bufbuild/protobuf";

import { CoverageSourceService, CoverageSourceUnavailable, GetCoverageSourceResponseSchema, type GetCoverageSourceRequest } from "@/gen/reliant/v1/coverage_source_pb";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { SHOP_CHARGE_BLOB, SHOP_CHARGE_SOURCE } from "../model/tests";
import { withMessageRequests } from "./defaults";
import { DEMO_SCOPE, scopedToDemo, type PackScope } from "./scope";

export const COVERAGE_SOURCE_RPC = `${CoverageSourceService.typeName}/GetCoverageSource`;

export function coverageSourceHandlers(scope: PackScope = DEMO_SCOPE): MockHandlers {
  const handlers: MockHandlers = {
    [COVERAGE_SOURCE_RPC]: (req: GetCoverageSourceRequest) =>
      create(GetCoverageSourceResponseSchema, {
        result:
          req.blobId.toLowerCase() === SHOP_CHARGE_BLOB
            ? { case: "content", value: new TextEncoder().encode(SHOP_CHARGE_SOURCE) }
            : { case: "unavailable", value: CoverageSourceUnavailable.NOT_YET_AVAILABLE },
      }),
  };
  return withMessageRequests(scopedToDemo(handlers, [CoverageSourceService], scope), [CoverageSourceService]);
}
