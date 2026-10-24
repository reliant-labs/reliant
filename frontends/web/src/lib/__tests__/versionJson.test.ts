import { execFileSync } from "node:child_process";
import { mkdtempSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import type { ConfigEnv, UserConfig } from "vite";
import { afterAll, beforeAll, describe, expect, it } from "vitest";

import {
  ALLOW_UNVERSIONED_ENV,
  RELEASE_IDENTITY_SCRIPT,
  VERSION_JSON_FILE,
  resolveBuild,
  unversionedBuildError,
  versionJson,
  type ResolveDeps,
} from "../../../vite-plugin-version-json";

const SHA = "10c676b6f2fa66a4b13e68f2f995289a7245b8f0";
const NOW = new Date("2026-10-11T18:47:32.000Z");

/** Deps that record the resolver's arguments and answer from a table. */
function fakeDeps(identity: string | Error, git: Record<string, string> = {}) {
  const calls: string[][] = [];
  const deps: ResolveDeps = {
    identity: (args) => {
      calls.push(args);
      if (identity instanceof Error) throw identity;
      return identity;
    },
    git: (args) => git[args.join(" ")] ?? "",
    now: () => NOW,
  };
  return { deps, calls };
}

function argValue(args: string[], flag: string): string | undefined {
  const i = args.indexOf(flag);
  return i === -1 ? undefined : args[i + 1];
}

// The deployed SPA rewrites unmatched paths to index.html, so a client-side
// "version" route would be served BY that rewrite, as HTML. A real file in the
// bundle root is what takes it out of the rewrite's reach.
describe("version.json", () => {
  it("is emitted at the bundle root so the SPA rewrite cannot shadow it", () => {
    expect(VERSION_JSON_FILE).toBe("version.json");
    expect(VERSION_JSON_FILE).not.toContain("/");
  });
});

describe("resolveBuild", () => {
  it("hands forge's GitSource pin and release to the resolver", () => {
    // forge fetches the pin shallow with no tags; the env is the only way a
    // tag-pinned build can learn its tag.
    const { deps, calls } = fakeDeps("1.8.1");
    const { info } = resolveBuild(
      {
        FORGE_SOURCE_REF: "v1.8.1",
        FORGE_SOURCE_COMMIT: SHA,
        FORGE_RELEASE: "20261011.184732-0b1d2c3e4f5a",
      } as NodeJS.ProcessEnv,
      deps,
    );
    expect(argValue(calls[0], "--ref")).toBe("v1.8.1");
    expect(argValue(calls[0], "--commit")).toBe(SHA);
    expect(info).toEqual({
      version: "1.8.1",
      commit: SHA,
      ref: "v1.8.1",
      release: "20261011.184732-0b1d2c3e4f5a",
      built: NOW.toISOString(),
    });
  });

  it("reads the checkout itself when no one says otherwise", () => {
    const { deps, calls } = fakeDeps("1.8.2-dev+10c676b6", {
      "rev-parse HEAD": SHA,
      "rev-parse --abbrev-ref HEAD": "main",
    });
    const { info } = resolveBuild({} as NodeJS.ProcessEnv, deps);
    // No --commit: the resolver must keep its dirty check for a working tree.
    expect(calls[0]).not.toContain("--commit");
    expect(info.version).toBe("1.8.2-dev+10c676b6");
    expect(info.commit).toBe(SHA);
    expect(info.ref).toBe("main");
    expect(info).not.toHaveProperty("release");
  });

  it("ignores GITHUB_SHA, which names control-plane's commit in its CI", () => {
    const { deps } = fakeDeps("1.8.2-dev+10c676b6", { "rev-parse HEAD": SHA });
    const { info } = resolveBuild({ GITHUB_SHA: "ffffffffffffffffffffffffffffffffffffffff" } as NodeJS.ProcessEnv, deps);
    expect(info.commit).toBe(SHA);
  });

  it("passes an explicit release tag through", () => {
    const { deps, calls } = fakeDeps("1.7.11");
    resolveBuild({ RELEASE_VERSION: "v1.7.11" } as NodeJS.ProcessEnv, deps);
    expect(argValue(calls[0], "--tag")).toBe("v1.7.11");
  });

  // forge injects its frontend BUILD ID (a content hash such as 657f69e51d5e)
  // into VITE_APP_VERSION — the variable telemetry reads service.version from.
  // It is not a release tag; handing it to --tag made release-identity.sh
  // refuse every forge-driven prod build.
  it("does not mistake forge's VITE_APP_VERSION build id for a release tag", () => {
    const { deps, calls } = fakeDeps("1.8.3-dev+431baca5");
    const { info } = resolveBuild(
      { VITE_APP_VERSION: "657f69e51d5e", FORGE_SOURCE_COMMIT: SHA } as NodeJS.ProcessEnv,
      deps,
    );
    expect(calls[0]).not.toContain("--tag");
    expect(argValue(calls[0], "--commit")).toBe(SHA);
    expect(info.version).toBe("1.8.3-dev+431baca5");
  });

  it("never reports a detached HEAD as the ref", () => {
    const { deps } = fakeDeps("1.8.2-dev+10c676b6", { "rev-parse --abbrev-ref HEAD": "HEAD" });
    expect(resolveBuild({} as NodeJS.ProcessEnv, deps).info.ref).toBe("unknown");
  });

  it("admits it does not know, and keeps the resolver's reason", () => {
    const failure = Object.assign(new Error("exit 1"), {
      stderr: "release-identity.sh: cannot name this build — no release tag and no commit\n",
    });
    const { deps } = fakeDeps(failure);
    const resolved = resolveBuild({} as NodeJS.ProcessEnv, deps);
    expect(resolved.info.version).toBe("unknown");
    expect(resolved.info.commit).toBe("unknown");
    expect(resolved.problem).toContain("cannot name this build");
  });
});

// The release guard: a shipped bundle must never say "unknown".
describe("unversioned production build", () => {
  const failure = Object.assign(new Error("exit 1"), { stderr: "no tag, no commit" });
  const buildEnv: ConfigEnv = { command: "build", mode: "production" };

  function runConfigHook(env: NodeJS.ProcessEnv, configEnv: ConfigEnv) {
    const plugin = versionJson(env, fakeDeps(failure).deps);
    const hook = plugin.config as (c: UserConfig, e: ConfigEnv) => UserConfig;
    return () => hook({}, configEnv);
  }

  it("fails the build", () => {
    expect(runConfigHook({} as NodeJS.ProcessEnv, buildEnv)).toThrow(/cannot name its own version/);
  });

  it("does not stop the dev server", () => {
    expect(runConfigHook({} as NodeJS.ProcessEnv, { command: "serve", mode: "development" })).not.toThrow();
  });

  it("can be waived for a build with no history", () => {
    expect(runConfigHook({ [ALLOW_UNVERSIONED_ENV]: "1" } as NodeJS.ProcessEnv, buildEnv)).not.toThrow();
  });

  it("lets a versioned build through and bakes its identity in", () => {
    const plugin = versionJson({} as NodeJS.ProcessEnv, fakeDeps("1.8.1").deps);
    const hook = plugin.config as (c: UserConfig, e: ConfigEnv) => UserConfig;
    const define = hook({}, buildEnv).define as Record<string, string>;
    expect(JSON.parse(define.__RELIANT_BUILD__).version).toBe("1.8.1");
    expect(unversionedBuildError(resolveBuild({} as NodeJS.ProcessEnv, fakeDeps("1.8.1").deps))).toBeNull();
  });
});

// scripts/release-identity.sh itself, against real git repositories shaped
// like the clones that build releases.
//
// Its own budget: vite.config.ts's timeout (5s × the host's load factor) is
// sized for in-process tests, but each test here is SUBPROCESS-bound — the
// shallow-clone case alone is a `git clone` plus four runs of a script that
// itself runs ~5 git commands, ~25 process spawns. On a shared host at load
// 2x the core count a spawn costs 0.3–0.6s, so that case measured 16s against
// a 15s budget and failed with every assertion correct. 60s covers the worst
// case seen with room to spare and still catches a genuine hang.
describe("release-identity.sh", { timeout: 60_000 }, () => {
  let root: string;
  let repo: string;
  let releaseSha: string;
  let devSha: string;
  // Hermetic git: no user or system config (signing, hooks, templates).
  const gitEnv = {
    ...process.env,
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_AUTHOR_NAME: "t",
    GIT_AUTHOR_EMAIL: "t@example.com",
    GIT_COMMITTER_NAME: "t",
    GIT_COMMITTER_EMAIL: "t@example.com",
  };
  const git = (cwd: string, ...args: string[]) =>
    execFileSync("git", args, { cwd, env: gitEnv, encoding: "utf8" }).trim();
  const identity = (...args: string[]) =>
    execFileSync("bash", [RELEASE_IDENTITY_SCRIPT, ...args], { env: gitEnv, encoding: "utf8" }).trim();
  const writePackage = (dir: string, version: string) => {
    mkdirSync(path.join(dir, "electron"), { recursive: true });
    writeFileSync(path.join(dir, "electron", "package.json"), JSON.stringify({ name: "reliant", version }, null, 2) + "\n");
  };

  beforeAll(() => {
    root = mkdtempSync(path.join(tmpdir(), "release-identity-"));
    repo = path.join(root, "reliant");
    mkdirSync(repo);
    git(repo, "init", "-q");
    writePackage(repo, "1.8.1");
    git(repo, "add", ".");
    git(repo, "commit", "-q", "-m", "chore: release v1.8.1");
    git(repo, "tag", "v1.8.1");
    releaseSha = git(repo, "rev-parse", "HEAD");
    writeFileSync(path.join(repo, "README"), "after the release\n");
    git(repo, "add", ".");
    git(repo, "commit", "-q", "-m", "fix: something");
    devSha = git(repo, "rev-parse", "HEAD");
  });

  afterAll(() => {
    rmSync(root, { recursive: true, force: true });
  });

  it("names a build at a tag by the tag", () => {
    expect(identity("--repo", repo, "--commit", releaseSha)).toBe("1.8.1");
  });

  it("names a build at a later commit as the next patch's dev build", () => {
    expect(identity("--repo", repo)).toBe(`1.8.2-dev+${devSha.slice(0, 8)}`);
  });

  it("works in a shallow, tagless clone — how forge and actions/checkout fetch", () => {
    const shallow = path.join(root, "shallow");
    git(root, "clone", "-q", "--depth", "1", "--no-tags", `file://${repo}`, shallow);
    expect(identity("--repo", shallow)).toBe(`1.8.2-dev+${devSha.slice(0, 8)}`);
    // …and forge's ref is what lets a tag-pinned shallow build name its tag.
    expect(identity("--repo", shallow, "--ref", "v1.8.1", "--commit", devSha)).toBe("1.8.1");
    // A sha or branch ref is not a version and must not be taken for one.
    expect(identity("--repo", shallow, "--ref", devSha, "--commit", devSha)).toBe(`1.8.2-dev+${devSha.slice(0, 8)}`);
    expect(identity("--repo", shallow, "--ref", "main")).toBe(`1.8.2-dev+${devSha.slice(0, 8)}`);
  });

  it("follows a prerelease to its own version, not past it", () => {
    const rc = path.join(root, "rc");
    mkdirSync(rc);
    git(rc, "init", "-q");
    writePackage(rc, "1.8.2-rc1");
    git(rc, "add", ".");
    git(rc, "commit", "-q", "-m", "rc");
    const sha = git(rc, "rev-parse", "HEAD");
    expect(identity("--repo", rc)).toBe(`1.8.2-dev+${sha.slice(0, 8)}`);
  });

  it("marks a dirty working tree, but not a commit named explicitly", () => {
    writeFileSync(path.join(repo, "README"), "edited\n");
    try {
      expect(identity("--repo", repo)).toBe(`1.8.2-dev+${devSha.slice(0, 8)}.dirty`);
      expect(identity("--repo", repo, "--commit", devSha)).toBe(`1.8.2-dev+${devSha.slice(0, 8)}`);
    } finally {
      git(repo, "checkout", "-q", "--", "README");
    }
  });

  it("--commit-time prints the committer time of the commit, the build number that orders builds", () => {
    const committed = (sha: string) => git(repo, "show", "-s", "--format=%ct", sha);
    expect(identity("--commit-time", "--repo", repo)).toBe(committed(devSha));
    expect(identity("--commit-time", "--repo", repo, "--commit", releaseSha)).toBe(committed(releaseSha));
    expect(Number(identity("--commit-time", "--repo", repo))).toBeGreaterThan(0);
  });

  it("--commit-time is 0 (unordered) for a dirty tree, an unknown commit and a non-repo — never an error", () => {
    writeFileSync(path.join(repo, "README"), "edited\n");
    try {
      expect(identity("--commit-time", "--repo", repo)).toBe("0");
      // Naming the commit describes a commit, not the working tree.
      expect(identity("--commit-time", "--repo", repo, "--commit", devSha)).not.toBe("0");
    } finally {
      git(repo, "checkout", "-q", "--", "README");
    }
    expect(identity("--commit-time", "--repo", repo, "--commit", "deadbeef")).toBe("0");
    const empty = path.join(root, "not-a-repo");
    mkdirSync(empty);
    expect(identity("--commit-time", "--repo", empty)).toBe("0");
  });

  it("works in a shallow, tagless clone", () => {
    const shallow = path.join(root, "shallow-ct");
    git(root, "clone", "-q", "--depth", "1", "--no-tags", `file://${repo}`, shallow);
    expect(identity("--commit-time", "--repo", shallow)).toBe(git(repo, "show", "-s", "--format=%ct", devSha));
  });

  it("refuses when it can name nothing", () => {
    const empty = path.join(root, "empty");
    mkdirSync(empty);
    expect(() => identity("--repo", empty)).toThrow();
  });
});
