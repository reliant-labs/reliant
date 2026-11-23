---
name: migration-v1.0.0-connect-errors
description: Move hand-built connect.NewError calls onto pkg/svcerr — run forge fix connect-errors (wire-identical), then convert its worklist by hand with svcerr.WrapOr, and flip forgeconv-handler-connect-error to error.
relevance: migration
---

# v1.0.0 — hand-built Connect errors → `svcerr`

Use when a handler package (`internal/handlers/**`) calls `connect.NewError`,
or `forge lint --conventions` reports `forgeconv-handler-connect-error`.

### What changed

`connect.Error.Message()` is literally the wrapped error's `Error()`, and
`svcerr.Interceptor` passes a `*connect.Error` through exactly as built — it is
the handler's own verdict, so nothing redacts it. So
`connect.NewError(connect.CodeInternal, err)` publishes a driver's SQL, a DSN or
an internal host to the caller, and pins a guessed code over a NotFound or a
deadline the collaborator already classified. Three things make the fix
mechanical:

- `svcerr.WrapOr(err, fallback)` — a classified `err` keeps its own code and
  message; anything else reads `fallback` and is logged as the cause.
- `forgeconv-handler-connect-error` — a warning on every `connect.NewError` in a
  handler package, phrased for its shape. `forgeconv-no-handler-error-mapping`
  no longer goes quiet because a file imports svcerr.
- `forge fix connect-errors` — rewrites the sites where svcerr says exactly
  what the client read before, and lists the rest.

`tdd.AssertConnectError` reads `svcerr.Code` (the code the interceptor sends),
and generated proto mocks return `svcerr.Wrap(svcerr.Unimplemented(…))`.

### What to do

