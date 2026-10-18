// node --test .github/scripts/*.test.mjs
//
// The decision logic of check-release-health.mjs, on the two runs that define
// it: v1.8.1's failed release (run 38138480957, which nothing reported for a
// week) and v1.7.16's successful one (run 36260756107). Fixtures are trimmed
// from `gh run view --json`. Nothing here talks to GitHub or the network.

import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import {
  PRERELEASE_TAG,
  compareVersions,
  failedJobs,
  feedFiles,
  issueTitle,
  judge,
  mergeRun,
  parseFeedVersion,
  pickRun,
  releaseChannel,
  renderReport,
  runUrl,
} from "./check-release-health.mjs";

const REPO_ROOT = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const RELEASE_YML = readFileSync(join(REPO_ROOT, ".github", "workflows", "release.yml"), "utf8");

const RUNS = "https://github.com/reliant-labs/reliant/actions/runs";

const v181 = {
  attempt: 1,
  databaseId: 38138480957,
  conclusion: "failure",
  status: "completed",
  event: "push",
  headBranch: "v1.8.1",
  headSha: "6cef8aa859685e4dcfc3c1f36839a5183d96dd28",
  createdAt: "2026-10-11T12:13:59Z",
  url: `${RUNS}/38138480957`,
};

const v181Jobs = [
  {
    name: "Build Web",
    conclusion: "failure",
    databaseId: 114467194235,
    url: `${RUNS}/38138480957/job/114467194235`,
    steps: [
      { number: 13, name: "Build React frontend", conclusion: "success" },
      { number: 14, name: "Verify the built bundle", conclusion: "failure" },
      { number: 15, name: "Upload web build artifact", conclusion: "skipped" },
    ],
  },
  { name: "Build macOS", conclusion: "skipped", databaseId: 114467439202, url: `${RUNS}/38138480957/job/114467439202`, steps: [] },
  { name: "Finalize Release", conclusion: "skipped", databaseId: 114467439231, url: `${RUNS}/38138480957/job/114467439231`, steps: [] },
];

const v1716 = {
  attempt: 1,
  databaseId: 36260756107,
  conclusion: "success",
  status: "completed",
  event: "push",
  headBranch: "v1.7.16",
  headSha: "431bc8123af03979d4e859ae9bfac616331cdfd5",
  createdAt: "2026-09-26T17:55:49Z",
  url: `${RUNS}/36260756107`,
};

const dispatch = {
  ...v1716,
  databaseId: 39167760349,
  event: "workflow_dispatch",
  headBranch: "main",
  createdAt: "2026-10-18T21:25:19Z",
};

const feedsServing = (version, channel = "latest") =>
  feedFiles(channel).map((file) => ({
    file,
    url: `https://downloads.reliantlabs.io/${file}`,
    status: 200,
    version,
  }));

// ── Which run is judged ─────────────────────────────────────────────────────

test("the newest tag run with a verdict is judged", () => {
  assert.equal(pickRun([v1716, v181]).databaseId, v181.databaseId);
});

test("a workflow_dispatch run (a dry run or a rebuild of main) is never judged", () => {
  // The real history: the newest release.yml run before v1.8.2 was a green
  // dispatch on main. It must not stand in for v1.8.1's red tag run.
  assert.equal(pickRun([dispatch, v181, v1716]).databaseId, v181.databaseId);
  assert.equal(pickRun([dispatch]), null);
});

test("a cancelled or unfinished tag run is passed over: the previous verdict stands", () => {
  const cancelled = { ...v181, databaseId: 1, headBranch: "v1.8.2", conclusion: "cancelled", createdAt: "2026-10-12T00:00:00Z" };
  const running = { ...v181, databaseId: 2, headBranch: "v1.8.3", status: "in_progress", conclusion: "", createdAt: "2026-10-13T00:00:00Z" };
  assert.equal(pickRun([cancelled, running, v181, v1716]).databaseId, v181.databaseId);
});

test("a push run that is not a vX.Y.Z tag is not a release", () => {
  const branch = { ...v181, databaseId: 3, headBranch: "main", createdAt: "2026-10-14T00:00:00Z" };
  assert.equal(pickRun([branch, v1716]).databaseId, v1716.databaseId);
});

test("the run that fired the audit counts even while the run list still shows it in progress", () => {
  const listed = { ...v181, status: "in_progress", conclusion: "" };
  // Without the merge the previous, green, tag would be judged.
  assert.equal(pickRun([listed, v1716]).databaseId, v1716.databaseId);
  assert.equal(pickRun(mergeRun([listed, v1716], v181)).databaseId, v181.databaseId);
});

test("a successful re-run of the failed tag is what gets judged", () => {
  const rerun = { ...v181, attempt: 2, conclusion: "success" };
  const run = pickRun(mergeRun([v181, v1716], rerun));
  assert.equal(run.attempt, 2);
  assert.equal(judge(run, null).verdict, "green");
});

// ── The verdict ─────────────────────────────────────────────────────────────

