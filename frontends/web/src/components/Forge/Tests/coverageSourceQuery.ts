// Copyright (c) 2026 Reliant Labs

/**
 * A covered file's source, through the CoverageSourceContext seam, verified
 * against the report before anything is drawn over it.
 *
 * Keyed by blob id in the org's project: a blob never changes, so
 * one read serves every report that names it until the page is left (a
 * revoked connection is noticed on the next visit). A file the run changed
 * without committing has no blob to fetch, and nothing is asked.
 */

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";

import { orgQueryEnabled, useOrgScope, withOrgFailure } from "@/lib/orgScope";
import { useCoverageSourceFetch } from "@/services/coverageSource/CoverageSourceContext";
import { verifySource, type CoverageSource } from "@/services/coverageSource/coverageSource";
import type { FileCoverage } from "@/services/controlPlane/tests/types";

import { lastCoveredLine } from "./coverageModel";

/**
 * A blob's source in one org's project. CoverageSourceService is reliant's, not
 * TestsService's, so it has no generated key there: this is the key the Tests
 * reads used before they moved onto theirs.
 */
const coverageSourceKey = (orgId: string | null, project: string, blobId: string) => ["tests", orgId ?? "", project, "source", blobId] as const;

/** `file` unset: nothing is fetched. `source` is the verified answer, or undefined while it loads. */
export function useCoverageSource(project: string, file: FileCoverage | undefined): { source: CoverageSource | undefined; isLoading: boolean } {
  const fetchSource = useCoverageSourceFetch();
  const org = useOrgScope();
  const blobId = file?.blobId ?? "";
  const query = useQuery(
    withOrgFailure(
      {
        queryKey: coverageSourceKey(org.orgId, project, blobId),
        queryFn: ({ signal }: { signal: AbortSignal }) => fetchSource(project, blobId, { orgId: org.orgId, signal }),
        enabled: !!project && !!blobId && orgQueryEnabled(org),
        staleTime: Infinity,
        retry: false,
      },
      org
    )
  );
  const source = useMemo((): CoverageSource | undefined => {
    if (!file) return undefined;
    if (!file.blobId) return { unavailable: "uncommitted" };
    if (query.error) return { unavailable: "unreachable" };
    if (!query.data) return undefined;
    return verifySource(query.data, file.blobId, lastCoveredLine(file.lines));
  }, [file, query.data, query.error]);
  return { source, isLoading: !!file && !!blobId && query.isLoading };
}
