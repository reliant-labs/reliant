#!/usr/bin/env node
// Did the newest release tag actually ship?
//
// ## Why this exists
//
// v1.8.1 was tagged on 2026-10-11 and its "Release Electron App" run failed at
// "Verify the built bundle". Nothing said so. A red tag run notifies only
// whoever pushed the tag, the tag itself looks released, and
// downloads.reliantlabs.io simply went on serving the previous version: 1.7.16,
// for a week, to every new download, against a server that had already retired
// the RPC 1.7.16 sends first.
//
// This script produces the verdict; .github/workflows/release-health.yml turns
// it into ONE `release:failed` issue that stays open until a later tag ships.
//
// ## What counts as red
//
// It reads the newest COMPLETED release.yml run for a TAG PUSH that has a
// verdict, and reports red when:
//
//   - the run's conclusion is anything but success, or
//   - with --check-feeds, the run succeeded but an update feed clients poll
//     (<channel>-mac.yml, <channel>.yml, <channel>-linux.yml: the manifests
//     release.yml's Finalize purges) serves a version OLDER than the tag's.
//     Finalize already fails when a manifest is MISSING, but it never reads
//     one, and it only warns when the CDN purge is refused, so a green run
//     whose feed still names the old version would pass. A newer version is
//     fine: a later tag already shipped there.
//     --feed-wait N re-reads for up to N seconds first, because the run's
//     completion races the CDN purge it has just issued.
//
// Passed over: a cancelled run (no verdict; the previous one stands) and any
// workflow_dispatch run (a dry run, or a rebuild: not a tag release). "Newest"
// is by run creation, i.e. by tag push; a re-run keeps its run's place, so a
// successful re-run of the failed tag turns it green.
//
// ## Usage
//
//   node .github/scripts/check-release-health.mjs
//   node .github/scripts/check-release-health.mjs --check-feeds --feed-wait 300 --report /tmp/body.md
//   node .github/scripts/check-release-health.mjs --run 38138480957 --json
//
// --run judges that one run instead of the newest (a dry run of the verdict).
// --with-run <id> is the workflow_run mode: judge the newest as usual, but
// count run <id> as completed even if the run list has not caught up with it.
// Read-only in every mode: it never writes to GitHub. Exits 0 when green, 1 when red, 2 when it could not
// decide (gh failed, no tag run with a verdict, --run is not a tag release, a
// feed unreachable). Under GitHub Actions it also writes `verdict`
// (green|red), `tag`, `run_url` and `title` to $GITHUB_OUTPUT, so a caller can
// tell "the release is red" from "the audit itself broke".

import { execFileSync } from "node:child_process";
import { appendFileSync, writeFileSync } from "node:fs";
import { setTimeout as sleep } from "node:timers/promises";
import { pathToFileURL } from "node:url";

/** Conclusions that are a verdict on the release. Anything else is passed over. */
export const VERDICT_CONCLUSIONS = new Set(["success", "failure", "timed_out", "startup_failure", "action_required"]);

/** release.yml's trigger, `tags: ["v*.*.*"]`, matched against the run's head branch (the tag). */
const TAG_SHAPE = /^v\d+\.\d+\.\d+/;

/**
 * release.yml's "Detect release type" regex, verbatim: a tag that matches
 * publishes to the alpha channel. check-release-health.test.mjs pins it.
 */
export const PRERELEASE_TAG = /-rc\.|rc[0-9]+|-beta\.|beta[0-9]+|-alpha\.|alpha[0-9]+/;

export const FEED_BASE = "https://downloads.reliantlabs.io";

const OK_CONCLUSIONS = new Set(["success", "skipped", "neutral"]);
const RUN_FIELDS = "attempt,databaseId,conclusion,status,event,headBranch,headSha,createdAt,url,displayTitle";
const FEED_POLL_MS = 30_000;
const WITH_RUN_TRIES = 6;
const WITH_RUN_POLL_MS = 10_000;

export function isTagRelease(run) {
  return run.event === "push" && TAG_SHAPE.test(run.headBranch ?? "");
}

export function hasVerdict(run) {
  return run.status === "completed" && VERDICT_CONCLUSIONS.has(run.conclusion);
}