test("v1.8.1 is red, and the report names the tag, the run and the failed step", () => {
  const { verdict, reasons } = judge(v181, null);
  assert.equal(verdict, "red");

  const failed = failedJobs(v181Jobs);
  assert.deepEqual(
    failed.map((job) => [job.name, job.steps.map((step) => step.name)]),
    [["Build Web", ["Verify the built bundle"]]],
    "skipped jobs and steps are not failures",
  );

  const message =
    "verify-bundle: bundle still contains the same-origin transport branch — import.meta.env.DEV was TRUE at build time (a leaked NODE_ENV=development)";
  const body = renderReport({ run: v181, verdict, reasons, failed, annotations: { 114467194235: [message] } });

  assert.equal(issueTitle(v181), "Release v1.8.1 did not ship");
  assert.match(body, /\*\*v1\.8\.1 did not ship\*\*: its `Release Electron App` run concluded `failure`/);
  assert.ok(body.includes(`(${RUNS}/38138480957/attempts/1)`), "links the attempt in the form the workflow greps for");
  assert.ok(body.includes(`[Build Web](${RUNS}/38138480957/job/114467194235)`));
  assert.ok(body.includes(`step 14: [Verify the built bundle](${RUNS}/38138480957/job/114467194235#step:14:1)`));
  assert.ok(body.includes(`error: ${message}`));
  assert.match(body, /re-run the failed jobs/);
});

test("v1.7.16 is green, and so is the closing comment", () => {
  const { verdict } = judge(v1716, null);
  assert.equal(verdict, "green");
  const body = renderReport({ run: v1716, verdict });
  assert.equal(body, `**v1.7.16 shipped**: [run 36260756107, attempt 1](${RUNS}/36260756107/attempts/1) succeeded.`);
});

test("each attempt is its own report, so a second failure of one tag is not deduplicated away", () => {
  assert.notEqual(runUrl(v181), runUrl({ ...v181, attempt: 2 }));
});

// ── The update feeds (--check-feeds) ────────────────────────────────────────

test("a green run whose feeds serve its version is green", () => {
  assert.equal(judge(v1716, feedsServing("1.7.16")).verdict, "green");
});

test("a green run whose feeds still serve the previous version is red", () => {
  const { verdict, reasons } = judge(v1716, feedsServing("1.7.15"));
  assert.equal(verdict, "red");
  assert.match(reasons[0], /latest-mac\.yml serves `1\.7\.15`, not `1\.7\.16`/);
});

test("a feed already serving a LATER release is not stale", () => {
  // A hotfix tagged after a newer release, or a weekly re-audit after the
  // next release shipped.
  assert.equal(judge(v1716, feedsServing("1.8.2")).verdict, "green");
});

test("a missing feed is red; an unreachable one is undecided, never green", () => {
  const feeds = feedsServing("1.7.16");
  assert.equal(judge(v1716, [{ ...feeds[0], status: 404, version: null }, ...feeds.slice(1)]).verdict, "red");
  assert.equal(judge(v1716, [{ file: feeds[0].file, url: feeds[0].url, error: "ETIMEDOUT" }, ...feeds.slice(1)]).verdict, "undecided");
});

test("a red run stays red whatever the feeds say", () => {
  assert.equal(judge(v181, feedsServing("1.8.1")).verdict, "red");
});

test("feed versions parse as electron-builder writes them", () => {
  assert.equal(parseFeedVersion("version: 1.8.2\nfiles:\n  - url: Reliant-1.8.2-mac-x64.zip\n"), "1.8.2");
  assert.equal(parseFeedVersion("version: '1.6.4-rc1'\n"), "1.6.4-rc1");
  assert.equal(parseFeedVersion("<html>not a feed</html>"), null);
});

test("versions compare numerically, with a prerelease below its release", () => {
  assert.ok(compareVersions("1.7.16", "1.7.9") > 0);
  assert.ok(compareVersions("1.8.2", "1.8.2") === 0);
  assert.ok(compareVersions("1.6.4-rc1", "1.6.4") < 0);
  assert.ok(compareVersions("1.6.4-rc10", "1.6.4-rc2") > 0);
  assert.equal(compareVersions(null, "1.8.2"), null);
});

// ── Pinned to release.yml, so a change there cannot silently desynchronise ─

test("the prerelease regex is release.yml's, verbatim", () => {
  const m = /\$TAG_NAME =~ (\S+) \]\]/.exec(RELEASE_YML);
  assert.ok(m, 'release.yml no longer detects a prerelease with `[[ $TAG_NAME =~ ... ]]`; update PRERELEASE_TAG with it');
  assert.equal(PRERELEASE_TAG.source, m[1]);
  assert.equal(releaseChannel("v1.8.2"), "latest");
  assert.equal(releaseChannel("v1.6.4-rc1"), "alpha");
});

test("the feeds read are the manifests release.yml's Finalize purges", () => {
  for (const channel of ["latest", "alpha"]) {
    for (const file of feedFiles(channel)) {
      assert.ok(
        RELEASE_YML.includes(`"\${BASE_URL}/${file}"`),
        `release.yml's CDN purge does not name ${file}; the update feeds were renamed, so update feedFiles()`,
      );
    }
  }
});
