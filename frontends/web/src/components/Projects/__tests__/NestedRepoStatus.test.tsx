/**
 * A set-up's nested repositories: each one's progress, and on failure its
 * name with git's own words, never a generic "failed".
 */
import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";
import { NestedRepoStatus } from "../NestedRepoStatus";

describe("NestedRepoStatus", () => {
  it("shows each nested repository's clone state, and git's error for a failed one", () => {
    render(
      <NestedRepoStatus
        repos={[
          { name: "forge", relative_path: "forge", install_state: ProjectInstallState.INSTALLING },
          { name: "control-plane", relative_path: "control-plane", install_state: ProjectInstallState.INSTALLED },
          {
            name: "sdk",
            relative_path: "tools/sdk",
            install_state: ProjectInstallState.FAILED,
            install_error: "git clone failed: exit status 128: Permission denied (publickey).",
          },
        ]}
      />,
    );
    const list = screen.getByTestId("nested-repo-status");
    expect(list).toHaveTextContent("Cloning forge/…");
    expect(list).toHaveTextContent("control-plane/ cloned");
    expect(screen.getByRole("alert")).toHaveTextContent(
      "tools/sdk/: git clone failed: exit status 128: Permission denied (publickey).",
    );
  });

  it("names a repository whose folder is called something else", () => {
    render(
      <NestedRepoStatus repos={[{ name: "sdk", relative_path: "vendor/client", install_state: ProjectInstallState.INSTALLING }]} />,
    );
    expect(screen.getByTestId("nested-repo-status")).toHaveTextContent("Cloning sdk (vendor/client/)…");
  });

  it("renders nothing for a checkout with no nested repositories (or an older server)", () => {
    const { container } = render(<NestedRepoStatus repos={undefined} />);
    expect(container).toBeEmptyDOMElement();
    const empty = render(<NestedRepoStatus repos={[]} />);
    expect(empty.container).toBeEmptyDOMElement();
  });
});
