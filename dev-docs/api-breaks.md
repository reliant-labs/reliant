# Breaking `forge/pkg`: declare it, ship the codemod

forge is pre-1.0 and owes nobody compatibility. A change to `pkg/` may remove
or reshape an exported API. What it may not do is leave the cost to the next
person who pins forge: **the breaking PR ships its migration.** Two checks hold
that line.

|                     | what it checks                                                         | where                                                                           | when                                                             |
| ------------------- | ---------------------------------------------------------------------- | ------------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| **API gate**        | an incompatible change to `pkg/**` is declared and ships a codemod     | `ci.yml` `checks` job, step "pkg/ API compatibility"; `task apigate`            | every pull request, ~seconds when `pkg/` is not touched          |
| **Consumer canary** | reliant and control-plane still build, vet and pass the affected tests | `consumer-canary.yml`; `task canary` ([consumer-canary.md](consumer-canary.md)) | pushes to main, nightly, and a pull request labelled `ci:canary` |

## The API gate

`internal/apigate` type-checks the exported API of every package under `pkg/`
at the merge base and at the PR's head, and compares them with
`golang.org/x/exp/apidiff`. It fails on an **incompatible** change: a removed or
renamed export, a changed signature or field type, a removed method, a
removed package, a new method on an exported interface. Additions are
compatible and need nothing.

An incompatible change passes only if both of these hold.

**1. The PR declares it.** Either:

- the `api-break` label is on the pull request, or
- a line in the PR body (or a commit body) starts with `API-BREAK:`:

  ```
  API-BREAK: codemod=tx-isolation
  API-BREAK: playbook=migrations/v1.0.0
  API-BREAK: codemod=tx-isolation playbook=migrations/v1.0.0
  ```

The marker must start the line (prose that mentions it mid-sentence declares
nothing). The label and the body are read live from GitHub, so adding either and
re-running the failed job is enough.

**2. The same diff ships the remedy.** One of:

- **a codemod**: a non-test Go file in `internal/codemod/` that registers a
  `forge fix <name>` (`register(Codemod{Name: "<name>", ...})`). Mark it
  `Upgrade: true` when it is a pure rename or move, so `forge project upgrade`
  runs it for you. A codemod that is not a pure rename (it replaces files, or
  lists what it cannot decide) is named in that release's playbook frontmatter,
  `automation: fix:<name>`, and `forge project upgrade` runs it when the
  playbook's detection matches, over a clean tree. One of the two is required:
  `TestEveryRegisteredCodemodIsReachableFromUpgrade` fails otherwise, so a
  registered codemod is never one a consumer's `forge project upgrade` skips.
  See `internal/codemod/codemod.go` for what a codemod is and the existing ones
  (`txisolation.go`, `connecterrors.go`) for the shape.
- **a migration playbook entry**: a file under
  `internal/templates/project/skills/forge/migrations/<vX.Y.Z>/`, for a break no
  tool can rewrite (a decision a person has to make). Add a section to that
  release's `SKILL.md` saying _use when_ / _what changed_ / _what to do_.

With a bare `API-BREAK:` or the label, any codemod source or playbook file in the
diff satisfies the gate. Naming one (`codemod=<name>`, `playbook=<path>`) is
stricter: the name must be registered in a file this PR changes, or the
playbook's `SKILL.md` must exist and be touched by this PR. Prefer naming it —
it is what a reviewer reads.

Neither may be a promise: "the codemod comes in a follow-up" is the failure this
gate exists to remove. Land them together.

### Running it

```bash
task apigate                          # against the merge base with origin/main
task apigate -- -label api-break      # pretend the label is on the PR
task apigate -- -body-file body.md    # read the marker from a file
```

It exits in about a second when the diff touches no Go file `pkg/` can see
(docs, the CLI, linters, tests). A removed package under `pkg/` counts as a
break.

### What it does not see

`apidiff` compares Go types. It does not see behaviour changes behind an
unchanged signature (a new default, a stricter check), generated-output
changes (templates), CLI flags, KCL schemas or proto field changes. The
consumer canary catches the ones that break a build; the rest are what
migration playbooks and `lint.rules` warn-first rollout (`reliant.md`, "New
lint rules warn before they gate") are for.

### A false positive

If apidiff reports a change you are certain no consumer can observe (an
exported name nothing imports), declare it anyway and say so in the PR: the
cost of a one-line playbook entry is lower than the cost of an undeclared
break. There is deliberately no ignore list.
