---
name: vuln/gate
description: `forge vuln gate` — SBOM local build output (a directory, a `docker save` image tarball, a binary, or SBOMs your build made) and judge it by a `vuln_gate` with no release, no ledger and no network, against a vulnerability database mounted read-only (`--db`); the policy from an env (`--env`), a file (`--policy`) or the defaults with forge.yaml's exemptions; the `--out` SBOM files and `vuln-gate.json`; the verdict, reason code, rule per finding and exemptions applied; and exit codes 0/1/2. Use when a merge gate's SBOM + vulnerability-scan stage runs inside a sandbox, or for a repo forge does not deploy.
---

# Local build output, offline: `forge vuln gate`

`forge vuln gate` is the SBOM + vulnerability-scan stage of a merge gate, and the
same verdict for a repo forge does not deploy. It records nothing (no release, no
ledger): it describes each input with the cut's own SBOM generator, scans each SBOM,
and judges the findings by a `vuln_gate`.

```bash
forge vuln gate dist/ api.tar --sbom web=web.cdx.json --db /mnt/vulndb --out out/ --json
```

| Input | Read as |
|---|---|
| a directory | syft's directory scan (lockfiles, manifests, binaries) |
| an image tarball (`docker save`, or an OCI layout archive) | the image, from the file: no daemon, no registry |
| any other file | a binary or lockfile |
| `--sbom [<name>=]<file>` (repeatable) | an SBOM your build made, CycloneDX or SPDX JSON, scanned as given |

Each input is one artifact, named by its base name or `<name>=<path>`.

- **The database.** `--db <dir>` is a database directory in `$FORGE_VULN_DB_DIR`'s
  layout (the one `forge vuln db pull` keeps), used **read-only and offline**: nothing
  is written into it, nothing is fetched, and syft and grype are never installed, so
  run `forge tools install syft grype` when the image is built. `--offline` is the
  same rule over this machine's database. Neither: this machine's, refreshed when a
  day old.
- **The policy.** `--env <env>` judges by that env's `vuln_gate` and its `forge vuln
  exempt` exemptions; `--policy <file>` by a `vuln_gate` object in YAML or JSON (a
  field it leaves out keeps its default; `scoped_exemptions` entries add `packages`,
  `artifacts` or `locations`, and cover the advisory only there); neither, by the
  default gate with
  `forge.yaml`'s `ci.vuln_scan.exemptions`.
- **The output.** `--out <dir>` writes each SBOM there (`<name>.cdx.json` or
  `.spdx.json`) and the document as `vuln-gate.json`; `--json` prints it. It is the
  `forge.dev/vuln-list/v1` document: `gate.verdict` (pass, warn, block, unscanned,
  stale, off), `gate.reason` (`vuln_gate_blocked`, `vuln_gate_unscanned`),
  `gate.blocking`, `gate.exemptions_applied`, each finding's `rule` (`block_kev`,
  `block_severity`, `warn_severity`, `exemption`), and `sboms` (artifact, path,
  digest, format, source). It is printed on every outcome, unscanned included.
- **Unscanned is never a pass.** An input with no SBOM (syft missing, a tar holding
  no image, a path the build never made, a file that is no SBOM) or no scan (grype
  missing, no database, one built over 5 days ago) is listed under
  `coverage.unscanned` with the reason.

Exit codes: **0** nothing blocks (pass, warn, or a gate that is `off`); **1** a
blocking finding, in any gate mode; **2** could not determine (`unscanned` or
`stale`, an input or policy that cannot be read). These are a merge gate's 0 pass, 1
fail, 2 unproven as they are.

## Next

- The verbs, the gate's modes and exemptions: `vuln`.
- The database it reads, and installing syft and grype ahead of time: `vuln/database`.
