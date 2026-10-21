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

## Webhook events and their data schema

An event's `data:` is the JSON Schema of `trigger.payload.data`. The provider parser
(`internal/integrations/webhook/<id>`) produces it, and
`TestEmittedDataConformsToTheDeclaredSchema` (github) holds the two together: every
recorded fixture's emitted `Data` must validate against its event's schema, every
emitted property at any depth must be declared, and every declared property must be
produced by some fixture. To add a property, add it to the parser, the schema and a
fixture in `webhook/github/testdata`.

## Checking a change

```
make generate-manifest-schema
go test ./internal/integrations/...
```
