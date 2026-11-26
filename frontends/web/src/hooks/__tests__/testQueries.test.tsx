/**
 * The Tests reads moved onto the generated TestsService keys. What the old
 * hand-written keys gave must still hold:
 *
 *   - every key names the org and the project, so two orgs or two projects
 *     never share an answer, and an org switch purges them (their root is in
 *     ORG_SCOPED_QUERY_ROOTS);
 *   - the paged lists keep their previous page on screen only within one org
 *     (keepPreviousInOrg reads the org from the generated key's request);
 *   - with no control plane configured, a read on the app's transport fails
 *     Unimplemented (the pages' "not configured" state), never with the
 *     transport's plain Error ("unavailable").
 */
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient } from "@tanstack/react-query";
import { expect, it } from "vitest";

import { testsServiceKeys, testsServiceQueries } from "@/hooks/controlplane/tests-service-hooks_gen";
import { keepPreviousInOrg, requestOrgOfKey } from "@/lib/orgScope";
import { ORG_SCOPED_QUERY_ROOTS } from "@/store/activeOrgStore";
import { runsRequest, summaryRequest } from "@/services/controlPlane/tests/wire";

import { onControlPlane } from "../test-queries";

it("an org switch purges every TestsService read", () => {
  expect(ORG_SCOPED_QUERY_ROOTS).toContain(testsServiceKeys.all[0]);
});

it("a key names its org and project", () => {
  const a = testsServiceKeys.getTestSummary(summaryRequest({ orgId: "org-a", project: "shop" }));
  const b = testsServiceKeys.getTestSummary(summaryRequest({ orgId: "org-b", project: "shop" }));
  const other = testsServiceKeys.getTestSummary(summaryRequest({ orgId: "org-a", project: "cart" }));
  expect(requestOrgOfKey(a)).toBe("org-a");
  expect(a).not.toEqual(b);
  expect(a).not.toEqual(other);
});

it("a paged list keeps its previous answer only from the same org", () => {
  const { pageToken: _a, ...inA } = runsRequest({ orgId: "org-a", project: "shop", filters: {} });
  const { pageToken: _b, ...inB } = runsRequest({ orgId: "org-b", project: "shop", filters: {} });
  const previous = { queryKey: testsServiceKeys.listTestRunsInfinite(inA) };
  expect(keepPreviousInOrg("org-a")("rows", previous)).toBe("rows");
  expect(keepPreviousInOrg("org-b")("rows", { queryKey: testsServiceKeys.listTestRunsInfinite(inB) })).toBe("rows");
  expect(keepPreviousInOrg("org-b")("rows", previous)).toBeUndefined();
});

it("with no control plane configured, a read outside any scope fails Unimplemented", async () => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read = client.fetchQuery(onControlPlane(testsServiceQueries.getTestSummary(summaryRequest({ orgId: "org-a", project: "shop" }))));
  await expect(read).rejects.toBeInstanceOf(ConnectError);
  await expect(read).rejects.toMatchObject({ code: Code.Unimplemented });
});