/**
 * The run list with `fresh` (the run whose completion fired this audit, read
 * directly) in place of whatever the list said about it. The list can lag a
 * just-completed run by seconds and still show it in progress; passed over,
 * the PREVIOUS tag would be judged instead, and a failure followed by an older
 * green run would read as green: the exact silence this exists to end.
 */
export function mergeRun(runs, fresh) {
  return [...runs.filter((run) => run.databaseId !== fresh.databaseId), fresh];
}

/** The newest tag-push run with a verdict, or null. */
export function pickRun(runs) {
  return (
    runs
      .filter((run) => isTagRelease(run) && hasVerdict(run))
      .sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt))[0] ?? null
  );
}

/**
 * Attempt-qualified: a re-run is the same run id, and a second failure of the
 * same tag is news the issue has to hear about.
 */
export function runUrl(run) {
  return `${run.url}/attempts/${run.attempt ?? 1}`;
}

export function releaseChannel(tag) {
  return PRERELEASE_TAG.test(tag) ? "alpha" : "latest";
}

/** The manifests Finalize purges: macOS, Windows (named after the channel alone), Linux x64, Linux arm64. */
export function feedFiles(channel) {
  return [`${channel}-mac.yml`, `${channel}.yml`, `${channel}-linux.yml`, `${channel}-linux-arm64.yml`];
}

export function parseFeedVersion(text) {
  const m = /^version:\s*["']?([^"'\s]+)["']?\s*$/m.exec(text ?? "");
  return m ? m[1] : null;
}

/** <0, 0, >0 like a comparator; null when either side is not x.y.z[-pre]. A prerelease sorts below its release. */
export function compareVersions(a, b) {
  const parse = (v) => {
    const m = /^v?(\d+)\.(\d+)\.(\d+)(?:-?(.+))?$/.exec(v ?? "");
    return m ? { core: [m[1], m[2], m[3]].map(Number), pre: m[4] ?? "" } : null;
  };
  const x = parse(a);
  const y = parse(b);
  if (!x || !y) return null;
  for (let i = 0; i < 3; i++) if (x.core[i] !== y.core[i]) return x.core[i] - y.core[i];
  if (x.pre === y.pre) return 0;
  if (!x.pre) return 1;
  if (!y.pre) return -1;
  return x.pre.localeCompare(y.pre, "en", { numeric: true });
}

/** Jobs that did not succeed, each with the steps that did not. */
export function failedJobs(jobs) {
  const bad = (x) => x.conclusion && !OK_CONCLUSIONS.has(x.conclusion);
  return (jobs ?? []).filter(bad).map((job) => ({
    name: job.name,
    url: job.url,
    conclusion: job.conclusion,
    databaseId: job.databaseId,
    steps: (job.steps ?? []).filter(bad).map(({ name, number, conclusion }) => ({ name, number, conclusion })),
  }));
}

/**
 * @param run   the release run being judged
 * @param feeds null when the feeds were not read, else [{file, url, status, version} | {file, url, error}]
 * @returns {{verdict: "green"|"red"|"undecided", reasons: string[]}}
 */
export function judge(run, feeds) {
  if (run.conclusion !== "success") {
    return { verdict: "red", reasons: [`its \`Release Electron App\` run concluded \`${run.conclusion}\``] };
  }
  if (!feeds) return { verdict: "green", reasons: [] };

  const expected = run.headBranch.replace(/^v/, "");
  const stale = [];
  const unreachable = [];
  for (const feed of feeds) {
    if (feed.error) unreachable.push(`${feed.url}: ${feed.error}`);
    else if (feed.status !== 200) stale.push(`${feed.url} answers HTTP ${feed.status}`);
    // A NEWER version is a later release that already shipped, not a stale feed.
    else if (!(compareVersions(feed.version, expected) >= 0)) {
      stale.push(`${feed.url} serves \`${feed.version ?? "no version line"}\`, not \`${expected}\``);
    }
  }
  if (stale.length) return { verdict: "red", reasons: [`its run succeeded, but ${stale.join("; ")}`] };
  if (unreachable.length) return { verdict: "undecided", reasons: unreachable };
  return { verdict: "green", reasons: [] };
}

