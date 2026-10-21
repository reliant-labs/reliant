# Authoring an integration manifest

An integration is data: `internal/integrations/catalog/<id>/`. The Go structs in
`internal/integrations/manifest` (`reliantv1.IntegrationManifest`, which the loader
decodes strictly) are the one definition of the format. Everything else about the
format is generated from them.

## One source of truth

| Fact | Authored once in | Derived from it |
|---|---|---|
| The manifest format | `proto/reliant/v1/integration.proto` + the rules in `internal/integrations/manifest` (patterns, enums, bounds) | `internal/integrations/manifest/schema/*.schema.json` |
| An integration (actions, events, schemas) | its YAML under `catalog/<id>/` | the catalog index, tool definitions, pickers |

The schema is published at `internal/integrations/manifest/schema/`:

- `manifest.schema.json`: a whole manifest (what the loader decodes, after fragments are joined).
- `action.schema.json`: one file of `actions/`.
- `event.schema.json`: one file of `events/`.

Regenerate with `make generate-manifest-schema` (`go generate
./internal/integrations/manifest/schema`) after changing the proto or a rule in the
`manifest` package. `TestPublishedSchemasAreCurrent` fails when the checked-in files
are stale, and `TestCatalogManifestsSatisfyTheSchema` checks every catalog manifest
against them. Never edit the JSON by hand.

Every manifest file starts with a `yaml-language-server` comment so editors validate
and complete it:

```yaml
# yaml-language-server: $schema=../../manifest/schema/manifest.schema.json
```

(`actions/*.yaml`: `../../../manifest/schema/action.schema.json`; `events/*.yaml`:
`.../event.schema.json`; the path is relative to the file.)

## Layout

Small integrations are a single `manifest.yaml`. A large one is split:

```
catalog/github/
  manifest.yaml        id, connection, keywords, and the ordered lists below
  types.yaml           (optional) the value of the manifest's `types:` key
  actions/<id>.yaml    one action per file; the file name is the action id
  events/<id>.yaml     one trigger per file; the file name is the trigger id
  schemas/<name>.yaml  shared pieces, reached only through $ref
```

`manifest.yaml` lists its entries in the order they should appear:

```yaml
actions:
  - $ref: actions/user.get.yaml
  - $ref: actions/issue.create.yaml
triggers:
  - $ref: events/issue.opened.yaml
```

Order is meaningful (tool definitions and pickers list in it), so a manifest that
declares `actions:` or `triggers:` must name every file in its directory; an unlisted
file is a load error. A manifest with no `actions:` key takes the files in file-name
order. Entries may also be written inline.

## `$ref`

`{ $ref: ../schemas/owner.yaml }` stands for the content of that file, in place of the
whole mapping it appears in. It replaces YAML anchors, which cannot cross files.

- The path is relative to the file that contains it and must stay inside the
  integration's directory.
- A `$ref` takes no sibling keys, and a cycle is an error.
- Only a path ending in `.yaml`/`.yml` is a fragment reference. A JSON Schema
  `$ref: "#/$defs/x"` inside `params` or `data` is left alone.
- A file in `schemas/` that nothing references is an error.

## Value types

`types.yaml` (or `types:` in `manifest.yaml`) lists the value types an integration
defines; a workflow input or a trigger attribute names one as `<integration>.<id>`
(`github.repository`; inside the manifest a bare `repository` works too). A type is
data: nothing in Go knows what it is.

```yaml
- id: repository
  value: { type: string, pattern: "^[^/]+/[^/]+$", examples: [acme/app] }   # JSON Schema of ONE value
  options:                      # where a picker gets candidate values
    action: app.installation_repositories.list   # a read-only list action of this manifest
    params: { installation_id: "{{ has(scope.installation) ? dyn(int(scope.installation)) : dyn(null) }}" }
    search: client              # client, or param:<name> to pass the query to the action
    value: item.full_name       # CEL over one item of the action's output
    label: item.full_name
    annotations: { can_push: item.can_push }
    warnings:
      - when: "context.acting_identity == 'app' && !option.annotations.can_push"
        message: The bot cannot write here.
    scope: { installation: { type: installation } }   # other typed values the list depends on
```

Load checks everything that can be checked: the value schema compiles and its
examples are valid values; the action exists, does not mutate and runs on the
server; every projection compiles **and reads only fields the action's
`output.schema` declares**; `params` name params the action has and supply every
param it requires; `search`, `paging` and `scope` refer to real params, fields and
types; scopes do not form a cycle; and every attribute `type:` (and every scope type
naming another integration) resolves to a declared type. Projections see `item`
(one output item), and warnings also `option` and `context`; param templates see
`scope`, `query` and `page_token`, and may use the strings extension (`split`, ...).
A param template that renders to null leaves its param out.

`options.items` defaults to `output.items`, which is where a list action that
paginates returns its list.

### Actions that walk

Some listings are two calls deep (GitHub: your installations, then each one's
repositories). `request.fan_out` expresses that without code: the enclosing request
lists parents, and `fan_out.request` runs once per parent, its pages concatenated into
one list. The parent is `parent` in the per-parent templates and in `output.select`.
Both requests must paginate (even for one page), a walk is one level deep, more than
`max_items` parents (default 50) is an error rather than a silent cut, and a
non-retryable failure listed in `skip_status` drops that parent (it went away between
the two calls) while any other failure fails the action, so a partial list is never
returned as a complete one.

## Webhook events and their data schema

An event's `data:` is the JSON Schema of `trigger.payload.data`. For GitHub it is
**generated**, together with the Go that fills it, from one source:

