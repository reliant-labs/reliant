# Proposal: per-project context for dashboard panels

**Status:** proposal, not implemented. Written for owner review. Nothing here
has landed in forge, control-plane or reliant, and the architecture choice
needs the owner's approval before anyone implements it.
**Date:** 2026-11-18
**Issue:** forge#2233
**Scope:** forge (`pkg/dashspec`, `pkg/obsquery`, `internal/obsbackend`, the
dashboards skill), with reader work in control-plane (`internal/obsread`, the
observability handlers) and a fixture update in reliant
(`internal/dashspeccontract`).

## Summary

A sql panel receives the environments the viewer selected (`{env_ids}`,
`{envs}`), but not which project each one belongs to. On an org-scoped
dashboard, where the selection can mix `project/env` entries from several
projects, a panel that should follow "the selected environments of project P"
cannot say so. control-plane#2614 worked around this by writing the landing
project's two environment ids into 16 panel statements, plus a text guard test
that keeps the 16 copies in step. A new landing environment is invisible to all
16 until someone edits them.

The recommendation is **declared project environment sets**. A dashboard
declares a named set once:

```json
"projectEnvs": [{ "id": "landing_envs", "project": "landing" }]
```

The reader binds the set as the ClickHouse parameter
`{landing_envs:Array(String)}`. Its value depends on the selection:

- the selected environments that belong to project `landing`, in selection
  order;
- `[]` when nothing is selected at all;
- `['(none)']`, which matches no row, when the viewer selected environments
  but none of them is `landing`'s.

