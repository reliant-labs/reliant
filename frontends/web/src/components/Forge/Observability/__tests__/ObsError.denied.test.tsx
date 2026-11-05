import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";

import { ObsError } from "../ObsStates";

// #1831: a credential that cannot choose an organization was told its *role*
// lacked observability, though the credential held obs:read. The denied state
// names no cause of its own and shows the server's.
describe("ObsError — denied", () => {
  it("shows the server's reason instead of asserting a role", () => {
    const reason = "a machine credential is bound to one organization; sign in as a person";
    render(<ObsError error={new ConnectError(reason, Code.PermissionDenied)} subject="logs" />);

    const state = screen.getByTestId("obs-error-denied");
    expect(state).toHaveTextContent(reason);
    expect(state).toHaveTextContent("its reason is below");
    expect(state).not.toHaveTextContent(/^Your role in this organization doesn't include observability/);
  });
});
