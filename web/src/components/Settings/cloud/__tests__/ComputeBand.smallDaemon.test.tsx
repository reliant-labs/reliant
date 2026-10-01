import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ComputeBand, type ComputeBandDimension } from "../overview/ComputeBand";

/**
 * Per-daemon billing task J (design §6.2, "Usage card"): when the server sends
 * the §6.1 fields, the compute card leads with "N small-daemon-hours left",
 * then the burn line and "runs out ~…", stamped "as of HH:MM". Each part is
 * absent when it has nothing true to say, and the whole reading is absent
 * from an older server — never a fabricated or zero value.
 *
 * FAILS ON main: ComputeBand had no small-daemon reading at all.
 */

function band(dimension: Partial<ComputeBandDimension>, usageUnavailable = false) {
  return render(
    <ComputeBand
      planName="Compute Medium"
      pricePerMonthLabel="$29.00/mo"
      renewsOnLabel={null}
      allowedSizesLabel={null}
      dimensions={[
        {
          id: "daemon_compute",
          includedHoursLabel: "320.0 small-daemon-hours included",
          usedHoursLabel: "278.5 small-daemon-hours",
          capacity: { usedPct: 87, overagePct: 0, state: "near" },
          estimatedOverageCostLabel: null,
          ...dimension,
        },
      ]}
      grantedMinutesRemaining={0}
      planDetailUnavailable={false}
      usageUnavailable={usageUnavailable}
      onChangePlan={() => {}}
      onRetryUsage={() => {}}
      renderOverageControl={() => null}
    />,
  );
}

describe("ComputeBand — small-daemon-hours (J)", () => {
  it("leads with the remaining small-daemon-hours, the burn line, and as-of", () => {
    band({
      smallDaemon: {
        headline: "41.5 small-daemon-hours left",
        burn: "≈ 10.4 h on your Large (4×)",
        runsOut: "runs out ~Thu at the current rate",
        asOf: "as of 14:32",
      },
    });
    expect(screen.getByTestId("compute-sdh-headline")).toHaveTextContent(
      "41.5 small-daemon-hours left",
    );
    expect(screen.getByTestId("compute-sdh-headline")).toHaveTextContent("as of 14:32");
    expect(screen.getByTestId("compute-sdh-burn")).toHaveTextContent(
      "≈ 10.4 h on your Large (4×) · runs out ~Thu at the current rate",
    );
  });

  it("omits the burn line when nothing is running", () => {
    band({ smallDaemon: { headline: "320.0 small-daemon-hours left", burn: null, runsOut: null, asOf: null } });
    expect(screen.getByTestId("compute-sdh-headline")).toBeInTheDocument();
    expect(screen.queryByTestId("compute-sdh-burn")).not.toBeInTheDocument();
  });

  it("renders no small-daemon reading from an older server", () => {
    band({});
    expect(screen.queryByTestId("compute-sdh-headline")).not.toBeInTheDocument();
  });

  // usage_measured=false still renders as UNKNOWN: the band says so instead
  // of a headline.
  it("says usage is unavailable rather than showing a headline when unmeasured", () => {
    band(
      { smallDaemon: { headline: null, burn: null, runsOut: null, asOf: null } },
      true,
    );
    expect(screen.getByText(/usage unavailable for this period/i)).toBeInTheDocument();
    expect(screen.queryByTestId("compute-sdh-headline")).not.toBeInTheDocument();
  });
});
