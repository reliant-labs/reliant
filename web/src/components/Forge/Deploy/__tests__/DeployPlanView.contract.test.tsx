// Copyright (c) 2025 Reliant Labs

/**
 * THE PREVIEW CONTRACT: which cluster, what blocks, and what cannot be proven.
 *
 * Nothing here touches a transport, a daemon or a cluster. DeployPlanView takes
 * pure props, so every case is a document handed to a render — which is the only
 * safe way to test a screen whose subject is applying manifests to production.
 *
 * What is pinned:
 *   a multi-cluster env renders BOTH declared contexts
 *   current_context is never presented as the target
 *   a blocking preflight finding is prominent and is not rendered as advisory
 *   a skipped preflight says nothing was CHECKED, not that nothing was wrong
 *   the digest/tag split is shown, and a tag is a caveat rather than a failure
 *   rollout mode skip does not render resources as ready
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { DeployPlanView } from "../DeployPlanView";
import { devPlan, guardRefusedPlan, noClusterPlan, prodPlan, taggedPlan } from "./fixtures";

describe("which cluster this lands on", () => {
  it("renders BOTH contexts for a multi-cluster env", () => {
    // control-plane's dev env declares two. A dialog built on `kube_context`
    // alone would show one and write to two.
    render(<DeployPlanView plan={devPlan()} />);

    const target = screen.getByTestId("deploy-target");
    expect(target.getAttribute("data-multi-cluster")).toBe("true");
    expect(target.getAttribute("data-context-count")).toBe("2");

    expect(screen.getByTestId("deploy-target-context-k3d-control-plane")).toBeTruthy();
    expect(screen.getByTestId("deploy-target-context-k3d-cp-daemon")).toBeTruthy();

    // And the count is stated in words, so a reader cannot skim past the second.
    expect(screen.getByTestId("deploy-target-heading").textContent).toMatch(/2 clusters/);
    expect(screen.getByTestId("deploy-multi-cluster-note").textContent).toMatch(
      /more than one cluster/i
    );
  });

  it("names the declared cluster and namespace for the ordinary single-cluster env", () => {
    render(<DeployPlanView plan={prodPlan()} />);
    const target = screen.getByTestId("deploy-target");
    expect(target.getAttribute("data-multi-cluster")).toBe("false");
    expect(target.textContent).toContain("gke_reliant-labs-475814_us-central1_prod");
    expect(target.textContent).toContain("control-plane-prod");
  });

  it("NEVER presents current_context as the deploy target", () => {
    // prodPlan's ambient context is k3d-control-plane while it deploys to GKE.
    // Forge never reads the ambient one, so it must not appear in the target list.
    render(<DeployPlanView plan={prodPlan()} />);

    const contexts = screen.getByTestId("deploy-target-contexts");
    expect(contexts.textContent).toContain("gke_reliant-labs-475814_us-central1_prod");
    expect(contexts.textContent).not.toContain("k3d-control-plane");

    // It IS shown, and explicitly labelled as not the target.
    const aside = screen.getByTestId("deploy-current-context");
    expect(aside.textContent).toMatch(/not the target/i);
    expect(aside.textContent).toContain("k3d-control-plane");

    // There is no element claiming the ambient context is where this lands.
    expect(screen.queryByTestId("deploy-target-context-k3d-control-plane")).toBeNull();
  });

  it("says an env with no declared cluster has none, rather than inventing one", () => {
    render(<DeployPlanView plan={noClusterPlan()} />);
    expect(screen.getByTestId("deploy-target").getAttribute("data-context-count")).toBe("0");
    expect(screen.getByTestId("deploy-target-none")).toBeTruthy();
  });

  it("renders forge's own guard refusal with its fix and the available contexts", () => {
    render(<DeployPlanView plan={guardRefusedPlan()} />);
    const refused = screen.getByTestId("deploy-guard-refused");
    expect(refused.textContent).toMatch(/will not deploy/i);
    expect(screen.getByTestId("deploy-guard-fix").textContent).toContain("get-credentials");
  });
});

describe("preflight", () => {
  it("renders a blocking finding prominently, and NOT as advisory", () => {
    render(<DeployPlanView plan={devPlan()} />);

    const preflight = screen.getByTestId("deploy-preflight");
    expect(preflight.getAttribute("data-blocking-count")).toBe("2");

    const blocking = screen.getByTestId("deploy-preflight-blocking");
    // It says the deploy WOULD FAIL — not that there are warnings to consider.
    expect(blocking.textContent).toMatch(/would\s+fail/i);
    expect(blocking.textContent).toMatch(/not advisory/i);
    expect(blocking.textContent).toContain("STRIPE_SECRET_KEY");
    expect(blocking.textContent).toContain("LITELLM_MASTER_KEY");

    // The blocking findings are inside the blocking panel, flagged as such — not
    // mixed in with the advisory list where they would read as equal.
    const advisory = screen.getByTestId("deploy-preflight-advisory");
    expect(advisory.textContent).not.toContain("STRIPE_SECRET_KEY");
    expect(advisory.textContent).toContain("image_arch");

    // The advisory finding is marked as non-blocking, from its own field rather
    // than from its check name.
    const advisoryRow = screen.getByTestId("deploy-finding-image_arch");
    expect(advisoryRow.getAttribute("data-blocking")).toBe("false");
  });

  it("says a SKIPPED preflight checked nothing, not that nothing was wrong", () => {
    render(
      <DeployPlanView
        plan={prodPlan({ preflight: { status: "skipped_flag", findings: [], blocking: 0 } })}
      />
    );
    expect(screen.getByTestId("deploy-preflight-status").textContent).toMatch(/nothing was checked/i);
    // The empty-findings copy must not read as a clean bill of health.
    const preflight = screen.getByTestId("deploy-preflight");
    expect(preflight.textContent).toMatch(/nothing is known/i);
    expect(preflight.textContent).not.toMatch(/found nothing wrong/i);
  });

  it("reports a clean preflight as clean only when it actually ran", () => {
    render(<DeployPlanView plan={prodPlan()} />);
    expect(screen.getByTestId("deploy-preflight").textContent).toMatch(/found nothing wrong/i);
  });
});

describe("image pinning", () => {
  it("shows the digest/tag split and calls a tag a caveat, not a failure", () => {
    render(<DeployPlanView plan={taggedPlan()} />);

    const images = screen.getByTestId("deploy-images");
    expect(images.getAttribute("data-digest-count")).toBe("1");
    expect(images.getAttribute("data-tag-count")).toBe("1");

    expect(screen.getByTestId("deploy-digest-count").textContent).toMatch(/1 digest-pinned/);
    expect(screen.getByTestId("deploy-tag-count").textContent).toMatch(/1 by mutable tag/);

    // The caveat states what is unknown — not that anything is broken.
    const caveat = screen.getByTestId("deploy-tag-caveat");
    expect(caveat.textContent).toMatch(/cannot be proven/i);
    expect(caveat.textContent).not.toMatch(/\bfail/i);

    // Per-image, the tag one is flagged.
    const tagged = screen.getByTestId(
      "deploy-image-us-central1-docker.pkg.dev/p/r/internal-console"
    );
    expect(tagged.getAttribute("data-pinning")).toBe("tag");
  });

  it("shows no tag warning at all when everything is digest-pinned", () => {
    render(<DeployPlanView plan={prodPlan()} />);
    expect(screen.queryByTestId("deploy-tag-count")).toBeNull();
    expect(screen.queryByTestId("deploy-tag-caveat")).toBeNull();
  });
});

describe("rollout mode", () => {
  it("does NOT render resources as ready under mode skip", () => {
    // Under skip every resource is legitimately not_waited. That is not fine and
    // it is not broken — it is unknown.
    render(
      <DeployPlanView
        plan={prodPlan({
          mode: "apply",
          rollout: {
            mode: "skip",
            timeout_seconds: 0,
            results: [
              { kind: "Deployment", name: "admin-server", state: "not_waited" },
              { kind: "Deployment", name: "litellm", state: "not_waited" },
            ],
            ready: 0,
            failed: 0,
            timed_out: 0,
            not_waited: 2,
          },
        })}
      />
    );

    expect(screen.getByTestId("deploy-rollout").getAttribute("data-rollout-mode")).toBe("skip");
    expect(screen.getByTestId("deploy-rollout-mode").textContent).toMatch(/nothing will be observed/i);

    for (const name of ["admin-server", "litellm"]) {
      const row = screen.getByTestId(`deploy-rollout-${name}`);
      expect(row.getAttribute("data-state")).toBe("not_waited");
      // The critical assertion: NOT the known-good treatment.
      expect(row.getAttribute("data-certainty")).toBe("unknown");
      expect(row.textContent).not.toMatch(/\bReady\b/);
    }

    // And no "ready" tally chip is produced from a rollout that waited on nothing.
    expect(screen.queryByTestId("deploy-tally-ready")).toBeNull();
    expect(screen.getByTestId("deploy-tally-not_waited").textContent).toMatch(/2 not waited on/i);
  });

  it("states the per-resource budget as per-resource", () => {
    render(<DeployPlanView plan={prodPlan()} />);
    expect(screen.getByTestId("deploy-rollout").textContent).toContain("300s per resource");
  });
});
