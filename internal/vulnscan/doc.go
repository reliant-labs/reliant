// Package vulnscan scans SBOMs for known vulnerabilities with grype, keeps the
// one grype database every forge process on a machine shares, and states the
// vuln_gate rule as a pure function.
//
// # The pieces
//
//   - [DB] is the shared vulnerability database: where it lives, when it is
//     refreshed, how an offline machine imports one, and when it is too old
//     to trust ([StaleAfter]: a verdict computed against it is "stale",
//     never a pass).
//   - [Scanner] runs grype over one source — an SBOM file (the normal case),
//     a directory or an image — against that database and returns a [Scan]:
//     typed [Finding]s plus the {scanner, version, db_schema, db_built_at}
//     record that says how fresh the answer is.
//   - [Judge] and [Evaluate] are the vuln_gate rule
//     (release.ShapeVulnGate): which findings block, which warn, and the
//     verdict. They enforce nothing and grade no rollout; the caller decides
//     what a verdict does (forge vuln's exit code today, the promotion gate
//     later).
//   - [Document] is the versioned `forge vuln list --json` document,
//     schema forge.dev/vuln-list/v1, assembled by [BuildDocument].
//
// # The database
//
// One database per machine, never one per project: it is 205 MB to download,
// 3.3 GiB unpacked, and about two minutes of CPU to import. It lives at
//
//	$FORGE_VULN_DB_DIR (default <UserCacheDir>/forge/vulndb)/
//	  lock          flock: scans share it, an update or import holds it alone
//	  grype.yaml    an empty grype config, so no user or project .grype.yaml
//	                (an `ignore:` rule, `only-fixed`) changes a verdict
//	  state.json    forge's record of the last listing check
//	  grype/        GRYPE_DB_CACHE_DIR: grype's own files (6/vulnerability.db)
//
// grype never downloads anything during a scan: every invocation runs with
// GRYPE_DB_AUTO_UPDATE=false, and the refresh is forge's own step ([DB.Ensure]).
// That step reads the listing (<url>/v6/latest.json, a few hundred bytes),
// and only when it names a build newer than the local one runs
// `grype db update`, which downloads the archive the listing names and
// verifies it against the listing's sha256 before importing it. So the
// checksum check is grype's, against the listing it was pointed at.
// FORGE_VULN_DB_URL points both at a mirror serving the same layout;
// `forge vuln db import <file>` loads an archive on a machine with no network.
//
// A database older than [RefreshAfter] is refreshed by the next command
// allowed to fetch (`forge vuln scan`, `forge vuln db pull`); `forge vuln
// list` and `show` never download. One older than [StaleAfter] is refused by
// grype itself (forge never disables grype's age check), and every verdict
// computed against it is "stale".
//
// # Caching it in CI
//
// Cache the directory ($FORGE_VULN_DB_DIR) keyed by the database's build
// date: `forge vuln db status --json` prints it as `cache_key`
// (forge-vulndb-v6-<YYYY-MM-DD>). A job that restores yesterday's cache
// (restore-keys: forge-vulndb-v6-) refreshes it only when a newer build is
// published, which is a 205 MB download at most once a day.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: the one outbound call is the database listing read, and every input it reaches out with (URL, Client, Grype, Run, Dir) is already an injectable DB field the tests point at an httptest server and a fake grype, as internal/toolfetch does; a contract.go Service would be one implementation with no substitution point, and a consumer (the promotion gate, the control-plane read) declares its own narrow interface over Scan or Ensure
//forge:exclude-contract: a CLI-driven scanner and machine-wide database cache, not an injected application component
package vulnscan
