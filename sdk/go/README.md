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
`http` to the local machine only, for a dev server. `reliant.WithInsecureHTTP()`
permits it to any host, for a self-hosted server on a network you trust: the
token crosses that network in the clear.

A webhook trigger needs no API token: `reliant.NewWebhook(baseURL, triggerID,
reliant.WebhookBearer(token))` delivers with the trigger's own credential.

## What is in it

| | |
|---|---|
| `Runs` | `Start`, `Get`, `List`, `LastPerWorkflow`, `Signal`, `Cancel`, `Pause`\*, `Resume`\*, `Interrupt`\*, `CancelWithResult`, `PauseWithResult`\*, `ResumeWithResult`\*, `Follow`, `Status`, `Nodes`, `ContentBlockBodies` |
| `Gates` | `Pending`, `Answer` |
| `Inbox` | `List` (filters: project, kinds, automation; follows truncation), `Counts`, `Dismiss`, `Restore` |
| `Approvals` | `List` (by chat), `ListForRun`, `Approve`, `Deny`, `BatchApprove`, `BatchDeny` (these four need an explicitly granted `approvals:decide`) |
| `Triggers` | `Get`, `List`, `Fire`, `Events`, `LaunchEvent` |
| `NewWebhook` | `POST /hooks/{id}` with a bearer token, a path token or an HMAC signature |
| `reliant/follow` | the follow engine, if you have your own source of updates |

\* beta: their semantics are still settling.

Lists (`Runs.List`, `Triggers.List`, `Triggers.Events`) return an `*Iter` that
follows page tokens lazily. Failures are `*reliant.Error`; branch on
`errors.Is(err, reliant.ErrNotFound)` and friends (`ErrUnauthenticated`,
`ErrPermissionDenied`, `ErrUnavailable`, `ErrInvalidArgument`,
`ErrFailedPrecondition`, `ErrAmbiguous`, `ErrNotApplied`, `ErrTruncated`, `ErrScopeMissing`, `ErrMachineCredential`, `ErrApprovalNotPending`).

## "Did it succeed?"

A run's **status** (did it finish) and its **outcome** (did the work pass) are
different facts. A run that fails verification routes to its workflow's failure
node and finishes cleanly: status `completed`, outcome `failure`.
`FollowResult.Succeeded`, `RunStatus.Succeeded` and `reliant.Succeeded` combine
the two; reading status alone is the false green.

## What is not in it

Anything that decides who a run executes as, where, or whether an automation runs
at all: enabling, creating or editing triggers. The inbox's machine
actions (`CleanupStorage`, `ForgetStorageWorktree`) are not here either. They are not in the
generated clients either (`proto/public-api.txt` in the repository lists exactly
which rpcs are public). Approval gates are reported by `Gates.Pending`.

### Deciding approvals

`Approvals.Approve`, `Deny`, `BatchApprove` and `BatchDeny` need the
`approvals:decide` scope **granted explicitly** on the token. `reliant:api` does
not stand in for it here, so every token minted for runs, triggers or the inbox
still cannot approve. A token bound to a machine (a daemon credential, which is
what a run executes with) is refused even if it holds `approvals:decide`. The
errors are `ErrScopeMissing` (`Error.Scopes` names the scope) and
`ErrMachineCredential`, both also `ErrPermissionDenied`; deciding what is
already resolved is `ErrApprovalNotPending`.

For an rpc this package does not wrap, build a generated client from the
authenticated HTTP client:

```go
runs := reliantv1connect.NewRunServiceClient(c.HTTPClient(), c.BaseURL())
```

## Versioning

Additive within `reliant.v1`. The public protos are generated into `gen/` from
the repository's `proto/` (`buf.gen.sdk-go.yaml`, run by `forge generate`), so
the SDK cannot drift from the server it ships with.

The module depends only on connect, protobuf and protovalidate: not on forge. The
public protos carry `forge.v1` options, and `scripts/sdk-go-gen.sh` drops the
`forgepb` import protoc-gen-go would add for them. The options stay in the
descriptors, undecoded; a program that links forge itself decodes them as usual.

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

Reliant's own app builds against this repository's `sdk/go`: the root
`go.mod` carries `replace github.com/reliant-labs/reliant/sdk/go => ./sdk/go`.
So an SDK change and the app code that uses it are **one PR**: change `sdk/go`
(and `proto/`, `proto/public-api.txt`, the generated `sdk/go/gen`), change the
app, and the merge gate builds and tests them together. There is no pin bump
after it merges and no re-pin after the squash (reliant#2824).

The replace binds only the root module, so nothing outside the repository sees
it:

- A program that requires the SDK resolves it from the module proxy, as above.
- A module that requires `github.com/reliant-labs/reliant` (control-plane)
  resolves the SDK by the root `go.mod`'s require line, which may name an
  older commit than the tree's. control-plane's `scripts/pin-sibling.sh`
  therefore pins the SDK at the same reliant commit it pins the app at.
- `go install github.com/reliant-labs/reliant/cmd/reliant@<version>` refuses a
  module whose `go.mod` has a replace, so a **release commit carries none**:
  the RC train's reliant pin commit runs `scripts/sdk-release-pin.sh <base>`,
  which drops the replace and requires the SDK at the commit the release was
  cut from (its `sdk/go` is checked to be byte-identical).

`internal/buildmode`'s `TestSDKModuleIsSelfContained` keeps the replace and a
real require line in the root `go.mod`, and `TestSDKReleasePinDropsTheReplace`
holds the release script. The SDK's own `go.mod` still carries no replace.

### Hosted check before adopting: resolved-approval replay

`Runs.Follow` attributes an approval to its run from the `workflow_id` and
`thread_id` the server writes on every approval feed update, its resolution
included (`internal/db/approval_close.go`, `AttributeApprovalUpdate`). That is
what lets a follower that attaches after an approval was resolved still replay
its open and close on the right run: a resolved approval is gone from the
pending-only `ListApprovalsByChat`. An update that names neither (written by a
server without the attribution) falls back to that pending list, so on such a
server a resolved approval's rows are skipped, not misattributed.

The in-memory tests (`TestFollowRunReplaysAnAlreadyResolvedApproval`) prove the
wire shape, not that the hosted server writes it. So after the server change
deploys, and **before a release that carries the SDK change is promoted**,
check it on the hosted server:

1. With an `rlat_` token for a test user, start a run whose workflow raises an
   approval gate, and note its run id. In a second run of the same session (or
   a sibling chat), raise another approval, so the session holds more than one
   run.
2. Approve the first run's approval in the app (this API cannot), and let the
   run finish.
3. Only then follow it: `c.Runs.Follow(ctx, runID, reliant.FollowOptions{})`,
   printing every event.

Expected: exactly one `approval` and one `approval_resolved` event with that
approval's id, the resolved one carrying `Status: "approved"`, both with the
followed run's `WorkflowID` (or a child workflow of it); no approval event of
the other run; then the run's terminal event and `Wait()` returning its
verdict. Following the second run shows only its own approval.

If the check fails (no `approval`/`approval_resolved` pair, or one attributed to
the other run), revert the SDK change on main before a release carries it:
the CLI builds against the tree's SDK (see "Changing the SDK" above), so the
revert returns it to the code it ran before, with nothing else to undo. A server that does not attribute
approvals degrades the SDK to the pending-list fallback, so reverting the
server change is safe on its own; fix the attribution and repeat the check.
