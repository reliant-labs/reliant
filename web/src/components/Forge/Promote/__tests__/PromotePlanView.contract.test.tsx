// Copyright (c) 2025 Reliant Labs

/**
 * THE VISUAL CONTRACT TESTS for the promote diff.
 *
 * Each pins a property that, if it regressed, would let a reviewer authorise
 * something other than what they thought they were authorising:
 *
 *   a ROLLBACK reads differently from a forward promote
 *   ADDED/REMOVED read as structural, not as a third shade of "changed"
 *   an uncomputed commit range renders neither a count nor a zero
 *   promote never claims a deploy happened
 *
 * Every assertion is on the RENDERED DISTINCTION, and the treatments are compared
 * AGAINST EACH OTHER rather than against hardcoded class strings — a test that
 * pinned literal classes would pass while someone gave a rollback the same
 * appearance as a forward promote under a renamed token, which is precisely the
 * regression that matters. This mirrors EnvStatusPanel.contract.test.tsx next
 * door.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { PromotePlanView } from "../PromotePlanView";
import { forwardPlan, rollbackPlan } from "./fixtures";

describe("direction", () => {
  it("renders a rollback distinctly from a forward promote", () => {
    const forward = render(<PromotePlanView plan={forwardPlan()} />);
    const forwardBanner = forward.getByTestId("promote-direction");
    const forwardLabel = forward.getByTestId("promote-direction-label").textContent ?? "";
    const forwardClasses = forwardBanner.className;
    forward.unmount();

    const back = render(<PromotePlanView plan={rollbackPlan()} />);
    const backBanner = back.getByTestId("promote-direction");
    const backLabel = back.getByTestId("promote-direction-label").textContent ?? "";

    // 1. The semantic category differs.
    expect(forwardBanner.getAttribute("data-direction")).toBe("ahead");
    expect(backBanner.getAttribute("data-direction")).toBe("behind");

    // 2. The visible treatment differs — compared against each other, not
    //    against a literal.
    expect(backBanner.className).not.toBe(forwardClasses);

    // 3. A rollback is SOLID and destructive; it is not the muted/dashed
    //    treatment reserved for directions that move nothing.
    expect(backBanner.className).toMatch(/destructive/);
    expect(backBanner.className).toMatch(/border-solid/);
    expect(forwardClasses).not.toMatch(/destructive/);

    // 4. The word appears in the heading. A reviewer skimming must not have to
    //    decode a colour.
    expect(backLabel.toLowerCase()).toContain("rollback");
    expect(forwardLabel.toLowerCase()).not.toContain("rollback");
    expect(backLabel).not.toBe(forwardLabel);
  });

  it("carries forge's own direction sentence, which names the releases", () => {
    render(<PromotePlanView plan={rollbackPlan()} />);
    expect(screen.getByTestId("promote-direction-detail").textContent).toContain("BACKWARDS");
  });

  it("renders a direction it does not recognise as unknown, never as forward", () => {
    render(<PromotePlanView plan={forwardPlan({ direction: "sideways" })} />);
    const banner = screen.getByTestId("promote-direction");
    expect(banner.getAttribute("data-direction")).toBe("unknown");
    // Dashed: nothing was established about where this moves.
    expect(banner.className).toMatch(/border-dashed/);
  });
});

describe("image changes", () => {
  it("renders added and removed distinctly from changed", () => {
    // Both structural kinds and a version change in one render, so the
    // treatments can be compared directly.
    const plan = forwardPlan({
      images: [
        {
          image: "control-plane",
          change: "changed",
          current_digest: "sha256:aaaa1111bbbb",
          target_digest: "sha256:cccc2222dddd",
        },
        { image: "internal-console", change: "added", target_digest: "sha256:eeee3333ffff" },
        { image: "legacy-worker", change: "removed", current_digest: "sha256:9999aaaa8888" },
      ],
    });
    render(<PromotePlanView plan={plan} />);

    const changed = screen.getByTestId("promote-image-control-plane");
    const added = screen.getByTestId("promote-image-internal-console");
    const removed = screen.getByTestId("promote-image-legacy-worker");

    // 1. The semantic shape separates structural from version — this is the
    //    distinction, and it is not a third value of the same axis.
    expect(changed.getAttribute("data-shape")).toBe("version");
    expect(added.getAttribute("data-shape")).toBe("structural");
    expect(removed.getAttribute("data-shape")).toBe("structural");

    // 2. The visible treatment differs from the version row for BOTH.
    expect(added.className).not.toBe(changed.className);
    expect(removed.className).not.toBe(changed.className);

    // 3. Structural rows carry a solid left rule; the version row does not.
    expect(added.className).toMatch(/border-l-2 border-solid/);
    expect(removed.className).toMatch(/border-l-2 border-solid/);
    expect(changed.className).not.toMatch(/border-solid/);

    // 4. And they differ from EACH OTHER — a gain is not a loss.
    expect(added.className).not.toBe(removed.className);
    expect(added.textContent).toContain("+");
    expect(removed.textContent).toContain("−");
    expect(changed.textContent).not.toContain("+");
    expect(changed.textContent).not.toContain("−");
  });

  it("shows one digest for a structural change and two for a version change", () => {
    const plan = forwardPlan({
      images: [
        {
          image: "control-plane",
          change: "changed",
          current_digest: "sha256:aaaa1111bbbb",
          target_digest: "sha256:cccc2222dddd",
        },
        { image: "internal-console", change: "added", target_digest: "sha256:eeee3333ffff" },
      ],
    });
    render(<PromotePlanView plan={plan} />);

    // A version move answers "from what, to what".
    const changed = screen.getByTestId("promote-image-control-plane").textContent ?? "";
    expect(changed).toContain("aaaa1111bbbb");
    expect(changed).toContain("cccc2222dddd");
    expect(changed).toContain("→");

    // An added image has no "from". No arrow, so no empty half that reads as a
    // missing digest.
    const added = screen.getByTestId("promote-image-internal-console").textContent ?? "";
    expect(added).toContain("eeee3333ffff");
    expect(added).not.toContain("→");
  });

  it("does not classify an unrecognised change as unchanged", () => {
    render(
      <PromotePlanView
        plan={forwardPlan({ images: [{ image: "mystery", change: "rebased" }] })}
      />
    );
    const row = screen.getByTestId("promote-image-mystery");
    expect(row.getAttribute("data-change")).toBe("unknown");
    expect(row.getAttribute("data-shape")).toBe("unknown");
  });
});

describe("commit range", () => {
  it("renders a dirty-release range as its own explanation, not as a count", () => {
    // The real staging case: v1.3.0 was cut from a dirty tree, so the range is
    // meaningless even though `count` is present and zero.
    render(<PromotePlanView plan={forwardPlan()} />);

    const section = screen.getByTestId("promote-commits");
    expect(section.getAttribute("data-range-kind")).toBe("unavailable");

    // Forge's own sentence is shown.
    expect(screen.getByTestId("promote-commits-unavailable").textContent).toContain("MEANINGLESS");

    // And crucially: NO count element, and no zero anywhere in the section.
    expect(screen.queryByTestId("promote-commits-count")).toBeNull();
    expect(section.textContent).not.toMatch(/\b0 commits?\b/);
  });

  it.each(["git_unavailable", "ledger_missing", "commit_not_found", "no_commit"])(
    "renders %s without a commit count",
    (state) => {
      render(
        <PromotePlanView
          plan={forwardPlan({
            commits: { state, detail: `range unavailable: ${state}`, count: 0, commits: [] },
          })}
        />
      );
      const section = screen.getByTestId("promote-commits");
      expect(section.getAttribute("data-range-kind")).toBe("unavailable");
      expect(screen.queryByTestId("promote-commits-count")).toBeNull();
      expect(section.textContent).not.toMatch(/\b0 commits?\b/);
    }
  );

  it("renders a state added by a newer forge as unavailable, not as zero", () => {
    render(
      <PromotePlanView
        plan={forwardPlan({ commits: { state: "shallow_clone", count: 0, commits: [] } })}
      />
    );
    const section = screen.getByTestId("promote-commits");
    expect(section.getAttribute("data-range-kind")).toBe("unavailable");
    expect(section.textContent).not.toMatch(/\b0 commits?\b/);
  });

  it("shows a computed count, and says a rollback's commits are being taken away", () => {
    render(<PromotePlanView plan={rollbackPlan()} />);

    const section = screen.getByTestId("promote-commits");
    expect(section.getAttribute("data-range-kind")).toBe("measured");
    expect(screen.getByTestId("promote-commits-count").textContent).toContain("2 commits");

    // `reverts` inverts the meaning: these are commits being removed, and the
    // heading must not call them incoming changes.
    expect(section.textContent).toContain("taken away");
    expect(section.textContent).not.toContain("being promoted");
  });
});

describe("ships nothing", () => {
  it("states that nothing is deployed and names the next step", () => {
    render(<PromotePlanView plan={forwardPlan()} />);
    const notice = screen.getByTestId("promote-ships-nothing");
    expect(notice.textContent).toMatch(/nothing is deployed|Nothing is deployed/);
    expect(screen.getByTestId("promote-next-step").textContent).toBe("forge env deploy staging");
  });
});

describe("binding summary", () => {
  it("renders a never-promoted env as its own state, not a blank release", () => {
    render(
      <PromotePlanView plan={forwardPlan({ current: { bound: false, release_known: false } })} />
    );
    expect(screen.getByTestId("promote-current-unbound").textContent).toContain("Never promoted");
    expect(screen.queryByTestId("promote-current-release")).toBeNull();
  });

  it("badges a dirty-tree release on either endpoint", () => {
    render(<PromotePlanView plan={forwardPlan()} />);
    // The real staging case: the CURRENT release was cut dirty.
    expect(screen.getByTestId("promote-current-dirty")).toBeTruthy();
  });
});