```
internal/integrations/webhook/github/projection/
  provider.yaml            the pinned upstream schema, the caps, the envelope every event carries
  objects/<name>.yaml      a projected object several events share (user, pr, issue, ...)
  <trigger id>.yaml        one event: its catalog prose, attributes and data
tools/integrationgen/upstream/octokit-webhooks-schemas/schema.json   the vendored upstream schema
```

`make generate-integrations` (part of `make generate`) turns that into

| Output | Replaces |
|---|---|
| `webhook/github/zz_projection_gen.go`: the `mappings` table and one attrs and one data func per event | the hand-written projections in `data.go` |
| `catalog/github/events/<id>.yaml`: attributes and the closed `data` schema | the hand-written event fragments |
| `catalog/github/schemas/<object>_schema.yaml` | the hand-written object schemas |
| the `triggers:` list of `catalog/github/manifest.yaml` (a new event is appended) | |

Schema and parser come from the same lines of a spec, so they cannot drift. What
stays by hand: the parser's routing (`parse.go`: the envelope, access, revocations,
our own bot), the helpers the generated code is written in (`data.go`), the shared
attribute declarations (`schemas/attr_*.yaml`), and the named hooks (`hooks.go`).
`TestEmittedDataConformsToTheDeclaredSchema` still holds the emitted `Data` to the
schema, now as a check on the generator and on the fixtures.

### What a spec says

```yaml
# projection/pull_request.closed.yaml
id: pull_request.closed            # the trigger id; the file is named for it
event: pull_request.closed         # <X-GitHub-Event>.<action>, or the bare event (push)
display_name: Pull request closed
summary: A pull request was closed or merged.
description: >-
  Fires when a pull request is closed. Match merged: "true" for merges only.
keywords: [pr, merged, merge]
attributes:
  - { group: pr }                  # branch + base_branch, defined once in provider.yaml
  - { name: merged, description: '"true" when the pull request was merged.', example: "true", from: pull_request.merged, kind: boolean }
data:
  number: integer
  pull_request: { kind: object, object: pr }     # a shared objects/pr.yaml
```

The envelope (`repository`, `installation_id`, `sender`, `action` attributes; `action`,
`repository`, `sender` data) is added to every event; `omit: [action]` drops one.

**Field kinds** (`data:` and `objects/*.yaml`; a bare word is a kind):

| Kind | Emits |
|---|---|
| `string` | the text at `from:` (default: the field name); `cap: true` cuts it at `caps.text_bytes`; `lower: true`; `omit_empty: true` leaves it out when empty; `enum:` is schema-only |
| `integer` | the JSON number as an int64; `omit_empty: true` leaves it out when absent |
| `boolean` | `value == true` |
| `present` | true when the value is a JSON object (a marker such as `issue.pull_request`) |
| `names` / `integers` | the `pluck:` key of each element of a list, at most `caps.list_items` |
| `strings` | a list of strings, at most `caps.list_items` |
| `object` | `object: <name>` (a shared object) or inline `fields:`; `omit_empty: true` drops it when the source is not an object |
| `list` | `object: <name>` per element, at most `max: <caps.lists key>` |

`optional: <why>` on a field excuses it from existing in every event's upstream
definition (a pull request in a review event is slimmer than a full one); it must
still exist in at least one. `unchecked: <why>` excuses a field from the upstream
check entirely.

**Attributes** are strings. `from:` is the payload path (several are tried in order);
`kind: boolean` or `hook: <name>` yield `"true"`/`"false"`; `lower:` and
`strip_prefix:` shape a string. `use: <name>` stands for a shared declaration in
`catalog/github/schemas/attr_<name>.yaml`. A hook is a `func(any) bool` in
`webhook/github/hooks.go` (`mentionsReliant`, `isObject`, `isSet`); logic that is
not a projection stays Go, named from the spec.

`shrink:` (push) says how an oversized payload is cut to `caps.data_bytes`: the list
loses the `drop` keys of each element, then elements from the end, and `flag` is set.

### Adding an event

1. Add the fixture: a recorded delivery as `webhook/github/testdata/<event>.<action>.json`
   (the file name starts with the `X-GitHub-Event` header).
2. Add `projection/<trigger id>.yaml` as above. Reuse `objects/` and the attribute
   groups; a new shared shape is a new `objects/<name>.yaml`.
3. `make generate-integrations`. It fails, naming the file and the path, if a kept path
   does not exist in the event's upstream definition (`issues$opened`, `push$event`) or
   upstream types it differently than the kind can read.
4. `go test ./internal/integrations/...`: conformance requires the fixture to produce
   every declared property, so a field no fixture exercises is a gap to fill.

No Go, no catalog YAML and no manifest edit by hand.

### Bumping the upstream schema

The upstream schema is a check, not a source of types: nothing is copied out of it.
It pins what GitHub says the payload contains, so a bump surfaces every field the
projection keeps that moved.

1. Download the new `schema.json` over the vendored one (`npm pack @octokit/webhooks-schemas@<v>`)
   and update `upstream.version` and `upstream.sha256` in `provider.yaml` (the generator
   refuses a file whose hash is not the pinned one). Update the notice in `LICENSES.txt`.
2. `make generate-integrations`. A renamed, removed or retyped field fails with the event,
   the spec and the path. Fix the spec (a rename is `from:`; a field GitHub dropped leaves
   the payload, which is a manifest `version` bump) and rerun. A new upstream event or field
   changes nothing until a spec asks for it.
3. `go test ./internal/integrations/...`: the conformance test runs the fixtures through the
   regenerated projection. Re-record fixtures if GitHub's shape changed.

`TestRepositoryGeneratedFilesAreCurrent` (and `go run ./tools/integrationgen -check`) fail
when a generated file differs from what the specs produce.

## Checking a change

```
make generate-manifest-schema generate-integrations
go test ./internal/integrations/... ./tools/integrationgen/...
```
