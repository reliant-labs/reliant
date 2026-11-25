// Copyright (c) 2026 Reliant Labs

/**
 * DEV-ONLY proving harness for the ML tab. NOT a product surface.
 *
 * The real ML route (MlRoute: the entitlement gate, then the page) in the
 * real console, on its real client over the shop fixture pack
 * (ConsoleFixturePreview) — no control plane — so the "Reach out to Reliant
 * for access" page and every ML page can be looked at and screenshotted. A
 * case is a named overlay over the pack:
 *
 *   /forge-ml-preview                         the access page: ml not enabled, never requested
 *   …?case=requested                          requested 3 days ago (the button disabled)
 *   …?case=request-works                      Request access succeeds (the pack is writable for it)
 *   (the default case refuses the request, as the demo does: the email fallback)
 *   …?case=entitled                           the six pages on the shop's ML data
 *   …?case=revoked                            entitled, but every ML read answers FEATURE_NOT_ENTITLED
 *   …?case=no-data                            entitled, every ML read Unimplemented (today's real control plane)
 *   …&view=machines|stack|recommendations|runs|models|inference
 *   …&dark=1&scheme=pure-black
 */

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { Code } from "@connectrpc/connect";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { MlEarlyAccessService } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import {
  GetMyOrgEntitlementsResponseSchema,
  OrganizationService,
  RequestOrgEntitlementResponseSchema,
} from "@/gen/controlplane/services/organization/v1/organization_pb";
import { answering, failing } from "@/fixtures/overlay";
import { demoNow } from "@/fixtures/shop/shopStory";
import type { FixtureServeOptions } from "@/fixtures/preview";

import { isMlView, ML_VIEWS, orgPath } from "../../Console/paths";
import { CONSOLE_SERVE, ConsoleFixturePreview, isDark, PreviewControls } from "../../__preview__/ConsoleFixturePreview";

const BASE = "/forge-ml-preview";
const DAY = 24 * 60 * 60 * 1000;

const notEnabled = (requestedAtMs: number | null) =>
  answering(`ml.not-enabled${requestedAtMs ? ".requested" : ""}`, OrganizationService.method.getMyOrgEntitlements, () =>
    create(GetMyOrgEntitlementsResponseSchema, {
      features: [{ feature: "ml", enabled: false, ...(requestedAtMs ? { requestedAt: timestampFromMs(requestedAtMs) } : {}) }],
    }),
  );

const requestWorks = answering("ml.request-works", OrganizationService.method.requestOrgEntitlement, () =>
  create(RequestOrgEntitlementResponseSchema, { entitlement: { feature: "ml", enabled: false, requestedAt: timestampFromMs(demoNow()) }, alreadyRequested: false, alreadyEnabled: false }),
);

/** The pack's answers, writable: only for the case whose request should go through. */
const WRITABLE: FixtureServeOptions = { ...CONSOLE_SERVE, readOnly: false };

const CASES = [
  { id: "access", label: "not enabled (request refused → email)", overlay: notEnabled(null) },
  { id: "requested", label: "requested 3 days ago", overlay: notEnabled(demoNow() - 3 * DAY) },
  { id: "request-works", label: "request succeeds", overlay: [notEnabled(null), requestWorks], serve: WRITABLE },
  { id: "entitled", label: "entitled: the pages" },
  {
    id: "revoked",
    label: "revoked: reads refused",
    overlay: failing("ml.revoked", MlEarlyAccessService, Code.PermissionDenied, "ML is available by request and is not enabled for this organization.", {
      "x-forge-error-reason": "FEATURE_NOT_ENTITLED",
    }),
  },
  { id: "no-data", label: "entitled, no data yet", overlay: failing("ml.no-data", MlEarlyAccessService, Code.Unimplemented) },
] as const;

type PreviewSearch = { case?: string; view?: string; dark?: boolean | string | number; scheme?: string };

export default function MlPreview() {
  const search = useSearch({ strict: false }) as PreviewSearch;
  const active = CASES.find((c) => c.id === search.case) ?? CASES[0];
  const view = isMlView(search.view) ? search.view : ML_VIEWS[0];
  const path = orgPath({ kind: "ml", view }, { org: null });
  return (
    <ConsoleFixturePreview
      key={`${active.id}|${path}`}
      path={path}
      overlay={"overlay" in active ? active.overlay : null}
      serve={"serve" in active ? active.serve : undefined}
      controls={
        <PreviewControls base={BASE} cases={CASES} caseId={active.id} defaultCase="access" scheme={search.scheme ?? "professional-blue"} dark={isDark(search.dark)}>
          <ViewPicker view={view} />
        </PreviewControls>
      }
    />
  );
}

function ViewPicker({ view }: { view: string }) {
  const navigate = useNavigate();
  return (
    <select
      data-testid="preview-ml-view"
      value={view}
      onChange={(e) => void navigate({ to: BASE, search: (prev: Record<string, unknown>) => ({ ...prev, view: e.target.value }), replace: true } as never)}
      className="rounded-md border border-border bg-surface px-2 py-1 text-sm text-ink"
    >
      {ML_VIEWS.map((v) => (
        <option key={v} value={v}>
          {v}
        </option>
      ))}
    </select>
  );
}