1. **Run the codemod** on a clean tree, then review `git diff`:

   ```bash
   forge fix connect-errors --dry-run   # every rewrite as file:line: old → new, then the worklist
   forge fix connect-errors
   ```

   Every `file:line` in the report is the file as it is on disk when the run
   ends — after a real run, the rewritten file's lines — so the worklist can be
   committed straight from the run that wrote the diff.

   **One lane of a shared checkout?** Bound the run to your slice, as
   `forge lint --scope` does:

   ```bash
   forge fix connect-errors --scope internal/handlers/worktree --scope internal/handlers/project
   ```

   Only files under the paths are rewritten and reported on; the reader
   analysis still reads the whole module, so a reader in another lane's
   package still keeps yours wrapped (and is named). A test outside the scope
   that the rewrite would switch to `svcerr.Code` is listed, not rewritten —
   and when that test is the package's OWN (it calls the package directly),
   the package stays wrapped, with that test named, until the file is in
   scope. The clean-tree check looks only inside the scope.

   It **never changes a code or a message**:

   | Before | After |
   |---|---|
   | `connect.NewError(connect.CodeNotFound, errors.New("chat not found"))` | `svcerr.NotFound("chat")` |
   | `connect.NewError(connect.CodeNotFound, errors.New("no such chat"))` | `svcerr.WithMessage(svcerr.ErrNotFound, "no such chat")` |
   | `connect.NewError(connect.CodeX, errors.New("…"))` | `svcerr.X("…")` |
   | `connect.NewError(connect.CodeX, fmt.Errorf(f, args…))` (no `%w`, no error argument) | `svcerr.X(fmt.Sprintf(f, args…))` |
   | the same in a `*connect.Error`-typed result, outside `handlers/`, or where a `*connect.Error` is read in process | `svcerr.ToConnect(svcerr.X(…))` |
   | `connect.CodeOf(x)` in the tests of a package that now returns bare kinds (and its importers) | `svcerr.Code(x)` |

   A bare kind is written only in a handler package — it reaches the client
   with its code through `svcerr.Interceptor`, which forge's chain builders
   install innermost — and only where nothing reads a `*connect.Error` in
   process (`connect.CodeOf`, `errors.As` into one, a type assertion, or an
   As-style helper such as testify's `require.ErrorAs` handed a
   `**connect.Error`) where it can see the package's errors:

   | The reader is… | How the report says it reaches the package |
   |---|---|
   | in the package's code | `in the package` |
   | in the package's own tests | `in its own tests, which call it directly` |
   | in the code of a package that imports it | `internal/app imports it` |
   | in the code of a package calling it through an interface one of its types implements, where production code hands it that type (the consumer-declared seam composition wires it into — nothing is imported; an interface only ever handed a generated client is not one) | `internal/triggertools calls it through the interface triggertools.TriggerAPI.ListTriggers at …:58, which *trigger.Service implements and internal/app/providers.go:449 hands it` |

   Those packages keep `svcerr.ToConnect(…)`, and the report lists **every**
   reader keeping each one — file:line, the enclosing function, the kind of
   read, and the reach — on every run, including a re-run with nothing left to
   rewrite. Not readers: an import by **tests alone** (those tests are the
   importer's; what they read is theirs), and a read of an error a **Connect
   client returned** — a `*connect.Client` / `*ForClient` stream method or a
   generated `<svc>connect` client, directly or through a function of the
   module whose every error is one (or `nil`, or a fresh
   `errors.New`/`fmt.Errorf`): that error came off the wire and stays a
   `*connect.Error` whatever the handler returns. A reader further away is not
   traced. A `connect.NewError` inside an interceptor stays a
   `*connect.Error` the same way: interceptors run outside `svcerr.Interceptor`.

   Category D — `if err != nil { return nil, connect.NewError(CodeInternal,
   errors.New("failed to list projects")) }` — becomes
   `svcerr.Internal("failed to list projects")`, still discarding `err`. That is
   deliberate: changing what the client reads is step 2's reviewed decision.

2. **Convert the worklist by hand** ("left as written" under Next). Each line
   says why the codemod left it:

   - *wraps an error value* (`err`, `fmt.Errorf("…: %w", err)`, an error or
     `.Error()` argument) → `svcerr.WrapOr(err, svcerr.<Kind>("<summary>"))`.
     The text is redacted and a classified `err` passes through — a NotFound
     from the store stays NotFound, a deadline reads DeadlineExceeded — so this
     changes what clients read: pin the codes the web/CLI depend on in tests.
     Text the user SHOULD read (a daemon's git rejection) goes in the
     constructor's argument instead. A remote Connect client's error is never
     `WrapOr`ed: `svcerr.Wrap(svcerr.WithCause(svcerr.Unavailable("…"), err))`.
   - *nil error* → the sentinel `svcerr.Err<Kind>` (the message becomes the
     category, e.g. "unauthenticated") or a constructor with real text.
   - *computed message* / *empty message* → a constructor, if the text can
     never be empty.
   - *computed code* → name the kind at the site, or move the mapping to the
     producer (its sentinels become svcerr kinds; the handler does
     `svcerr.Wrap(err)`).

   Delete the `logging.Error(…, "error", err)` line beside each converted
   return: the logging interceptor logs the cause with the request id.

3. **Switch in-process readers** the report named to `svcerr.Code(err)` (it
   agrees with `connect.CodeOf` for a `*connect.Error` and reads a bare kind;
   an `errors.As` into a `*connect.Error` becomes `svcerr.Code` or an
   `svcerr.Is*` predicate), then re-run `forge fix connect-errors`: it drops the
   `svcerr.ToConnect(…)` wrappers it left in that package once no reader is
   left — and if one is, the re-run names it.

4. **Flip the gate** once `forge lint --conventions` shows no
   `forgeconv-handler-connect-error` and no `forgeconv-no-handler-error-mapping`:

   ```yaml
   lint:
     rules:
       forgeconv-handler-connect-error: error
       forgeconv-no-handler-error-mapping: error
   ```

5. `go build ./... && go vet ./...`, the handler tests, and one test per
   reviewed change through a real Connect client (the code, and
   `x-forge-error-reason` where the frontend routes on it).
