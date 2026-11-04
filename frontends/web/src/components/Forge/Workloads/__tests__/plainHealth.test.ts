// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Workload, WorkloadEvent } from "@/services/controlPlane/observability/types";

import {
  KUBERNETES_WORDS,
  PLATFORM_NOTE,
  causeNote,
  eventHeadline,
  eventRawReason,
  exitPhrase,
  healthDetailText,
  healthLine,
  isPlatformCause,
  jobFailurePhrase,
  leakedWords,
  plainErrorText,
  scrubInternals,
} from "../plainHealth";

const ev = (reason: string, plainReason: string): Pick<WorkloadEvent, "reason" | "plainReason"> => ({ reason, plainReason });
const w = (health: Workload["health"], healthCause: Workload["healthCause"]): Pick<Workload, "health" | "healthCause"> => ({ health, healthCause });

describe("event wording", () => {
  it("heads a row with the plain reason when the server has one, else the raw reason", () => {
    expect(eventHeadline(ev("BackOff", "Waiting before the next restart attempt"))).toBe("Waiting before the next restart attempt");
    expect(eventHeadline(ev("SomethingNew", ""))).toBe("SomethingNew");
  });

  it("discloses the raw reason only when it adds something", () => {
    expect(eventRawReason(ev("BackOff", "Waiting before the next restart attempt"))).toBe("BackOff");
    // no plain wording: the headline IS the raw reason, so it is not shown twice
    expect(eventRawReason(ev("SomethingNew", ""))).toBeNull();
    // identical: nothing to add
    expect(eventRawReason(ev("Started", "Started"))).toBeNull();
  });
});

describe("a last exit reads in words", () => {
  it.each([
    ["", ""],
    ["OOMKilled", "Ran out of memory"],
    ["Error", "Exited with an error"],
    ["Completed", "Exited"],
    ["ContainerCannotRun", "Could not start"],
    ["StartError", "Could not start"],
    ["CreateContainerConfigError", "Could not start"],
    ["DeadlineExceeded", "Ran past its time limit"],
    ["Evicted", "Moved to another machine"],
    ["CrashLoopBackOff", "Keeps stopping"],
    ["BackOff", "Keeps stopping"],
    ["SomethingTheRuntimeInvented", "Stopped unexpectedly"],
  ])("%s -> %s", (reason, want) => {
    expect(exitPhrase(reason)).toBe(want);
  });

  it("never returns a Kubernetes word", () => {
    for (const r of ["OOMKilled", "Error", "Completed", "ContainerCannotRun", "DeadlineExceeded", "Evicted", "CrashLoopBackOff", "BackOff", "Weird"]) {
      expect(leakedWords(exitPhrase(r))).toEqual([]);
    }
  });
});

describe("a failed Job reads in words", () => {
  it.each([
    ["BackoffLimitExceeded", "Failed after exhausting its retries"],
    ["DeadlineExceeded", "Ran past its time limit"],
    ["PodFailurePolicy", "Stopped by its failure policy"],
    ["", "Failed"],
    ["SomethingNew", "Failed"],
  ])("%s -> %s", (reason, want) => {
    expect(jobFailurePhrase(reason)).toBe(want);
  });
});

describe("whose fault it is", () => {
  it("says so only for the platform, and only for a workload that has a fault", () => {
    expect(causeNote(w("degraded", "platform"))).toBe(PLATFORM_NOTE);
    expect(causeNote(w("pending", "platform"))).toBe(PLATFORM_NOTE);
    expect(causeNote(w("crash_looping", "platform"))).toBe(PLATFORM_NOTE);
    // the workload's own, undetermined, none: no note, and certainly no blame on the platform
    expect(causeNote(w("degraded", "workload"))).toBeNull();
    expect(causeNote(w("degraded", "undetermined"))).toBeNull();
    expect(causeNote(w("degraded", "none"))).toBeNull();
    // a healthy or stopped workload has no fault to attribute, whatever a stale cause says
    expect(causeNote(w("healthy", "platform"))).toBeNull();
    expect(causeNote(w("suspended", "platform"))).toBeNull();
  });

  it("recognises the platform cause", () => {
    expect(isPlatformCause("platform")).toBe(true);
    for (const c of ["none", "workload", "undetermined"] as const) expect(isPlatformCause(c)).toBe(false);
  });
});

