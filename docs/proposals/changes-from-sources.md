# Changes from pinned sources: carry a source-built frontend's `changes.yaml` into the release

**Status:** decided (owner, 2026-11-25): forge carries a pinned source's
`changes.yaml` into the release, option (b), with the recommended answers to
decisions 1–4 ([section 7](#7-decisions)). Being implemented as a `feat(changes)`
pull request. The owner may still override any of 1–4.
**Date:** 2026-11-25
**Related:** [changes.md](changes.md) (Forge Changes, decision 1: a project-root
`changes.yaml`), [../changes.md](../changes.md) (the declaration file),
`kcl/schema.k` `GitSource`, `internal/gitsource`, reliant #3639 (the first
signed-in ramp, which found this gap).

## 1. Problem

A change is declared once, in `changes.yaml` at the project root. `forge env shape`
reads it from the project being released (`changesdecl.Load(projectDir)` in
`internal/cli/env_shape.go`) and copies it into `Shape.Changes`. The control plane
then refuses to launch or ramp a variant that the env's current release does not
declare.

That works when the code that evaluates a change and the project that releases it
are the same repository. It breaks when a project ships a frontend **built from
another repository at a pinned commit** (`forge.Frontend { source = forge.GitSource
{...} }`).

The first real case is prod's `reliant-web` (app.reliantlabs.io):

- The code is reliant's `frontends/web`.
- Its accessors are generated from reliant's `changes.yaml`.
- The release that ships it is control-plane's prod release. control-plane pins
  reliant's commit in `deploy/kcl/lib/reliant_web.k` and has no `changes.yaml`.

So prod's shape declares no changes, and `pending-provider-copy` (reliant #3639)
cannot be launched. That is not because it is wrong; the declaration is simply in
the wrong repository.

The declaration belongs with the code that evaluates it. The release, though, is
cut elsewhere, and today forge has no way to bring the declaration along.

## 2. Proposal

When forge builds a release's shape, it reads `changes.yaml` from each pinned source
a frontend of that env builds from. It merges those declarations with the root's
into `Shape.Changes`.

1. **Which sources count.** Every `forge.Frontend` of the env whose `source` is a
   `forge.GitSource`. A frontend with a `path` uses the root's declaration, as
   today. A `path` that leaves the project is unpinned and stays out of scope (see
   decision 3).
2. **Which file.** `changes.yaml` at the source **repository root**, not at
   `subdir`. That is the file the source repository's own `forge generate` reads
   to write the accessors its frontend calls. A source with no `changes.yaml`
   contributes nothing.
3. **Which commit.** The commit the release records for that frontend
   (`release.Source.Commit`): the same bytes that were built. `forge env build`
   resolves the pin once and uses that checkout for both the build and the
   declaration. `forge env shape <env>` (no build) resolves the ref through the
   same `gitsource` cache. A `.forge/source-overrides.yaml` entry applies too, so
   local iteration reads the working copy, exactly as the build does. A shape can
   therefore never declare changes from a different commit than the one it ships.
4. **Validation.** Each file is checked on its own by `changesdecl.Parse`, with
   messages that name where it came from:
   `github.com/reliant-labs/reliant@72e241de:changes.yaml:5: …`. Then the merged
   set is checked: key uniqueness (section 3) and the existing `MaxShapeChanges`
   (200) over the **merged** total. As today, an invalid declaration fails
   `env shape` and `env build`. A release never ships a declaration nobody
   validated.
5. **Codegen is unchanged.** `forge generate` in the releasing project still
   generates accessors only from its own root `changes.yaml`. A sourced frontend's
   accessors are generated, and committed, in its own repository. The releasing
   project gains no `changes_gen.*` for code it does not own.
6. **Visibility.** `forge env shape <env>` lists each declared change with its
   origin (`root` or `repo@commit`), so a reviewer of a pin bump can see that the
   bump brings or drops a declaration.

## 3. Two declarations of one key

Change keys are one namespace per env: the snapshot, the ledger and the readout are
all keyed by them. A key can be declared twice in three ways: by the root and a
source, by two sources, or by two pins of the same repository.

| Case                                                                                   | Rule                                                                                                                                                                                                                                                                                            |
| -------------------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Identical declarations (same type, default, variants and values after JSON compaction) | **Allowed, kept once.** This is the common case: two frontends from one repository at one commit (reliant-web and forge-demo share one pin literal), or a mirror kept in step. Description differences are ignored, and the first in a stable order wins (root, then sources by frontend name). |
| Different declarations                                                                 | **Refused**, at `env shape` and `env build`, naming both origins and what differs. There is no precedence rule: a winner would silently serve one repository's variants to the other's code.                                                                                                    |

Refusing rather than overriding is deliberate. Either side may be the stale one, and
only a person can say which. The fix is to rename one key, or to make the two
declarations equal.

## 4. Shape, `BundleFeatureChanges` and compatibility

**The shape does not change form.** `Shape.Changes` stays
`map[string]ShapeChange`, keyed by change key, with the same fields. A control plane
that decodes `shape-changes` today decodes a merged map with no change, and its
validation (refuse a variant the release does not declare) applies as it is. So
control-plane needs nothing for the core of this.

**The strip logic is unchanged.** `BundleReader.LimitShape` still drops
`s.Changes` whole for a reader without `BundleFeatureChanges`. Merged declarations
are stripped together with root ones, and an old reader sees what it sees today: no
changes, every key evaluating to its code default.

**Provenance is not in the shape in v1.** An `origin` on each `ShapeChange` would
need a new bundle feature (for example `shape-change-origins`, stripped per entry
for readers without it), because a control plane that predates a field refuses the
whole shape. The release already records each frontend's `Source.Commit`, and
`env shape` prints origins locally, so the console does not need it yet (decision 4).

**Semver.** The change is additive at major 1:

- no flag, field, exit code or `--json` shape is removed or changes meaning;
- a project with no sourced frontend, or whose sources have no `changes.yaml`,
  produces a byte-identical shape and shape hash;
- the one visible difference is that a project pinning a source _with_ a
  `changes.yaml` gains declarations in its next release, after it moves its
  `forge_version` past the release that ships this. That is the point of the
  change, and the declarations only make launches possible; none is launched by
  them.

A new failure mode exists: a key conflict (section 3) fails a build that passed
before. It arises only when both sides declare the key differently, which today
would mean the root's declaration is silently wrong for the sourced code. Following
the "warn before gate" rule for lint, the conflict **warns** for one minor (the
build proceeds and the root's declaration is kept), then **gates** from the next.
That is decision 2.

If an opt-out is wanted, it is an additive optional KCL field on `forge.Frontend`
(`changes_from_source: bool = True`), never a change to `GitSource`'s meaning
(decision 1).

## 5. Alternatives considered

| Alternative                                                                                                                       | Why not                                                                                                                                                                                                                                                                                                                                                                  |
| --------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| (a) **Mirror**: copy the entry into the releasing project's root `changes.yaml` (for reliant-web, a control-plane `changes.yaml`) | Works today, with no forge change. But it creates two copies that drift: a pin bump that changes a variant would ship a shape that disagrees with the code. The releasing project would also generate accessors (`internal/changes/changes_gen.go`) it never calls. Reasonable as a stopgap, with section 3's identical-declaration rule making it safe once this lands. |
| Declare changes in KCL on the `forge.Frontend`                                                                                    | The same duplication as (a), in a different file, and it moves the declaration away from the code that reads it.                                                                                                                                                                                                                                                         |
| Release the sourced frontend from its own repository (a reliant-owned env for app.reliantlabs.io)                                 | It fixes the provenance at the cost of a deploy-topology change. Prod releases reliant-web together with the APIs it calls on purpose (`reliant_web.k`, "NO release_dependencies"). That is an owner topology decision, out of scope here.                                                                                                                               |
| Read only `subdir/changes.yaml`                                                                                                   | It does not match where the source repository's `forge generate` reads it (its root), so the accessors and the release could disagree.                                                                                                                                                                                                                                   |
| Root always wins over a source                                                                                                    | It silently serves variants the sourced code may not know (section 3).                                                                                                                                                                                                                                                                                                   |

## 6. Open owner decisions

| #   | Decision                             | Options                                                                                                                      | Recommendation                                                                                                                   | Blocks                                                      |
| --- | ------------------------------------ | ---------------------------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- | ----------------------------------------------------------- |
| 0   | Mirror (a) or carry from sources (b) | (a) mirror in the releasing project; (b) this proposal; (a) now and (b) later.                                               | (b). (a) is acceptable as a stopgap for the reliant ramp if you want it sooner.                                                  | The prod launch of `pending-provider-copy` (reliant #3639). |
| 1   | On by default, or opt-in             | (a) on for every GitSource frontend, with an optional opt-out field; (b) opt-in per frontend (`changes_from_source = True`). | (a). A sourced frontend whose declarations are missing from its release can never be launched, so the default should be correct. | The KCL schema field and the default.                       |
| 2   | Conflicting declarations of one key  | (a) refuse; (b) warn for one minor, then refuse; (c) root wins.                                                              | (b), following warn-before-gate.                                                                                                 | `env shape` / `env build` errors and the rollout row.       |
| 3   | Sources that are not pinned          | (a) GitSource only; (b) also a `path` outside the project root.                                                              | (a). An out-of-tree path is the unpinned build that GitSource exists to remove.                                                  | Scope of the merge.                                         |
| 4   | Provenance in the shape              | (a) none, printed locally by `env shape`; (b) `origin` per change behind a new bundle feature, shown in the console.         | (a) now; (b) when the console has a use for it.                                                                                  | A new bundle feature, and control-plane decoding.           |

## 7. Decisions

Recorded 2026-11-25. The owner chose "Forge carries it over", option (b) of
decision 0, which includes writing this proposal and then building it. For
decisions 1–4 the recommendations stand until the owner says otherwise.

| #   | Decided                                                                                                                                                                                                                                                                                                                         | By                                 |
| --- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------- |
| 0   | (b): forge reads each pinned source's `changes.yaml` into the release. No mirror in control-plane.                                                                                                                                                                                                                              | Owner                              |
| 1   | (a): on for every `GitSource` frontend. The opt-out is the optional KCL field `forge.Frontend.changes_from_source`; only `False` renders, so every other frontend renders byte-identically.                                                                                                                                     | Recommendation, owner may override |
| 2   | (b): a conflict is the rule `changes-source-conflict`, declared in forge's lint rollout table (`internal/cli/lint/lint_rollout_table.go`) and graded with `lint.GradeRule`, like `vuln-gate`. It ships in v1.12.0, where it warns and the first declaration ships, and gates from v1.13.0. `lint.rules` dials it like any rule. | Recommendation, owner may override |
| 3   | (a): `GitSource` frontends only.                                                                                                                                                                                                                                                                                                | Recommendation, owner may override |
| 4   | (a): no origin in the shape. `forge env shape` prints the origin of each change a source contributed.                                                                                                                                                                                                                           | Recommendation, owner may override |

Prod's launch of `pending-provider-copy` (reliant #3639) waits for the
implementation's release, then control-plane's forge pin bump and a control-plane
release.
