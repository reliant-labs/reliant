import { describe, expect, it } from "vitest";
import { addProjectLead, isLocalDaemonType } from "../addProjectActions";

// The picker led with "Open Project" for everyone. For a cloud user that
// steers them at a directory picker which reads the WRONG filesystem — the
// browser's host, not the machine their code is on — so the one action that
// can actually work sat underneath it as a quiet secondary.

describe("addProjectLead", () => {
  it("leads with clone for a cloud account", () => {
    expect(addProjectLead({ hasCloudDaemons: true, activeDaemonType: "managed" })).toBe("clone");
  });

  it("leads with clone while a cloud machine is still booting", () => {
    // No daemon has attached yet, which is onboarding and the moment cloning
    // matters most. The clone is durably queued, so it is a valid action even
    // with nothing running.
    expect(addProjectLead({ hasCloudDaemons: true, activeDaemonType: undefined })).toBe("clone");
  });

  it("leads with open when the machine in use is local", () => {
    // A local daemon's filesystem IS the user's, so browsing to a directory
    // is real and usually faster than cloning.
    expect(addProjectLead({ hasCloudDaemons: true, activeDaemonType: "local" })).toBe("open");
  });

  it("leads with open for an install that has no cloud daemons at all", () => {
    expect(addProjectLead({ hasCloudDaemons: false, activeDaemonType: undefined })).toBe("open");
  });
});

describe("isLocalDaemonType", () => {
  it("recognises the local and self-hosted spellings", () => {
    for (const type of ["local", "self_hosted", "self-hosted", "SelfHosted", "LOCAL"]) {
      expect(isLocalDaemonType(type)).toBe(true);
    }
  });

  it("does not treat a managed cloud daemon as local", () => {
    expect(isLocalDaemonType("managed")).toBe(false);
    expect(isLocalDaemonType(undefined)).toBe(false);
  });
});