These are the conventions `{env_ids}` already follows. So a panel of project P
shows nothing for P while the viewer has selected only other projects'
environments. Today's boards show all of P in that case, and section 4 weighs
the change. It is a typed value bound by the server and never spliced into the SQL. It
follows the precedent toggle filters set (#1746): declared on the dashboard,
bound by the reader, named by the author, and unable to shadow a built-in. It
claims no new global parameter name, so no existing dashboard can break.

Section 3 compares this with exposing each environment's project as a built-in
parameter, and with narrowing a whole panel to a project. Section 4 argues
against the recommendation.

---

## 1. Where things stand

### How a panel gets its selection today

- **The spec** (`pkg/dashspec`). `Dashboard.Scope` (`spec.go`, `type Scope`)
  is `project` (the default: the viewer's selection within the route's
  project), `org` (every project of the organization, still narrowed to what
  the viewer selected) or `platform` (no environment predicate). Panels carry
  no scope of their own. Validation is closed-world: `v.known(...)` refuses an
  unknown key on a dashboard, filter or panel (`validate.go`). `version`
  stays `forge.dashboards/v1`.
- **The parameters** (`pkg/dashspec/sql.go`). A sql panel's statement may use
  `{from:DateTime64(9)}`, `{to:DateTime64(9)}`, `{step:UInt32}`,
  `{env_ids:Array(String)}`, `{envs:Array(String)}` (names, in `env_ids`
  order) and `{services:Array(String)}`. When exactly one environment is in
  scope it may also use `{reliant_env_id:String}`
  (`obsquery.EnvIDParam`). A toggle filter adds `{<id>:Bool}`.
  `builtinSQLParams` (`validate.go`) stops a toggle shadowing any of them.
- **The binding** (`pkg/obsquery/sql_panel.go`). `SQLPanel.Compile` leaves the
  statement untouched and builds `Stmt.Params` from a _verified_
  `obsquery.Scope`. `env_ids` is `[]` when nothing narrows the environments
  and `['(none)']` when the scope is restricted to none.
  `obsquery.Env` is `{ID, Name}`, with no project.
- **The control plane.** `QueryPanel` (`internal/handlers/observability/rpc_query_panel.go`)
  opens a tenant through `obsread`. `envscope.Resolve`
  (`internal/envscope/envscope.go`) checks every requested id against the
  org's live environments (PermissionDenied `env_unavailable` otherwise) and
  returns them as `envscope.Environment{ID, Name, Project}`.
  `obsread/service.go` copies only `{ID, Name}` into `Scope.Envs`, so the
  project is known at that point and then dropped.
- **The local backend** (`internal/obsbackend/local.go`, `sqlScope`). It has
  one environment, named by `--env`, with the id `""`. The project name exists
  only in the dashboard generator (`internal/obsdash/spec.go`, `Spec.Project`)
  and never reaches a parameter.
- **The console** (`reliant/frontends/web/.../Dashboards/orgScope.ts`). On an
  org-scoped board it lists every environment of the org as `project/env` and
  sends only `envIds`: "never a project". One selection can therefore name
  environments of several projects.
- **Postgres panels** (`pkg/dashspec/postgres.go`, control-plane
  `internal/dbpanel`) bind only `$1..$3` (from, to, step). They have no
  environment context at all, and are out of scope here (open question 6).

### How projects are identified

In the control plane an environment's project is the forge.yaml **name**:
`controlplane.deploy_environments.project TEXT`, unique as
`(org_id, project, name)`. `controlplane.hosted_projects` has `UNIQUE(org_id,
name)` and an immutable identity. So a project name is unique and stable within
an org, and it is the key environments actually carry. Two orgs may both have a
project called `landing`. That is harmless, because a name is only ever
resolved inside the org the reader has already verified.

### The hard-coded examples (control-plane#2614)

`deploy/observability/dashboards/product-growth.json` and `product-funnel.json`
are both `"scope": "org"`. They span the landing site, the app funnel, forge
and the demo. The landing project's prod and staging environments,
`3a5e234f-a027-497d-9a0b-ab1c6cf8c281` and
`00969003-cc1b-472e-9ae8-4493e3bb6e37`, are written into 16 statements: 9 in
growth and 7 in funnel.

Eleven use one plain shape, for example `landing-headline`:

```sql
WHERE Client = 'landing' AND EventName IN ('page_view')
  AND Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}
  AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId)
       OR NOT hasAny({env_ids:Array(String)},
                     ['3a5e234f-a027-497d-9a0b-ab1c6cf8c281', '00969003-cc1b-472e-9ae8-4493e3bb6e37']))
```

Five are **mixed**. They join landing events to app events in one query, for
example the funnel's `landing-daily`, `landing-session-funnel`,
`stitched-funnel`, `desktop-funnel` and `utm-conversion`. In each, landing rows
and app rows need different environment rules:

```sql
AND (empty({env_ids:Array(String)}) OR has({env_ids:Array(String)}, EnvId)
     OR (Client = 'landing' AND NOT hasAny({env_ids:Array(String)}, ['3a5e234f-…', '00969003-…'])))
```

The intended rule, from the board's description, is that the environment
filter narrows a site only when it names one of that site's environments.
Selecting control-plane prod alone leaves every landing panel reading all
landing traffic. Selecting landing staging narrows the landing rows to staging,
and a staging environment with no traffic in the range reads as no landing
events.

Two tests guard the literals:

- `TestLandingPanelsDeclareTheLandingEnvironments`
  (`internal/dashtemplates/product_growth_test.go`). A text check that every
  landing panel contains the exact `NOT hasAny(..., ['<prod>', '<staging>'])`
  string. The two ids are test constants.
- `TestLandingPanelsFollowTheDeclaredLandingEnvironments`
  (`product_landing_scope_integration_test.go`, integration tag). It pins the
  behaviour against seeded ClickHouse.

### What it costs

- **A new landing environment is silently wrong.** If the viewer selects a
  `landing/preview` environment, `hasAny` against the two-id list is false. The
  panel then reads _all_ landing traffic, not the preview's. Nothing reports
  this. Someone has to remember 16 statements and a test constant.
- **Environment ids are the least stable key available.** An environment is
  deleted and re-created with a new id. A project's name is its identity.
- **Every per-project panel repeats the pattern.** Any future board that mixes
  projects, such as forge's own site or the demo, copies it again.
- **It is invisible to anyone not reading the SQL.** The board's description
  has to explain its own workaround.

### A related finding: toggles have no reader

Toggle filters (#1746) are declared and validated in dashspec, and
`SQLPanel.Compile` binds `Scope.Toggles`. However, I found nothing in forge's
local backend, the control plane or reliant that fills `Scope.Toggles` from
`Dashboard.ToggleValues`. `QueryPanelRequest` has no field for toggles. The
product boards hard-code their internal-user exclusion instead. This matters
here because the recommended design uses the same "declared on the dashboard,
bound by the reader" path. The delivery must include the readers, or it will
repeat the dead letter. Section 5.4 does both.

## 2. Goals and non-goals

**Goals**

1. A panel can say "the selected environments of project P" without naming an
   environment id.
2. Adding, deleting or re-creating an environment of P needs no dashboard
   edit.
3. Mixed panels, where one statement reads rows of P and rows of the
   selection, can express both rules.
4. Every existing dashboard, shipped or saved, keeps working with no edit. No
   parameter changes meaning and no new global name is claimed.
5. Local `forge obs` and the hosted console bind the same values for the same
   selection, from one implementation in forge.
6. The tenancy boundary is unchanged. Authorization stays where it is today,
   in the server's org admission and the org's own ClickHouse database and
   user, and no declaration or selection takes part in it. A project is only
   ever looked up inside the verified org.
7. A set follows the selection's intent. When the viewer has selected
   environments, P's rows appear only if one of P's environments is among
   them.

**Non-goals**

- Structured (series/formula) panels and postgres panels. Section 6 lists them
  as follow-ups.
- Changing how a project-scoped route resolves its environments.
- Splicing text into SQL in any form.

## 3. Options

### Option A: expose each environment's project as built-in parameters

Bind `{env_projects:Array(String)}`, the project of each environment in
`env_ids` order, parallel to `{envs}`. This is the literal reading of the
issue's first ask: put the project in the template context. Something like a
`{project_envs:Map(String, Array(String))}` would also be needed to cover the
fallback.

- **For:** small, and generic. It would also serve a display column
  ("project/env") with no further work.
- **Against:**
  - Every panel re-derives the rule inline. "Selected ∩ P" is
    `arrayFilter((id, p) -> p = 'landing', {env_ids}, {env_projects})`. The
    "narrow only when the selection names P" fallback adds an `empty()` test
    on that result, and each mixed panel repeats it twice. Sixteen copies of a
    lambda replace sixteen copies of a list, and the logic most likely to be
    wrong stays in hand-written SQL.
  - **It claims new global names.** `builtinSQLParams` makes a toggle that
    shadows a built-in a validation error. An existing dashboard with a toggle
    whose id is `env_projects` would stop parsing the day the name became
    built-in. That is a v1 break. It could be avoided only by reserving a
    namespace first, and `reliant_` is the only one with precedent.

### Option B: declared project environment sets (recommended)

The dashboard declares named sets of the form `{id, project}`. The reader binds
each set as `{<id>:Array(String)}` using one rule, implemented once in
`pkg/obsquery`:

| The verified selection               | `{<id>}` is                   |
| ------------------------------------ | ----------------------------- |
| unrestricted (nothing selected)      | `[]`                          |
| names one or more environments of P  | those ids, in selection order |
| names environments, none of them P's | `['(none)']`                  |
| platform scope                       | `[]`                          |

The values carry the same meanings they carry in `{env_ids}`:

- `[]` means nothing narrows the environments.
- `['(none)']` is `obsquery`'s existing restricted-to-none sentinel. It cannot
  equal an environment id, because ids are UUIDs, so it matches no row.

A statement uses the set the way it already uses `env_ids`:

```sql
-- a landing-only panel
AND (empty({landing_envs:Array(String)}) OR has({landing_envs:Array(String)}, EnvId))

-- a mixed panel: landing rows follow the landing set, the rest the selection
AND (   (Client =  'landing' AND (empty({landing_envs:Array(String)}) OR has({landing_envs:Array(String)}, EnvId)))
     OR (Client != 'landing' AND (empty({env_ids:Array(String)})      OR has({env_ids:Array(String)}, EnvId))))
```

- **For:**
  - The rule lives in one Go function with a table test, not in SQL.
  - The project is named once per board, by its stable name.
  - Mixed panels work.
  - The parameter name is the author's, so no global name is claimed and no
    existing dashboard can collide.
  - It uses the toggle mechanism the spec already has.
  - Resolution needs only the projects of the _selected_ environments, which
    `envscope.Resolve` already returns, so the read costs no extra query.
  - The SQL contract is the one authors already know from `{env_ids}`. The
    `empty(...) OR has(...)` shape is right in all three states, and a panel
    never has to test for a sentinel.
- **Against:**
  - It is a new spec field, so readers older than the release refuse a
    dashboard that uses it, because validation is closed-world (section 5).
  - It is SQL-only.
  - A statement can still forget the `Client = 'landing'` half of a mixed
    predicate. The set narrows environments; it does not know which rows are
    P's.

### Option C: narrow a whole panel to a project

A panel declares `"project": "landing"`. The reader replaces the panel's scope
with the selection ∩ P (or all of P), so `{env_ids}`, `{envs}`,
`{reliant_env_id}` and the structured predicates all follow P with no SQL
change. This is the "derive the project server-side" and "project-scoped
panel" shape.

- **For:**
  - It also covers structured panels.
  - The 11 landing-only panels would just drop their literals.
- **Against:**
  - **It cannot express the five mixed panels.** They need both rules in one
    statement.
  - It changes what `{env_ids}` means panel by panel, so a reader of the SQL
    can no longer tell what `env_ids` holds without looking at the panel's
    metadata.
  - "All of P" as a fallback needs a lookup of P's live environments, which
    hides the history of a deleted environment.

### Not an option: split the board into project-scoped boards

`scope: project` already narrows to one project. But the growth and funnel
boards exist to join projects: landing visit → app sign-up. Splitting them
loses the funnel, which is the point of the board.

## 4. Recommendation: Option B, and the case against it

Adopt Option B with the containment rule in the table above: `['(none)']`
when the selection names environments but none of P's, and `[]` only when
nothing is selected at all. Ship it together with its two readers (local and
control-plane), and with the toggle binding that is missing today
(section 1).

### Selection is a filter, not authorization

An environment selection, a declared set and every other bound parameter are
dashboard **filters**. They say what a panel should show. They do not decide
what a caller may read.

- **Authorization is enforced by the server.** The org is admitted through
  membership or an `obs:read` credential of that org (`obsread`). The
  statement then runs in the org's own ClickHouse database as the org's own
  user. Both happen whatever the request or the dashboard declares. The
  control plane says so in `internal/obsread/sql.go`: the statement's
  parameters "are not enforcement".
- **The one credential that is confined to an environment never runs a sql
  panel.** A token bound to one environment is refused at admission
  (`Request.Raw`) and again in `SQLStatement`. So no caller that may run a sql
  panel is limited to less than its org, and sets can only reach callers that
  may read the whole org anyway.
- **So a set, including one an inline request forges, decides presentation
  only.** It cannot reach another org's data, because the project is resolved
  inside the verified org and the query runs as that org's user. What it can
  do is show more or less of the org than the viewer selected. That is why
  its rule matters for correctness, even though it does not matter for
  authorization.

### Containment or today's behaviour

Under today's rule, `[]`, a set whose project the selection does not name is
"not narrowed". A viewer who selected only control-plane prod would see every
landing environment's traffic, including environments they never chose.
That is what the boards do now, and their integration test pins it. It is
also the convention `{env_ids}` uses when nothing is selected. But it means a
panel can show rows **wider than the selection**, and the viewer cannot tell
from the filter bar.

The containment rule, `['(none)']`, removes that. A selection is honoured as
written: P's panels show P's rows only when P's environments are selected, or
when nothing is selected.

|                                     | `[]` (today's behaviour)                                | `['(none)']` (containment, recommended)                              |
| ----------------------------------- | ------------------------------------------------------- | -------------------------------------------------------------------- |
| Selection = only control-plane prod | landing panels show all landing traffic                 | landing panels show nothing; mixed funnels have no landing step      |
| Selection = nothing                 | all landing traffic                                     | all landing traffic (unchanged)                                      |
| Selection names a landing env       | that env                                                | that env (unchanged)                                                 |
| What the filter bar promises        | "these environments, plus any project you did not name" | "exactly these environments"                                         |
| Cost                                | none                                                    | a product change on two shipped boards, to be accepted and described |

I recommend containment. A filter that silently shows more than it says is
the defect #2233 is about, in another form: there, a new landing environment
showed all traffic. A viewer who wants landing next to the app either selects
landing's environments too, or selects nothing.

Two alternatives were considered and not recommended:

- **A separate flag `{<id>_selected:UInt8}`.** It would let each panel choose
  between the two behaviours, but that puts the rule back into hand-written
  SQL, in 16 copies. The sentinel already makes the two cases distinguishable
  without one.
- **Binding every live environment of P.** It is self-contained, but it costs
  a lookup on every read, and it hides deleted environments' history and rows
  with no environment id.

If a board ever needs today's behaviour back, the compatible path is an opt-in
on the declaration, for example `"whenUnselected": "all"`. Adding it later
breaks nothing. Shipping it now could never be taken back.

### Devil's advocate

- _"Option A is smaller and is what the issue literally asks for."_ It is
  smaller in forge and larger everywhere else. The issue's own second ask, a
  macro the server expands to "the selected env ids of project P", is Option B
  under a typed parameter instead of a text macro. A text macro would break
  the invariant every sql panel relies on, that nothing is spliced into the
  SQL. If a display column ever needs each environment's project, Option A's
  parameter can be added later under the `reliant_` prefix with nothing
  broken. The two options do not exclude each other.
- _"Containment makes the growth board worse: I selected control-plane prod
  and the landing headline went blank."_ That is the cost, and it is real.
  Before, the board answered a question the viewer did not ask. Now the empty
  panel is honest, and the board's description should say "select landing's
  environments, or none, to see the landing". The rule is one branch of
  `ResolveProjectEnvs`, but it decides what shipped panels show, so it must
  be settled before release (question 2). Changing it afterwards would change
  shipped behaviour again.
- _"Mixed panels still need `Client = 'landing'`, so the set does not know
  which rows are P's."_ Correct. The rows in `product_events` carry an
  `EnvId`, not a project. Teaching the reader which rows belong to a project
  is the structured follow-up: a panel-level `projectEnvs` reference that
  narrows a structured panel's environment predicate, or a row-level project
  attribute stamped at ingest. Neither is needed to remove the literals.
- _"A dashboard-level declaration has to travel with an inline panel."_ It
  does. A shipped board run by `panel_ref` already gives the server the whole
  dashboard. A console that sends a panel inline must send the board's
  declarations too (section 5.4). The server does not need to trust them. A
  forged declaration changes only which of the org's rows a panel _shows_,
  and the caller could already read all of those rows by sending no selection.
  Authorization stays the org admission and the org's ClickHouse user
  ("Selection is a filter, not authorization", above). A panel-level
  declaration would travel for free, at the cost of naming the project in each
  panel instead of once per board.
- _"Why not let a set name several projects?"_ No panel needs that today. A
  list is a compatible widening later (`project` → `projects`), whereas a
  list shipped now could never be narrowed again.

## 5. Design

### 5.1 Spec (`pkg/dashspec`)

```go
// ProjectEnvs is a named set of the selection's environments that belong to
// one project, bound to a sql panel as the ClickHouse parameter
// {<ID>:Array(String)}: the selected environments of Project, in selection
// order; [] when nothing is selected; ["(none)"], which matches nothing, when
// environments are selected but none is Project's. The same conventions as
// SQLParamEnvIDs. A typed value, never spliced into the SQL, and a filter: it
// authorizes nothing.
type ProjectEnvs struct {
	// ID is the parameter's name: ToggleIDPattern, unique among the
	// dashboard's toggles and sets, and not a built-in parameter.
	ID string `json:"id"`
	// Project is the project's name as its forge.yaml declares it.
	Project string `json:"project"`
}

// Dashboard gains, beside Filters:
ProjectEnvs []ProjectEnvs `json:"projectEnvs,omitempty"`
```

The validator (`validate.go`) adds `projectEnvs` to the dashboard's `known`
list and checks:

- `id` matches `ToggleIDPattern`, is not in `builtinSQLParams`, and does not
  equal any toggle's id or another set's;
- `project` is 1–63 bytes, starts with a letter, and uses only ASCII letters,
  digits, `_` and `-` (the `cmdutil.ValidateProjectName` rule);
- there are at most `MaxProjectEnvs` (8) sets;
- **the dashboard's scope is `org`**. On a `project` or `platform` board the
  refusal says why: "a project dashboard's selection is already one project's;
  projectEnvs needs scope org". Refusing now leaves the door open: allowing
  other scopes later is a compatible loosening, while tightening later would
  break a valid dashboard.

`SpecVersion` stays `forge.dashboards/v1`. The field is optional and
`omitempty`, so every existing dashboard serializes byte-for-byte the same.

The values are computed in `pkg/obsquery` (5.2), not in `dashspec`:
`obsquery` imports `dashspec`, so the reverse import is not available, and the
rule belongs next to the scope it reads.

### 5.2 Binding (`pkg/obsquery`)

- `Env` gains `Project string` (`Env{ID, Name, Project}`). This is additive.
  Every literal in forge, control-plane and reliant is keyed, so they all keep
  compiling (the implementation PR re-checks this with `rg 'obsquery.Env{'`).
- `Scope` gains `ProjectEnvs map[string][]string`, which works like
  `Toggles`.
- `ResolveProjectEnvs(selected []Env, restricted bool, project string) []string`
  is the rule table in section 3, Option B, and
  `ProjectEnvValues(decls []dashspec.ProjectEnvs, selected []Env, restricted bool) map[string][]string`
  applies it to every set a dashboard declares, so every declared set is
  bound. Both are pure, and both readers call them.
- `SQLPanel.Compile` binds each entry with `stringArrayText`. Like toggles, it
  refuses a name that shadows a built-in, `EnvIDParam` or a toggle.
- `Fingerprint` appends the sets only when there are any, as it does for
  instances, so an existing page cursor still fits.

### 5.3 Local backend (`internal/obsbackend/local.go`)

`sqlScope` binds each set with the local project's forge.yaml name as the
environment's project. With `--env` set, the single environment `{ID: "",
Name: env, Project: <forge.yaml name>}` is selected. A set naming this project
is then `[""]`, consistent with `env_ids = [""]`, and any other set is
`['(none)']`. With no `--env`, every set is `[]`. `ReservedParamList` and the `forge obs
query` help (`internal/cli/obs_query.go`) say that declared sets, like toggles,
are bound only for `--panel`. A bare `--sql` names no dashboard.

The same PR binds toggles for `--panel` through `ToggleValues`, which closes
the gap in section 1 for the local reader.

### 5.4 Control plane

- `obsread/service.go` copies `Project` into `Scope.Envs`. It builds the sets
  from `envscope.Resolved.Selected()`, which lists exactly the verified
  selection with each environment's project, rather than from `Scope.Envs`,
  which is capped at `MaxScopeEnvs`. No new database read is needed.
- `QueryPanel` with `panel_ref` takes the declarations, and toggle defaults,
  from the shipped board it already holds, and ignores any the request sends.
- `QueryPanel` with an inline `panel` reads them from a new optional field,
  `DashboardBindings bindings = 8` (`repeated ProjectEnvs project_envs`,
  `map<string,bool> toggles`). This is additive and clean under
  `buf breaking`. The console fills it from the dashboard it is drawing.
- When an inline panel's statement names `{<id>:Array(String)}` but the
  request declares no such set (an older console), the handler refuses it with
  "the statement names {landing_envs}, which this dashboard does not declare".
  `usesParam` already makes that check for `reliant_env_id`. Without it, the
  failure would be ClickHouse's opaque unbound-parameter error.

### 5.5 Console (reliant)

- The dashboard editor round-trips `projectEnvs`. The
  `internal/dashspeccontract` fixture gains one set, so the forge pin proves
  the editor's output parses.
- `wireScope` (`services/controlPlane/observability/api.ts`) sends `bindings`
  for inline panels.
- Showing which project each set follows, for example a chip "landing: follows
  selection / all", is optional polish and out of scope.

### 5.6 Documentation

- The dashboards skill
  (`internal/templates/project/skills/forge/observability/dashboards/SKILL.md`)
  gets a "Panels of one project on an org board" section with both SQL shapes.
  It also fixes its second parameter list, which omits `env_ids` and
  `services`.
- The `pkg/dashspec/sql.go` package comment names declared sets next to
  toggles.

## 6. Backwards compatibility (semver major 1)

- **Existing dashboards are unchanged.**
  - No field is required.
  - `version` does not move.
  - `{env_ids}`, `{envs}`, `{services}`, `{reliant_env_id}` and toggles keep
    their meaning and their values.
  - The new parameter names belong to the author, so the built-in set does
    not grow and no existing toggle can start shadowing one. This is the
    deciding difference from Option A.
- **Nothing is removed or deprecated in forge.** The `NOT hasAny(...)` pattern
  is plain SQL, not a forge API, and keeps working wherever it is written. The
  skill stops recommending it.
- **Go API.** One exported field each is added to `obsquery.Env`,
  `obsquery.Scope` and `dashspec.Dashboard`, plus a new type and two
  functions. Nothing changes signature. The PR title is a `feat`, so the
  release is a minor.
- **Older readers.** Validation is closed-world, so a reader built with an
  older forge refuses a dashboard that uses `projectEnvs`. The readers are
  `forge obs`, `forge lint`, the control plane, and reliant's editor. The
  usual rule covers this: readers move first. control-plane uses the field in
  a shipped board only after its forge pin includes the release, and
  `task pin:forge-coherence` keeps reliant's editor on the same forge. A
  project that hand-writes the field needs that forge, and the skill says so.
  Release-carried dashboards are generated by forge, which emits no
  `projectEnvs` today.
- **Protocol.** `QueryPanelRequest.bindings` is optional. An old console that
  sends none still runs every panel that uses no set. A panel that uses a set
  gets a clear refusal instead of a wrong answer.
- **Lint rollout.** None of this adds a `forge lint` rule. If the owner wants
  the env-id-literal lint in question 7, it gets a `lint_rollout_table.go` row
  and warns for one release before it gates.

## 7. Migration path for existing per-project panels

These steps follow the forge release and the control-plane pin bump.

1. **Declare the set** on `product-growth.json` and `product-funnel.json`:
   `"projectEnvs": [{"id": "landing_envs", "project": "landing"}]`. First
   confirm the landing environments' `project` column reads `landing`
   (question 3).
2. **Rewrite the 11 landing-only predicates.** Replace
   `(empty({env_ids}) OR has({env_ids}, EnvId) OR NOT hasAny({env_ids}, [<two ids>]))`
   with `(empty({landing_envs:Array(String)}) OR has({landing_envs:Array(String)}, EnvId))`.
3. **Rewrite the 5 mixed predicates** to the two-armed shape in section 3,
   Option B, and delete the `-- Landing events live in …` comment that lists
   the ids.
4. **Replace the text guard.** `TestLandingPanelsDeclareTheLandingEnvironments`
   becomes "no shipped panel contains an environment-id literal", using a UUID
   regex over every shipped board's SQL, plus "every panel reading
   `Client = 'landing'` names `{landing_envs:Array(String)}`". The test
   constants then exist only in the integration seed.
5. **Keep the integration test's expectations as they are.**
   `TestLandingPanelsFollowTheDeclaredLandingEnvironments` must pass with
   identical results for every selection that is empty or names a landing
   environment. That proves the rewrite preserves those cases. Its harness
   binds `landing_envs` through the real `obsread` path, not a hand-built
   parameter map. Add the case the literals get wrong: a selection naming a
   landing environment that is not one of the two narrows to it.
6. **Change the one expectation containment changes.** With
   `env_ids = ['prod']`, only control-plane prod, the landing panels now read
   as no landing events, like the quiet-staging case. Update the test and the
   boards' descriptions in the same PR. The PR says so in its title and body,
   because it changes what two shipped boards show.
7. **Saved copies need no migration.** The literals live only in the embedded
   boards. A workspace dashboard forked from them keeps its own copy of the
   old SQL, which keeps working exactly as before. Optionally, a one-off
   read-only query lists forks whose SQL contains either id, so their owners
   can be told. No database migration is needed.

There are no forge-side migrations: forge's generated dashboards hard-code no
environment ids.

## 8. Test plan

**forge**

- `pkg/dashspec` validation: accepts a valid set. Refuses a bad id; an id that
  shadows each built-in, `reliant_env_id`, a toggle or another set; a bad or
  empty project; more than 8 sets; and `projectEnvs` on `project` and
  `platform` boards. Every existing fixture still parses, and
  `json.Marshal(ParseDashboard(x))` is unchanged for every shipped generated
  dashboard (no `projectEnvs` key appears).
- `pkg/obsquery` `ResolveProjectEnvs` table test. Cases:
  - unrestricted gives `[]`;
  - the selection names only P;
  - P plus others (order preserved, others dropped);
  - only other projects gives `['(none)']`;
  - platform gives `[]`;
  - an environment whose project is `""`;
  - a duplicate id.
- `TestSQLPanelBindsTheScopeAsParameters` is extended. Sets are bound,
  built-ins are unchanged, and a set that shadows a built-in is refused.
  `Fingerprint` is unchanged when there are no sets.
- `internal/obsbackend` local: `--panel` with `--env` binds the local
  project's set as `[""]` and another project's as `[]`. Toggles are now bound
  too.
- One ClickHouse-backed test, slow-gated with `testkit.Slow` beside the
  existing `releases_clickhouse_test.go`. It runs the landing-only and mixed
  shapes from section 3 over seeded rows and asserts the rule table
  end-to-end.
- Skill: both parameter lists name the same built-ins and the declared
  sets.

**control-plane**

- `obsread`: `Scope.Envs` carries `Project`. Sets are built from `Selected()`
  and stay correct when the org has more environments than `MaxScopeEnvs`.
- `QueryPanel`: a `panel_ref` run uses the board's declarations and ignores
  the request's. An inline run uses the request's. An inline statement naming
  an undeclared set is refused with the named message. A request that
  declares project P while selecting only Q's environments binds
  `['(none)']`, and every statement runs under the org's own ClickHouse user
  and database whatever the declarations say. An environment-bound
  credential is still refused for sql panels.
- The landing integration test described in section 7, step 5, plus the
  literal-free guard.

**reliant**

- `dashspeccontract`: the editor fixture with a set parses at the pinned
  forge.
- The `wireScope` unit test sends `bindings` for an inline panel.

## 9. Open questions for the owner

1. **Approve Option B** (declared project environment sets) over A (built-in
   per-environment project parameters) and C (whole-panel project narrowing)?
2. **What a set holds when the selection names environments but none of
   P's.** The options are:

   - `['(none)']` (containment): P's panels show nothing until P is selected,
     or until nothing is selected;
   - `[]` (today's boards): P's panels show all of P, wider than the
     selection;
   - every live environment of P: a lookup on every read, which hides
     deleted-env and env-less rows.

   I recommend `['(none)']`. Nothing selected stays `[]`. Containment changes
   what the growth and funnel boards show for an app-only selection (section
   4, "Containment or today's behaviour"). It must be settled before release,
   and a later opt-in (`"whenUnselected": "all"`) stays possible.

3. **Project identity: the name, not `hosted_projects.id`.** The issue asks
   for id and name. Environments carry only the name, which is unique and
   immutable within an org, so I recommend the name only. Is there a case for
   ids, such as a project rename?
4. **Restrict `projectEnvs` to `scope: org` for now?** I recommend yes,
   because widening later is compatible.
5. **Ship the toggle readers in the same change?** Toggles are declared and
   compiled today, but I found no reader that binds them. I recommend yes,
   since both use the same `bindings` path.
6. **Follow-ups in or out?** (a) A structured panel referencing a set to
   narrow its environment predicate. (b) Environment context for postgres
   panels, which bind none today. (c) Option A's `reliant_env_projects` as a
   display aid. I recommend all three out, filed separately when a panel needs
   them.
7. **A lint against environment-id literals in sql panels?** It would be a
   warn-first `forge lint` rule that flags a UUID literal compared with
   `EnvId` or `{env_ids}`, pointing at `projectEnvs`. It is cheap and would
   have caught #2614. I recommend yes, as a separate PR.