export function issueTitle(run) {
  return `Release ${run.headBranch} did not ship`;
}

const GUIDANCE =
  "Until a later tag's release succeeds, every new download and every auto-update still gets the " +
  "previous release. If the cause was transient, re-run the failed jobs: a successful re-run closes " +
  "this issue. Otherwise fix it on `main` and push a new tag. A `workflow_dispatch` run of release.yml " +
  "does not count. This issue is opened, updated and closed by `.github/workflows/release-health.yml`.";

/**
 * The issue body (red) or closing comment (green). The attempt URL appears as
 * `(<url>)`, which is the token release-health.yml greps to avoid reporting the
 * same attempt twice.
 */
export function renderReport({ run, verdict, reasons = [], failed = [], annotations = {}, feeds = null }) {
  const tag = run.headBranch;
  const link = `[run ${run.databaseId}, attempt ${run.attempt ?? 1}](${runUrl(run)})`;
  const lines =
    verdict === "green"
      ? [`**${tag} shipped**: ${link} succeeded.`]
      : [
          `**${tag} did not ship**: ${reasons.join("; ")}.`,
          "",
          `- Run: ${link}`,
          `- Tag: \`${tag}\` at \`${(run.headSha ?? "").slice(0, 12)}\`, run created ${run.createdAt}`,
        ];

  if (failed.length) {
    lines.push("", "Failed jobs and steps:");
    for (const job of failed) {
      lines.push(`- [${job.name}](${job.url}) — \`${job.conclusion}\``);
      for (const step of job.steps) {
        lines.push(`  - step ${step.number}: [${step.name}](${job.url}#step:${step.number}:1) — \`${step.conclusion}\``);
      }
      for (const message of annotations[job.databaseId] ?? []) lines.push(`  - error: ${message}`);
    }
  }

  if (feeds) {
    lines.push("", `What the update feeds serve on the \`${releaseChannel(tag)}\` channel now:`);
    for (const feed of feeds) {
      const state = feed.error
        ? `unreachable (${feed.error})`
        : feed.status !== 200
          ? `HTTP ${feed.status}`
          : `\`${feed.version ?? "no version line"}\``;
      lines.push(`- [\`${feed.file}\`](${feed.url}): ${state}`);
    }
  }

  if (verdict !== "green") lines.push("", GUIDANCE);
  return lines.join("\n");
}

async function readFeed(base, file) {
  const url = `${base}/${file}`;
  try {
    const res = await fetch(url, { signal: AbortSignal.timeout(20_000) });
    const text = res.ok ? await res.text() : "";
    return { file, url, status: res.status, version: parseFeedVersion(text) };
  } catch (err) {
    return { file, url, error: String(err?.cause?.message ?? err?.message ?? err) };
  }
}

function readFeeds(base, tag) {
  return Promise.all(feedFiles(releaseChannel(tag)).map((file) => readFeed(base, file)));
}

