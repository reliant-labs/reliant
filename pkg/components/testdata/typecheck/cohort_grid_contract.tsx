// The CohortGrid's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import CohortGrid, { type Cohort } from "./cohort_grid";

const cohorts: Cohort[] = [
  { label: "Oct 6", size: 412, retained: [412, 198, 151, 133] },
  { label: "Oct 13", size: 388, retained: [388, 201, null] },
  { label: "Oct 20", size: 17, retained: [17, 9] },
];

export function Accepted() {
  return (
    <>
      <CohortGrid cohorts={cohorts} />
      <CohortGrid
        cohorts={cohorts}
        periodLabel={(p) => `M${p}`}
        minN={30}
        dimBy="cell"
        showAverage={false}
        sizeLabel="Signups"
        caption={<span>Weekly retention</span>}
        formatPercent={(share) => share.toFixed(2)}
        className="mt-4"
      />
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error retained counts are numbers */}
      <CohortGrid cohorts={[{ label: "Oct 6", size: 4, retained: ["4"] }]} />
      {/* @ts-expect-error dimBy is "cohort" or "cell" */}
      <CohortGrid cohorts={cohorts} dimBy="row" />
      {/* @ts-expect-error a cohort needs its size */}
      <CohortGrid cohorts={[{ label: "Oct 6", retained: [4] }]} />
    </>
  );
}
