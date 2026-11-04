// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THE LIVE SURFACES MAY SAY — no internal nouns (#366).
 *
 * A customer did not choose our control plane, cannot visit its hostname, has
 * no use for the id it files their environment under, and does not run the
 * clusters we host. Naming any of those spends the most valuable line on the
 * screen telling them something they cannot act on — and in an ERROR state it
 * is worse than useless, because it answers a question they did not ask
 * instead of the one they did ("is my environment gone?").
 *
 * These tests pin the ABSENCE, which is exactly the kind of thing a later
 * "let's show a bit more detail" quietly undoes. They run over the states
 * where internal nouns previously leaked: the empty and error paths, which are
 * the ones nobody looks at in a screenshot review.
 *
 * The kube context is NOT banned everywhere — for a cluster the customer owns
 * it is their own name and carries information. It is banned for the
 * environments WE run, which is what the kinds distinguish.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";

import { CloudNotice } from "../../SourceNotices";

/**
 * Nouns that are ours, not the customer's.
 *
 * The convergence vocabulary is in here for the same reason as the rest. A
 * customer did not choose Flux, does not operate a reconciler, and has never
 * heard of a Kustomization — those are how WE implement "did your release
 * actually arrive", which is the question they asked. The words appear in the
 * proto and in our own reasoning, and they must not reach the screen as the
 * sentence a reader is given. The raw state identifiers are banned for the
 * same reason: `in_sync` is a wire value, not English.
 *
 * The one deliberate exception is a FAILURE's verbatim reason string
 * (`ReconciliationFailed`, `HealthCheckFailed`), passed through untranslated
 * because it is the thing someone searches for. It is asserted separately, as
 * subordinate detail beside a plain sentence — never as the sentence itself.
 */
const INTERNAL_NOUNS = [
  /control[ -]plane/i,
  /admin\.reliantapi\.com/i,
  /endpoint/i,
  /environment id/i,
  /\bdenv_/i,
  /ledger/i,
  /kube[ -]?context/i,
  /\bflux\b/i,
  /reconciler/i,
  /kustomization/i,
  /\bin_sync\b/i,
  /\bnot-reported\b/i,
  /lastAppliedRevision/i,
];

function expectNoInternalNouns(text: string) {
  for (const noun of INTERNAL_NOUNS) {
    expect(text, `copy must not name "${noun}"`).not.toMatch(noun);
  }
}

describe("the unavailable notices", () => {
  it("say what the reader cannot see and what to do, naming nothing internal", () => {
    for (const availability of ["no-access", "not-configured", "unreachable"] as const) {
      const { container, unmount } = render(
        <CloudNotice availability={availability} detail={undefined} />
      );
      expectNoInternalNouns(container.textContent ?? "");
      unmount();
    }
  });

  it("still passes the server's own message through as detail", () => {
    // The one place a technical string beats a friendly one: it is what a
    // support conversation needs, and it is clearly subordinate.
    render(<CloudNotice availability="unreachable" detail="connect: connection refused" />);
    expect(screen.getByTestId("forge-cloud-unreachable").textContent).toContain(
      "connect: connection refused"
    );
  });
});