async function main() {
  const args = process.argv.slice(2);
  const argValue = (flag, fallback) => {
    const i = args.indexOf(flag);
    return i !== -1 && args[i + 1] !== undefined ? args[i + 1] : fallback;
  };

  const workflow = argValue("--workflow", "release.yml");
  const runId = argValue("--run", "");
  const withRun = argValue("--with-run", "");
  const runCount = Number(argValue("--runs", "30"));
  const reportPath = argValue("--report", "");
  const repo = argValue("--repo", "");
  const feedBase = argValue("--feed-base", FEED_BASE);
  const feedWaitMs = Number(argValue("--feed-wait", "0")) * 1000;
  const checkFeeds = args.includes("--check-feeds");
  const asJson = args.includes("--json");

  const undecided = (message) => {
    console.error(`release-health: ${message}`);
    process.exit(2);
  };
  const gh = (ghArgs) => {
    const full = repo ? [...ghArgs, "--repo", repo] : ghArgs;
    try {
      return execFileSync("gh", full, { encoding: "utf8", maxBuffer: 32 * 1024 * 1024 });
    } catch (err) {
      undecided(`\`gh ${full.join(" ")}\` failed:\n${(err.stderr || err.message || "").toString().trim()}`);
    }
  };
  const setOutput = (name, value) => {
    if (process.env.GITHUB_OUTPUT) appendFileSync(process.env.GITHUB_OUTPUT, `${name}=${value}\n`);
  };

  let run;
  if (runId) {
    run = JSON.parse(gh(["run", "view", runId, "--json", `${RUN_FIELDS},jobs`]));
    if (!isTagRelease(run)) {
      undecided(`run ${runId} is a ${run.event} run on \`${run.headBranch}\`, not a tag push; only tag releases are judged.`);
    }
    if (!hasVerdict(run)) undecided(`run ${runId} has no verdict (${run.status}, ${run.conclusion || "no conclusion"}).`);
  } else {
    let runs = JSON.parse(
      gh(["run", "list", "--workflow", workflow, "--event", "push", "--limit", String(runCount), "--json", RUN_FIELDS]),
    );
    if (withRun) {
      let fresh;
      for (let attempt = 1; ; attempt++) {
        fresh = JSON.parse(gh(["run", "view", withRun, "--json", RUN_FIELDS]));
        if (fresh.status === "completed" || attempt >= WITH_RUN_TRIES) break;
        await sleep(WITH_RUN_POLL_MS);
      }
      if (fresh.status !== "completed") undecided(`run ${withRun} still reads as ${fresh.status} after it completed.`);
      runs = mergeRun(runs, fresh);
    }
    run = pickRun(runs);
    if (!run) {
      undecided(`no completed tag-push ${workflow} run with a verdict in the last ${runCount}.`);
    }
    run.jobs = JSON.parse(gh(["run", "view", String(run.databaseId), "--json", "jobs"])).jobs ?? [];
  }

  // Best-effort: the `::error::` text is what makes the issue self-explanatory
  // ("verify-bundle: bundle still contains the same-origin transport branch"),
  // but failing to read it must not cost the verdict.
  const failed = failedJobs(run.jobs);
  const annotations = {};
  const apiRepo = repo || process.env.GITHUB_REPOSITORY || "{owner}/{repo}";
  for (const job of failed) {
    try {
      const raw = execFileSync("gh", ["api", `repos/${apiRepo}/check-runs/${job.databaseId}/annotations`], {
        encoding: "utf8",
      });
      annotations[job.databaseId] = JSON.parse(raw)
        .filter((a) => a.annotation_level === "failure" && !/^Process completed with exit code \d+\.?$/.test(a.message))
        .map((a) => a.message.replace(/\s+/g, " ").slice(0, 400));
    } catch {
      // Leave this job without annotations.
    }
  }

  let feeds = null;
  if (checkFeeds) {
    feeds = await readFeeds(feedBase, run.headBranch);
    const deadline = Date.now() + feedWaitMs;
    // Only a green run is worth waiting for: a red one is red whatever the feeds say.
    while (run.conclusion === "success" && judge(run, feeds).verdict !== "green" && Date.now() + FEED_POLL_MS <= deadline) {
      console.error(`release-health: feeds do not serve ${run.headBranch} yet; re-reading in ${FEED_POLL_MS / 1000}s`);
      await sleep(FEED_POLL_MS);
      feeds = await readFeeds(feedBase, run.headBranch);
    }
  }

  const { verdict, reasons } = judge(run, feeds);
  if (verdict === "undecided") undecided(`could not read the update feeds:\n  ${reasons.join("\n  ")}`);

  const body = renderReport({ run, verdict, reasons, failed, annotations, feeds });
  if (reportPath) writeFileSync(reportPath, `${body}\n`);
  setOutput("verdict", verdict);
  setOutput("tag", run.headBranch);
  setOutput("run_url", runUrl(run));
  setOutput("title", issueTitle(run));

  if (asJson) {
    const { jobs: _jobs, ...summary } = run;
    console.log(JSON.stringify({ verdict, reasons, run: summary, failed, annotations, feeds }, null, 2));
  } else {
    console.log(body);
  }
  process.exit(verdict === "red" ? 1 : 0);
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  await main();
}
