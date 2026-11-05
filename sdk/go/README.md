# Reliant Go SDK

`github.com/reliant-labs/reliant/sdk/go` is the Go client for Reliant's public
API: start and steer runs, follow them to a verdict, answer their questions, and
read and fire automations.

**License: Apache-2.0** (see [LICENSE](LICENSE) and [NOTICE](NOTICE)). The rest of
the repository is under the Business Source License; this directory is the
exception, so it can be embedded in your own software.

```go
import "github.com/reliant-labs/reliant/sdk/go/reliant"

c, err := reliant.FromEnv() // RELIANT_SERVER_URL (optional), RELIANT_TOKEN
if err != nil { /* ... */ }

run, err := c.Runs.Start(ctx, reliant.StartRun{
	Workflow: "builtin://agent",
	Message:  "fix the failing test",
})

f, err := c.Runs.Follow(ctx, run.GetId(), reliant.FollowOptions{})
for ev := range f.Events() {
	fmt.Println(ev.Event, ev.NodeID)
}
res, err := f.Wait()
if !res.Succeeded { // not res.Status == "completed": see below
	os.Exit(1)
}
```

## Authentication

Create an API token with `reliant auth token create --name <app>`: an `rlat_`
token with the `reliant:api` scope, acting as the user who created it. Pass it
with `reliant.WithToken`, or rotate it with `reliant.WithTokenSource`. Tokens last
90 days.

The base URL must be `https`. `reliant.WithInsecureLoopback()` permits plain
`http` to the local machine only, for a dev server.

A webhook trigger needs no API token: `reliant.NewWebhook(baseURL, triggerID,
reliant.WebhookBearer(token))` delivers with the trigger's own credential.

## What is in it

| | |
|---|---|
| `Runs` | `Start`, `Get`, `List`, `LastPerWorkflow`, `Signal`, `Cancel`, `Pause`\*, `Resume`\*, `Interrupt`\*, `Follow`, `Status`, `Nodes` |
| `Gates` | `Pending`, `Answer` |
| `Triggers` | `Get`, `List`, `Fire`, `Events`, `LaunchEvent` |
| `NewWebhook` | `POST /hooks/{id}` with a bearer token, a path token or an HMAC signature |
| `reliant/follow` | the follow engine, if you have your own source of updates |

\* beta: their semantics are still settling.

Lists (`Runs.List`, `Triggers.List`, `Triggers.Events`) return an `*Iter` that
follows page tokens lazily. Failures are `*reliant.Error`; branch on
`errors.Is(err, reliant.ErrNotFound)` and friends (`ErrUnauthenticated`,
`ErrPermissionDenied`, `ErrUnavailable`, `ErrInvalidArgument`,
`ErrFailedPrecondition`, `ErrAmbiguous`, `ErrNotApplied`).

## "Did it succeed?"

A run's **status** (did it finish) and its **outcome** (did the work pass) are
different facts. A run that fails verification routes to its workflow's failure
node and finishes cleanly: status `completed`, outcome `failure`.
`FollowResult.Succeeded`, `RunStatus.Succeeded` and `reliant.Succeeded` combine
the two; reading status alone is the false green.

## What is not in it

Anything that decides who a run executes as, where, or whether an automation runs
at all: enabling, creating or editing triggers, approving. They are not in the
generated clients either (`proto/public-api.txt` in the repository lists exactly
which rpcs are public). Approval gates are reported by `Gates.Pending` so a
supervisor can tell a person; this API cannot decide them.

For an rpc this package does not wrap, build a generated client from the
authenticated HTTP client:

```go
runs := reliantv1connect.NewRunServiceClient(c.HTTPClient(), c.BaseURL())
```

## Versioning

Additive within `reliant.v1`. The public protos are generated into `gen/` from
the repository's `proto/` (`buf.gen.sdk-go.yaml`, run by `forge generate`), so
the SDK cannot drift from the server it ships with.

The module has no tags yet. Require it at a commit of `main`, and Go resolves
that to a pseudo-version through the module proxy:

```sh
go get github.com/reliant-labs/reliant/sdk/go@<main commit sha>
```

`go.mod` carries no `replace`, and a module that requires the SDK must not add
one: a consumer ignores its dependencies' replace directives, and
`go install <module>@<version>` refuses a module that has one.
`consumer_test.go` builds a scratch module against the SDK as an external
dependency to hold that (`task test:sdk:full`).

### Changing the SDK, and the app that uses it

Reliant's own CLI is a consumer like any other, so an SDK change reaches it in
two steps, each its own PR:

1. **The SDK PR** changes `sdk/go` (and `proto/`, `proto/public-api.txt`, the
   generated `sdk/go/gen`) and lands on `main`.
2. **The pin bump** runs `go get github.com/reliant-labs/reliant/sdk/go@<the
   main commit where step 1 landed>` in the repository root and commits the
   `go.mod`/`go.sum` change, with any app code that uses the new API.

Never point the root module at `./sdk/go` with a `replace`. To work on both
halves at once in a checkout, use a local `go.work` that you do not commit.
