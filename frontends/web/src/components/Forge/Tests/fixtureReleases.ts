// Copyright (c) 2026 Reliant Labs

/**
 * shopfront's releases as every fixture names them: each version and the
 * commit it was cut from. ONE table, read by the Tests fixtures (the trend's
 * release markers, the runs, GetReleaseTests) and by the visual harness's
 * DeployService fixtures (e2e/visual/fixtures/services.ts ListReleases,
 * releases.ts GetRelease), so a release's header and its Tests tab name the
 * same commit.
 *
 * A leaf: it imports nothing, so every fixture module can read it at load
 * time whatever order they are loaded in.
 *
 * A version ends in the short sha of its commit, except staging's: staging
 * runs main's newest commit (the Tests fixtures' FIXTURE_COMMIT, 9f3c…), and
 * its version is the one the console fixtures have always used.
 */

export const FIXTURE_RELEASE_VERSIONS = {
  staging: "20261118.144000-f6e5d4c3b2a1",
  prod: "20261118.121200-a1b2c3d4e5f6",
  partial: "20261117.090000-0badc0ffee00",
  untested: "20261116.160500-5eed5eed5eed",
  first: "20261105.093000-77e0c1d2a3b4",
} as const;

export type FixtureRelease = keyof typeof FIXTURE_RELEASE_VERSIONS;

export const FIXTURE_RELEASE_COMMITS: Record<FixtureRelease, string> = {
  staging: "9f3c2a17be0d4c55a1f0e9d8c7b6a5f4e3d2c1b0",
  prod: "a1b2c3d4e5f60718293a4b5c6d7e8f9012a3b4c5",
  partial: "0badc0ffee0012ab34cd56ef7890ab12cd34ef56",
  untested: "5eed5eed5eed40f1e2d3c4b5a69788796a5b4c3d",
  first: "77e0c1d2a3b4c5d6e7f8091a2b3c4d5e6f708192",
};

/** The commit `version` was cut from, or "" for a version this table does not hold. */
export function fixtureReleaseCommit(version: string): string {
  const key = (Object.keys(FIXTURE_RELEASE_VERSIONS) as FixtureRelease[]).find((candidate) => FIXTURE_RELEASE_VERSIONS[candidate] === version);
  return key ? FIXTURE_RELEASE_COMMITS[key] : "";
}
