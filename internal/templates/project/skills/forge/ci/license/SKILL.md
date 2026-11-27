---
name: ci/license
description: The gate's license-check row (`forge ci gate`, `task ci:checks`) — go-licenses over the project's Go dependencies, its own modules ignored, run with GOROOT set; the forbidden/restricted blocklist by default and its warn-first rollout (`lint.rules: {license-check: error}`); and `ci.license` in forge.yaml, an allowlist of SPDX license names with per-module exceptions that each carry a reason and an optional expiry. Use when license-check is red or says "would fail", before accepting a dependency's license, or when moving a hand-written license script (an allowlist file, go-licenses in a workflow) onto forge.
---

# License check

`license-check` judges the licenses of your Go dependencies, not your own code:
it passes go-licenses `--ignore` for the module in `go.mod` and for the modules
a root `go.work` uses from inside the project, so a project needs no LICENSE
file of its own for the check to run clean. go-licenses runs with `GOROOT` set
(`go env GOROOT` in the project, unless the environment sets one): the pinned
binary is built with `-trimpath` and has no GOROOT of its own, and without one
it takes every package for the standard library and passes having checked
nothing, which it did before v1.11.0. Its findings are therefore new to every
project, so they warn first, through the `license-check` lint rollout row: below
its `gates_from` release a failing row is an allowed `SKIPPED` "would fail"
naming the dependency, and from it the row FAILs. `lint.rules:
{license-check: error}` gates now; `{license-check: warn}` keeps it a warning.

By default the check is a blocklist (`--disallowed_types=forbidden,restricted`):
a license go-licenses does not recognise passes. `ci.license` makes it an
allowlist, with exceptions per module. Declaring a `ci.license` policy asks for
this verdict, so a project that declares one gates at once, without the
warn-first window:

```yaml
ci:
  license:
    allowed: [Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, ISC]  # SPDX ids
    exceptions:                       # one module each, never a license
      - module: github.com/hashicorp/go-version
        reason: "MPL-2.0 is file-level copyleft; we import it unmodified"  # required
        expires: "2027-06-01"         # optional review date
```

`allowed` runs go-licenses with `--allowed_licenses` instead of the blocklist,
so any other license fails the row, an unrecognised one included. Each
exception adds one `--ignore <module>`; the modules it imports are still
checked. An acceptable license belongs in `allowed`, not in an exception per
module that carries it. An entry with no `reason`, a malformed `expires`, or a
module listed twice is a forge.yaml load error at its line. An entry past its
`expires` date FAILS the row, naming it, until it is re-reviewed (extend the
date with a reason that still holds) or removed, the same rule as
`ci.vuln_scan.exemptions`. go-licenses matches `--ignore` as a prefix, so an
exception for `example.com/a` also covers `example.com/a-b` and
`example.com/a/v2`, which are other modules. An entry that reaches a module the
project requires that way FAILS the row, naming each such module: give each its
own exception (with its own reason), or remove the entry. Packages below the
module path (`example.com/a/sub`) are the entry's own. The check covers Go dependencies only.

npm dependencies have no license check yet.
