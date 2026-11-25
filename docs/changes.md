# Declaring changes

A **change** is one piece of app behavior that can differ between users without a
deploy: remote config, a gradual rollout, or an A/B test. Every change has a default
and one or more named variants. The control plane decides who gets which variant
(Everyone, a stable percentage, or a split between variants). This repo decides what
a change _can_ be: its key, its value type, its variants and its default.

Changes are in **beta** until platform delivery, the readout and the Experiments
screen have all shipped. The declaration below, the evaluation SDKs and typed
accessors are released; until the platform delivers an environment's policy to
an app, every accessor returns the declared default.

You write that once, in `changes.yaml` at the project root. forge ships it in every
environment's release and generates typed accessors for your code. The design is in
[proposals/changes.md](proposals/changes.md), and the evaluation model is in
`pkg/changes`.

## The file

```yaml
version: 1
changes:
  new-checkout:
    description: Shorter checkout flow
    type: bool # bool | string | number | json
    default: control # a variant name
    variants:
      control: false
      treatment: true
  hero.copy:
    type: string
    default: a
    variants:
      a: Ship faster
      b: Deploy in one command
```

| Field         | Rule                                                                                                                 |
| ------------- | -------------------------------------------------------------------------------------------------------------------- |
| change key    | `^[a-z][a-z0-9_.-]{0,63}$`, unique. It is the key the platform and the snapshot use.                                 |
| `type`        | `bool`, `string`, `number` (a finite float64) or `json` (any value but `null`). Every variant's value has this type. |
| `variants`    | 1 to 32 entries, each named `^[a-z][a-z0-9_-]{0,31}$`. A value is at most 8 KiB of JSON.                             |
| `default`     | One of the variants. It applies when the change is off, and to every caller its delivery does not select.            |
| `description` | Optional, at most 500 bytes. It is shown in the console and becomes the accessor's doc comment.                      |

A project can declare at most 200 changes. The file is optional. Without it, forge
generates nothing and the release carries no `changes` key, so the release is exactly
what it was before.

When the file is invalid, `forge generate` and every release build (`forge env build`,
`forge env shape`) refuse it. The refusal lists every problem with its line number:

```
changes.yaml is invalid:
  changes.yaml:5: change "k": default "on" is not one of its variants (control, treatment)
```

Two keys whose accessor names would collide (`new-checkout` and `new_checkout` both
become `NewCheckout`) are refused too.

## What forge does with it

**The release.** Every env's release shape carries the declaration under `changes`,
keyed by change key:

```json
"changes": {
  "new-checkout": {
    "type": "bool",
    "default": "control",
    "variants": { "control": false, "treatment": true },
    "description": "Shorter checkout flow"
  }
}
```

The control plane uses this to refuse launching or ramping a variant that the env's
current release does not declare. The field is the `shape-changes` bundle feature, so
forge sends it only to a control plane that decodes it. `forge env shape <env>` lists
the declared changes. A deploy whose release no longer declares a variant a running
change uses stops that change; `forge env deploy`'s plan says so first.

**Go** (projects with a `go.mod`): `internal/changes/changes_gen.go` has one constant
and one accessor per change:

```go
const KeyNewCheckout = "new-checkout"

func NewCheckout(ctx context.Context, s *changes.Store, ec changes.EvalContext) bool {
	return changes.Bool(s.Evaluate(ctx, KeyNewCheckout, ec), false)
}
```

The literal default is the value of the declared default variant. A `string` change
returns a `string`, and a `number` change returns a `float64`. A `json` change takes a
pointer, `func Theme(ctx, s, ec, v any) bool`. It decodes the chosen value into `v`
and returns true. If the snapshot does not decide, it decodes the declared default
into `v` and returns false.

Open one store per process and pass it to the accessors. A hosted workload's snapshot
path is in `FORGE_CHANGES_FILE`, which `changes.EnvFile` names:

```go
store := changes.OpenFile(ctx, os.Getenv(changes.EnvFile), changes.Options{})
defer store.Close()
if appchanges.NewCheckout(ctx, store, changes.EvalContext{TargetingKey: userID}) {
	// the new checkout
}
```

**TypeScript**: every Next.js or Vite frontend that depends on
`@reliantlabs/forge-web-runtime` gets `src/lib/changes_gen.ts`, with one key constant
and one accessor per change:

```ts
export const newCheckoutKey = "new-checkout";

export function newCheckout(
  store: forge.ChangesStore,
  ctx?: forge.ChangeEvalContext,
): boolean {
  const r = store.evaluate(newCheckoutKey, ctx);
  return forge.changeBool(r, false);
}
```

```ts
const store = await loadChanges();
if (newCheckout(store, { targetingKey: userId })) {
  /* ... */
}
```

A frontend that does not depend on the runtime gets no file, and `forge generate`
says so. React Native frontends get none.

Accessors never throw or fail. If the snapshot does not carry a change, or the store
could not load, you get the declared default. Each change is its own `forge:section`
in both files, so two branches that edit different changes merge cleanly.

Edit `changes.yaml` and run `forge generate`, and the accessors follow. Delete the file
and the next run removes them, unless you edited a generated file or took it over with
`forge project disown`.