describe("the verdict's lines", () => {
  it("trims the reason and drops an empty detail", () => {
    expect(healthLine({ healthReason: "  It ran out of memory " })).toBe("It ran out of memory");
    expect(healthDetailText({ healthDetail: "  " })).toBeNull();
    expect(healthDetailText({ healthDetail: "last exit OOMKilled" })).toBe("last exit OOMKilled");
  });
});

describe("the contract: no Kubernetes word in a headline", () => {
  it("flags every word it lists and nothing else", () => {
    for (const word of KUBERNETES_WORDS) expect(leakedWords(`it said ${word} again`)).toContain(word);
    expect(leakedWords("Restarting repeatedly (14 times). It ran out of memory (limit 1 GiB)")).toEqual([]);
    expect(leakedWords("Waiting for capacity. This is on us")).toEqual([]);
  });
});

describe("scrubbing Kubernetes internals out of free text", () => {
  it("removes pod and node addresses, node names, internal DNS names, container ids and platform namespaces", () => {
    const cases: [string, string[], string[]][] = [
      [`Readiness probe failed: Get "http://10.36.2.7:8080/readyz": context deadline exceeded`, ["10.36.2.7"], ["/readyz", "localhost:8080", "context deadline exceeded"]],
      [`dial tcp 10.36.2.7:8080: connect: connection refused`, ["10.36.2.7"], ["connection refused"]],
      [`dial tcp [fd00:1234:5678:9abc::1]:8080: connection refused`, ["fd00:1234"], ["connection refused"]],
      [`Node gke-prod-daemon-v2-default-pool-ff79dd20-jt6v status is now: NodeNotReady`, ["gke-prod-daemon"], ["a node"]],
      [`node "gke-prod-obs-v1-9355c605-fp64" not found`, ["gke-prod-obs"], ["a node"]],
      [`lookup otel-agent.observability.svc.cluster.local: no such host`, ["observability.svc"], ["an internal service", "no such host"]],
      [`Container containerd://3f2a9c8e7d6b5a4c3b2a1f0e9d8c7b6a5f4e3d2c1b0a9f8e7d6c5b4a3f2e1d0c exited`, ["3f2a9c8e"], ["exited"]],
      [`pod env-61e3e6d6-aaaa-bbbb-cccc-1234567890ab/api is unschedulable`, ["env-61e3e6d6"], ["this environment", "unschedulable"]],
      [`Back-off restarting failed container api`, [], ["Back-off restarting failed container api"]],
    ];
    for (const [input, gone, kept] of cases) {
      const out = scrubInternals(input);
      for (const g of gone) expect(out, input).not.toContain(g);
      for (const k of kept) expect(out, input).toContain(k);
    }
    expect(scrubInternals("")).toBe("");
  });
});

describe("a runtime error line in words", () => {
  it("replaces the runtime's reason names where they stand, and scrubs", () => {
    expect(plainErrorText("container worker OOMKilled: exceeded its 1Gi memory limit (14 restarts)")).toBe("container worker ran out of memory: exceeded its 1Gi memory limit (14 restarts)");
    expect(plainErrorText("ImagePullBackOff for 10.1.2.3:443")).toBe("can't pull its image for localhost:443");
    // The reason is an adjective in the server's sentence: the phrase around it goes, not just the word.
    expect(plainErrorText("Container api was OOMKilled")).toBe("Container api ran out of memory");
    expect(plainErrorText("Container api is in CrashLoopBackOff")).toBe("Container api keeps restarting");
    expect(plainErrorText("0/3 nodes: FailedScheduling")).toBe("0/3 nodes: waiting for capacity");
    expect(plainErrorText("CrashLoopBackOff and ErrImagePull")).toBe("keeps restarting and can't pull its image");
    expect(plainErrorText("nothing to change")).toBe("nothing to change");
    expect(plainErrorText("")).toBe("");
  });

  it("leaves no Kubernetes word in what it returns", () => {
    for (const raw of ["OOMKilled", "CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "FailedScheduling", "BackoffLimitExceeded", "ContainerCannotRun"]) {
      expect(leakedWords(plainErrorText(`it hit ${raw} today`)), raw).toEqual([]);
    }
  });
});
