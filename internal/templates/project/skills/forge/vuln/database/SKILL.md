---
name: vuln/database
description: The machinery under `forge vuln` — the one grype vulnerability database per machine (location, size, refresh, the 5-day freshness rule and why stale outranks block, `forge vuln db status|pull|import`, the CI cache key, `FORGE_VULN_DB_URL` mirrors, offline import), and the pinned syft and grype release binaries (sha256 compiled into forge, `forge tools install`, `FORGE_TOOLS_MIRROR`, the tool cache's Unix permission and Windows ACL trust check, `forge tools pins --archives` and the public `pkg/toolpins` package). Use when a verdict says stale or not scanned, when caching the database in CI, on an offline or mirrored machine, or when a tool cache is refused as untrusted.
---

# The vulnerability database, syft and grype

## The database

One grype database per machine, shared by every project, never one per project.

| | |
|---|---|
| Where | `$FORGE_VULN_DB_DIR`, default `<UserCacheDir>/forge/vulndb` |
| Size | ~205 MB download, **3.3 GiB** unpacked, ~2 minutes of CPU to import |
| Refresh | `forge vuln scan` refreshes it when it is more than a day old; `forge vuln db pull` refreshes now; `list`, `show` and the cut never download it |
| Fresh enough | built within **5 days** (grype's own limit; grype refuses an older one). Below, the freshness rule says what that means for a verdict |
| Mirror | `FORGE_VULN_DB_URL`: a mirror of `https://grype.anchore.io/databases` with the same layout (`<url>/v6/latest.json` and the archives it names); grype verifies each archive against the listing's sha256 |
| Offline | `forge vuln db import <vulnerability-db_v6.*.tar.zst>` loads an archive downloaded elsewhere |
| Concurrency | scans share it; an update or import takes a lock alone |

```bash
forge vuln db status            # where it is, its age, the CI cache key; exit 2 if absent or stale
forge vuln db status --json     # forge.dev/vuln-db/v1: database, fresh, stale, age_seconds, cache_key
forge vuln db pull              # newest build now (installs grype first when missing)
```

**The freshness rule.** A verdict is determined only if, for every scanned artifact,
its **newest stored scan** ran against a database built within 5 days (by that scan's
own `db_built_at`). A stored scan that is current counts even on a machine with no
database or grype: it is a scan with a fresh database. When one is not, and no
rescan replaced it, the verdict is `stale`: **exit 2**, and at a self-hosted promote
under `enforce` it refuses `vuln_gate_unscanned`. **Stale outranks block**: findings
from an old database cannot establish the release's status either way, so the
verdict is "could not determine" whatever they found; the blocking ids are still
listed (`gate.blocking`, and "the old data would block: …"), so you see what the old
data found. A blocking finding from a current scan outranks an unscanned artifact.
`forge vuln db pull` then `forge vuln scan --release <v>` replaces a stale scan.

**Cache it in CI.** Set `FORGE_VULN_DB_DIR` explicitly and cache that directory
under a key computed before the restore: today's UTC date
(`forge-vulndb-v6-<YYYY-MM-DD>`), with `restore-keys: forge-vulndb-v6-` as the
fallback. A later run the same day hits the exact key and downloads nothing; the
first run of a day restores the newest earlier cache, and `forge vuln db pull`
downloads only when the listing has a newer build; a cold cache downloads once.
Save under today's key after the pull, and skip the save on an exact hit. The
scaffolded `build-images.yml` scan job (for a project pinned to forge v1.10.0 or
later, the first release with `forge vuln`) does exactly this with
`actions/cache/restore` and `actions/cache/save` over `~/.cache/forge/vulndb`.
(`forge vuln db status --json`'s `cache_key` names the database's build date. It
cannot be the restore key, because it is known only once the database is on disk.)

forge runs grype with every `GRYPE_*` variable removed and an empty config, so a
user's or project's `.grype.yaml` (an `ignore:` rule, `only-fixed`) can never change
a verdict. Exemptions are the env's, in KCL.

## syft and grype

forge installs them itself, on first use, as **pinned upstream release binaries
whose sha256 is compiled into forge**: a download that does not hash to the pin is
refused before a byte is extracted, and the cached executable is hashed against
its own compiled-in sha256 on every use. They need no Go toolchain.

```bash
forge tools install syft grype      # ahead of time: a CI image, a machine about to go offline
```

They land in forge's tool cache (`$FORGE_TOOLS_CACHE`, entry
`release/<name>@<version>-<os>-<arch>`). `FORGE_TOOLS_MIRROR` replaces
`https://github.com` with a mirror serving the same paths (the checksum is still
checked). A platform with no published binary (grype has no windows/arm64 build)
falls back to `go install` when Go is on PATH, and otherwise the tool is
unavailable: the cut records "not scanned", never a clean result.

**The cache must be private.** forge refuses a tool cache another user could write,
since they could swap the binary between the check and the exec. On Unix every
directory from `/` down must be owned by you or root and writable by nobody else
(a sticky directory such as `/tmp`, or a directory of your own private group, is
allowed), and forge's entries must be yours and not symlinks. On Windows forge
reads each component's owner and ACL instead and fails closed: the owner must be
you, SYSTEM, Administrators or TrustedInstaller, and no ACE may grant anyone else
write, delete, delete-child, `WRITE_DAC` or `WRITE_OWNER` (a parent may let others
create new folders, as `C:\` does). The default `%LocalAppData%\forge\tools` passes
as it comes. A refused cache names the directory: tighten it, or point
`FORGE_TOOLS_CACHE` somewhere private.

**The same binary elsewhere.** `forge tools pins` lists the versions; `forge tools
pins --archives` (or `forge tools pins grype --archives`) prints, as JSON, each
platform's archive name, URL and sha256 and the sha256 of the executable inside it.
Go code reads the same data from the public package
`github.com/reliant-labs/forge/pkg/toolpins` (`toolpins.Grype().ReleaseURL("linux",
"amd64")`, `.SHA256(…)`, `.BinarySHA256(…)`), so a worker image or a CI step runs the
grype forge pins, with no second place to bump. Pinning a forge release pins these
versions with it.

## Next

- What is scanned, the verbs and the gate: `vuln`.
