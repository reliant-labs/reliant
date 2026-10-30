// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import { InboxAutomationDefinitionBrokenSchema, InboxRefusedAutomationSchema } from "@/gen/reliant/v1/inbox_pb";
import { TriggerEventDefinitionSchema } from "@/gen/reliant/v1/trigger_pb";
import { definitionBrokenDetail } from "../InboxItem";

describe("definitionBrokenDetail", () => {
  it("names the error, the branch and commit it came from, and what it refuses", () => {
    const broken = create(InboxAutomationDefinitionBrokenSchema, {
      definition: create(TriggerEventDefinitionSchema, {
        path: "control-plane/.reliant/workflows/release.yaml",
        daemonName: "cicd",
        branch: "feat/x",
        commit: "1a2b3c4d5e6f7a8b9c0d",
        dirty: true,
        error: "release.yaml does not parse",
      }),
      automations: [
        create(InboxRefusedAutomationSchema, { triggerId: "a", triggerName: "Nightly" }),
        create(InboxRefusedAutomationSchema, { triggerId: "b", triggerName: "On push" }),
      ],
    });
    expect(definitionBrokenDetail(broken)).toBe(
      "Release.yaml does not parse · feat/x 1a2b3c4 on cicd · uncommitted changes · refuses Nightly, On push",
    );
  });

  it("says detached HEAD when no branch was checked out", () => {
    const broken = create(InboxAutomationDefinitionBrokenSchema, {
      definition: create(TriggerEventDefinitionSchema, { commit: "abcdef0123", error: "bad" }),
    });
    expect(definitionBrokenDetail(broken)).toBe("Bad · detached HEAD abcdef0");
  });
});
