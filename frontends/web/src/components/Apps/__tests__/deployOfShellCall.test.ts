// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { deployOfShellCall } from "../DeployedAppCard";

const result = (exit_code: number, is_error = false) => ({
  content: JSON.stringify({ stdout: "", stderr: "", exit_code }),
  is_error,
});

describe("deployOfShellCall — a chat's deploy, from the shell call that ran it", () => {
  it("reads the environment from the command, as an object or as JSON", () => {
    expect(deployOfShellCall({ command: "reliant forge env deploy prod" }, result(0))).toEqual({ env: "prod", queued: false });
    expect(deployOfShellCall(JSON.stringify({ command: "forge env deploy staging" }), result(0))).toEqual({ env: "staging", queued: false });
    // Older calls recorded the input JSON inside the command field.
    expect(deployOfShellCall({ command: JSON.stringify({ command: "forge env deploy prod" }) }, result(0))).toEqual({ env: "prod", queued: false });
  });

  it("reports a deploy queued on billing (exit 7) as queued", () => {
    expect(deployOfShellCall({ command: "reliant forge env deploy prod" }, result(7))).toEqual({ env: "prod", queued: true });
  });

  it("claims nothing for a deploy that failed, has not finished, or ran in the background", () => {
    const command = { command: "reliant forge env deploy prod" };
    expect(deployOfShellCall(command, result(1))).toBeNull();
    expect(deployOfShellCall(command, result(0, true))).toBeNull();
    expect(deployOfShellCall(command, undefined)).toBeNull();
    expect(
      deployOfShellCall(command, { content: JSON.stringify({ process_id: "p1", command: command.command, backgrounded: true }) })
    ).toBeNull();
  });

  it("accepts a plain-text result, which carries no exit code", () => {
    expect(deployOfShellCall({ command: "forge env deploy prod" }, { content: "release v1 promoted" })).toEqual({ env: "prod", queued: false });
  });

  it("ignores every other command", () => {
    expect(deployOfShellCall({ command: "npm test" }, result(0))).toBeNull();
  });
});
