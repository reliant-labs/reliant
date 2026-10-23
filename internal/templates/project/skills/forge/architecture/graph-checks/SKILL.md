---
name: graph-checks
description: What forge lint checks about the composition graph — reach (forge-graph-unreachable, //forge:standby, declaring roots one at a time), root-only nodes (//forge:root-only), audience (//forge:audience), the least-knowledge Deps rules under --conventions and their severities, forge fix narrow-deps, and migrating a pre-graph Infra/NewComponents project with forge fix composition-graph.
---

# Checking the whole graph, and what each node knows

The graph itself — providers, `compose.go`, roots and commands, each root's config reach — is in `architecture`, "The composition graph". This is what forge checks about it.

`forge lint --graph` judges the graph as `forge generate` assembles it, and fails on an error:

- **Reach.** Every service, package, worker and operator, and every provider, is reached by some root — a node no root reaches is never constructed, so a service in that position is served by no process. A provider kept ready for components to come carries `//forge:standby` on its func (the scaffolded `NewORM` and `NewHTTPClient` do). A sibling binary's service is `// forge:unmanaged` in its proto.
  Resolution is scoped by reach too. A Deps field forge cannot fill on a node a root reaches fails `forge generate`; on a node no root reaches it does not — that node, and every node taking it, is left out of `graph_gen.go` (no accessor, no injected `new<X>`) and reported here, always as an error. So a project with many components declares its roots one at a time: hold `lint.rules: {forge-graph-unreachable: warn}` in `forge.yaml` while the rest are unreached, and delete the line when the last root lands.
- **Root-only nodes.** A component two roots wire differently gets a type per root — the design's identity is the type — but a distinct type alone does not keep a root's variant to its root: resolution fills an interface-typed `Deps` field with whatever single node satisfies it, so a worker's privileged daemon router would be handed to every handler whose `Router` it satisfies. Mark the variant's provider `//forge:root-only <root>[,<root>...]`: resolution then offers it only to a field of its very type, and `forge generate` fails, naming the edge, when a root it does not name reaches it — whether forge or your `compose.go` drew the edge. A directive naming no declared root is an error too.
- **Audience.** Every service a listener's routes reach declares the audience of the authenticator its chain reaches: the provider func that builds it carries `//forge:audience <name>` (the scaffolded `NewUserAuth` is `//forge:audience user`). A daemon service on the user listener is an error before anything boots; the listener refuses the same mismatch at boot.

`forge lint --conventions` reads every component's `Deps` against what the package calls through it (least knowledge):

| Rule | Flags | Severity |
|---|---|---|
| `forgeconv-deps-surface` | an interface field declaring methods the package never needs through it: never calls, and never passes on as an interface (a parameter, a struct field) that declares them | error for a foreign interface of more than ten methods; warning otherwise |
| `forgeconv-no-container-deps` | a field holding a container: an `internal/app` type, `cmd.Deps`, a `map[string]any` locator, `any` | error |
| `forgeconv-deps-no-downcast` | a type assertion turning a field back into a concrete type | error |
| `forgeconv-no-getenv` | `os.Getenv` / `LookupEnv` / `Environ` in a component package: configuration its root's reach cannot see | warning |
| `forgeconv-deps-root-config` | a field holding the root `*config.Config` instead of the component's own block | `--strict` only, a warning (the scaffold still hands every component the root config) |

`--strict` makes every finding but the last an error.

`forge fix narrow-deps internal/handlers/<svc>` fixes the first: it declares, in the package (its `contract.go`, or `deps.go`), an interface holding exactly the methods the package uses — those it calls, with each interface it passes the value on as embedded, so every call that hands it on still type-checks — retypes the field, and writes its mock (`mock_gen.go`, kept in step by `forge generate`). `compose.go` needs no change — the value it hands the field still satisfies the narrower interface.

A project scaffolded before the graph keeps its `Infra`/`OpenInfra` + `NewComponents` composition, and forge generates for it exactly as before, until it migrates: `forge fix composition-graph` converts what forge scaffolded, or lists (writing nothing) each shape of yours it cannot; `migrations/v0.1.47` is the rest of the path.

## Pinning a root's start and stop order

`graph_gen.go` emits, per root, `Plan<Root>() graphkit.Plan`: every node `Build<Root>` constructs, in order, with the lifecycle methods its type has (the probe listener first for a root with no API listener). `Plan.Stop()` is the order `Run<Root>` stops the supervised nodes — the root's first field first — so a unit test pins it without constructing a database or a broker:

```go
func TestGatewayStopsTheHandOverFirst(t *testing.T) {
	if got := app.PlanGateway().Stop(); got[0] != "daemon_listener" {
		t.Fatalf("stop order %v", got)
	}
}
```
