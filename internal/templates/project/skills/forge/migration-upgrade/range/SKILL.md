---
name: migration-upgrade/range
description: What a forge pin bump brings, as `forge project upgrade` reports it — crossed[] (each release tag crossed and the untagged commits after the last one, with changelog entries by heading), the agent_instructions forge's PRs address to the agent running the upgrade, generated_changes[] (regenerated files by generator, with notes), and the Agent-Instructions / Generated-Change commit trailers a forge PR declares them with.
---

# What the range brings

`forge project upgrade` (and `--check`) prints, before anything else in its
summary, what changed in FORGE between the old pin and the new one, and
`--json` carries it as `crossed`, `generated_changes` and `generated_hint`.
Read it BEFORE you judge the diff: it says what forge's authors changed and
what they want you to do about it.

```json
"crossed": [
  {"version": "v1.0.0", "date": "2026-11-05", "source": "changelog",
   "url": "https://github.com/reliant-labs/forge/releases/tag/v1.0.0",
   "breaking": [], "changed": [], "fixed": [],
   "added": [{"scope": "observe", "text": "…", "pr": 2349,
              "agent": ["…"], "generated": ["middleware_gen.go: …"]}],
   "agent_instructions": [{"text": "…", "pr": 2349, "version": "v1.0.0"}]},
  {"version": "v1.0.1-0.20261106000000-abcdef012345", "untagged": true,
   "after": "v1.0.0", "commit": "abcdef012345", "commits": 12, "source": "git",
   "url": "https://github.com/reliant-labs/forge/compare/v1.0.0...abcdef012345",
   "breaking": [], "added": [], "changed": [], "fixed": [], "agent_instructions": []}
],
"generated_changes": [
  {"generator": "middleware_gen.go", "files": ["internal/billing/middleware_gen.go"],
   "notes": [{"text": "…", "pr": 2349, "version": "v1.0.0"}]}
],
"generated_hint": "tests that pin generated output … may need updating …"
```

## crossed

- One entry per release tag in (`from`, `to`], oldest first; then, when `to`
  is a pseudo-version, one `untagged: true` entry for the commits after the
  last tag (`after`) up to `commit`, with `commits` when git could count them.
- Entries by heading: `breaking`, `added`, `changed`, `fixed`, and `other`
  for any other heading. Each has `scope`, `text`, `pr`, and the PR's `agent`
  and `generated` trailers.
- `source`: `changelog` (the target's CHANGELOG.md from the Go module cache,
  else the one built into this forge), `git` (the clone of forge the go
  command keeps in `$GOMODCACHE/cache/vcs`), or `unavailable` — nothing
  offline describes that part; read `url` (release notes, or a compare view).
- `in_pin: true`: the old pin already contains this release (a pseudo-version
  pin can name the very commit a tag was later cut on). It brings nothing.
- `note` says when part of a release was already in the pin and left out, or
  when that cannot be told offline.

## agent_instructions

Steps the PR that made a change addresses to YOU, the agent upgrading across
it. The text report prints them first, under "Do this, from the changes you
crossed". Do each one after the upgrade applies, before you call it done,
and say in your PR that you did. They never change the exit code.

## generated_changes

The forge-generated files (those carrying a `forge:hash` marker) the
upgrade's `forge generate` changed, added or removed, grouped by generator: a
file name shared by several changed files (`middleware_gen.go`, one per
contract package under `internal/`), or a per-entity pattern (`*_orm_gen.go`). `notes` are the
range's `Generated-Change` notes naming it. Under `--check` generate does not
run, so each entry is a note with `planned: true`.

`generated_hint` is set whenever there are any: a test that pins generated
output (a golden file, a hash or snapshot of a generated file) may now fail
for a change that is forge's. Compare its expectation with the regenerated
file and update the expectation; do not revert the generated file.

## Declaring them: commit trailers (forge authors)

A forge PR whose change a consumer's agent must act on — a breaking change, a
behaviour change in generated output, a new default — says so in a trailer of
one of its commits (the squash body keeps every commit message):

```text
feat(observe): record failing calls' argument shapes

Agent-Instructions: mark an rpc argument that must never be recorded with
  `forge:no-capture`, then run `forge generate`
Generated-Change: middleware_gen.go: each op records redacted argument shapes of a failing or slow call
```

- `Agent-Instructions:` is an imperative step for the agent upgrading across
  the change ("do X", not "we did Y"). Continue a long one on lines that start
  with whitespace. One trailer per step.
- `Generated-Change: <file name or glob>: <summary>` names the generated files
  whose output changes and what they now do; upgrade attaches it to the files
  its generate changed.

At release, `scripts/changelog-release.sh` renders each as a nested bullet
under the PR's CHANGELOG entry (`  - Agent: …`, `  - Generated: …`), and
`forge project upgrade` reads them back from CHANGELOG.md — or straight from
the commits, for an unreleased pseudo-version. A migration playbook is still
the place for a change a person must work through; a trailer is for one or two
steps an agent can take.
